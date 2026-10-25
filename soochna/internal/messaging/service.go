package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/providers"
	"github.com/maapan/soochna/internal/templates"
)

// MaxAttempts is how often a failed message is tried before giving up.
const MaxAttempts = 5

// Service sends and tracks messages.
type Service struct {
	DB           *pgxpool.Pool
	SMS          providers.Sender
	WhatsApp     providers.Sender
	Tenants      *clients.Tenants
	Log          *slog.Logger
	WebhookToken string
}

func (s *Service) sender(channel string) providers.Sender {
	if channel == "whatsapp" {
		return s.WhatsApp
	}
	return s.SMS
}

// Send stores and delivers a message. tenantID is "" for pre-login messages.
// A repeated idempotency key returns the original message without resending.
func (s *Service) Send(ctx context.Context, tenantID string, in SendInput) (*SendResult, error) {
	body, err := templates.Render(in.Template, in.Channel, in.Params)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	scope := "global"
	var tenant *string
	if tenantID != "" {
		scope, tenant = tenantID, &tenantID
	}
	storedParams, storedBody := redact(in.Template, in.Params, body)
	paramsJSON, _ := json.Marshal(storedParams)

	var id string
	err = s.DB.QueryRow(ctx, `INSERT INTO messages (tenant_id, scope, channel, to_phone, template, params, body, media_url, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (scope, idempotency_key) DO NOTHING RETURNING id`,
		tenant, scope, in.Channel, in.To, in.Template, paramsJSON, storedBody, nilIfEmpty(in.MediaURL), nilIfEmpty(in.IdempotencyKey),
	).Scan(&id)
	if db.IsNotFound(err) {
		var status string
		if err := s.DB.QueryRow(ctx, `SELECT id, status FROM messages WHERE scope=$1 AND idempotency_key=$2`,
			scope, in.IdempotencyKey).Scan(&id, &status); err != nil {
			return nil, err
		}
		return &SendResult{ID: id, Status: status}, nil
	}
	if err != nil {
		return nil, err
	}
	status := s.deliver(ctx, id, providers.Message{
		Channel: in.Channel, To: in.To, Template: in.Template, Params: in.Params, Body: body, MediaURL: in.MediaURL,
	})
	return &SendResult{ID: id, Status: status}, nil
}

// deliver calls the provider and records the outcome. It returns the new status.
func (s *Service) deliver(ctx context.Context, id string, m providers.Message) string {
	snd := s.sender(m.Channel)
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ref, err := snd.Send(sctx, m)
	cancel()
	if err != nil {
		s.Log.Warn("message send failed", "id", id, "provider", snd.Name(), "template", m.Template, "error", err.Error())
		_, _ = s.DB.Exec(context.WithoutCancel(ctx), `UPDATE messages SET status='failed', provider=$2, error=$3,
			attempts=attempts+1, updated_at=now() WHERE id=$1`, id, snd.Name(), truncate(err.Error(), 500))
		return "failed"
	}
	_, _ = s.DB.Exec(context.WithoutCancel(ctx), `UPDATE messages SET status='sent', provider=$2, provider_ref=$3, error=NULL,
		attempts=attempts+1, sent_at=now(), updated_at=now() WHERE id=$1`, id, snd.Name(), ref)
	return "sent"
}

// RetryFailed resends failed messages from the last 24 hours (never OTPs —
// they are redacted and would be stale anyway). Called by Cloud Scheduler.
func (s *Service) RetryFailed(ctx context.Context, limit int) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, channel, to_phone, template, params, body, COALESCE(media_url,'')
		FROM messages WHERE status='failed' AND attempts < $1 AND template <> 'otp'
		  AND created_at > now() - interval '24 hours'
		ORDER BY created_at LIMIT $2`, MaxAttempts, limit)
	if err != nil {
		return nil, err
	}
	type job struct {
		id string
		m  providers.Message
	}
	var jobs []job
	for rows.Next() {
		var j job
		var params []byte
		if err := rows.Scan(&j.id, &j.m.Channel, &j.m.To, &j.m.Template, &params, &j.m.Body, &j.m.MediaURL); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(params, &j.m.Params)
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res := map[string]int{"attempted": len(jobs), "sent": 0, "failed": 0}
	for _, j := range jobs {
		res[s.deliver(ctx, j.id, j.m)]++
	}
	return res, nil
}

// DeliveryUpdate is a provider status callback normalised.
type DeliveryUpdate struct {
	Ref    string
	Status string // sent | delivered | read | failed
	Error  string
}

// ApplyWebhook verifies the token and applies provider delivery reports.
func (s *Service) ApplyWebhook(ctx context.Context, provider, token string, raw []byte) (int, error) {
	if s.WebhookToken == "" || token != s.WebhookToken {
		return 0, httpx.Unauthorized("bad webhook token")
	}
	var ups []DeliveryUpdate
	var err error
	switch provider {
	case "gupshup":
		ups, err = ParseGupshup(raw)
	case "msg91":
		ups, err = ParseMSG91(raw)
	default:
		return 0, httpx.NotFound("unknown provider %s", provider)
	}
	if err != nil {
		return 0, httpx.BadRequest("%s", err.Error())
	}
	n := 0
	for _, u := range ups {
		// Never move backwards (e.g. a late "sent" after "read").
		tag, err := s.DB.Exec(ctx, `UPDATE messages SET status=$3, error=COALESCE(NULLIF($4,''), error), updated_at=now()
			WHERE provider=$1 AND provider_ref=$2 AND
			  (CASE status WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END) <
			  (CASE $3 WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END)`,
			provider, u.Ref, u.Status, u.Error)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// ParseGupshup reads a Gupshup message-event callback.
func ParseGupshup(raw []byte) ([]DeliveryUpdate, error) {
	var ev struct {
		Type    string `json:"type"`
		Payload struct {
			ID      string `json:"id"`
			GsID    string `json:"gsId"`
			Type    string `json:"type"`
			Payload struct {
				Reason string `json:"reason"`
			} `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, err
	}
	if ev.Type != "message-event" {
		return nil, nil // inbound messages, user events: ignored
	}
	status := map[string]string{"sent": "sent", "delivered": "delivered", "read": "read", "failed": "failed"}[ev.Payload.Type]
	if status == "" {
		return nil, nil // enqueued, etc.
	}
	var out []DeliveryUpdate
	for _, ref := range []string{ev.Payload.ID, ev.Payload.GsID} {
		if ref != "" {
			out = append(out, DeliveryUpdate{Ref: ref, Status: status, Error: ev.Payload.Payload.Reason})
		}
	}
	return out, nil
}

// ParseMSG91 reads MSG91 delivery reports: a single object or an array of
// {"requestId", "status"}; status may be a word or MSG91's numeric code
// (1 delivered, 2 failed, 9 NDNC, 16 rejected, 17 blocked, 25 rejected).
func ParseMSG91(raw []byte) ([]DeliveryUpdate, error) {
	type rep struct {
		RequestID string          `json:"requestId"`
		Status    json.RawMessage `json:"status"`
		Desc      string          `json:"desc"`
	}
	var reps []rep
	if err := json.Unmarshal(raw, &reps); err != nil {
		var one rep
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, errors.New("unrecognised MSG91 report")
		}
		reps = []rep{one}
	}
	var out []DeliveryUpdate
	for _, r := range reps {
		st := strings.ToLower(strings.Trim(string(r.Status), `"`))
		var status string
		switch st {
		case "1", "delivered", "delivrd":
			status = "delivered"
		case "2", "9", "16", "17", "25", "failed", "rejected", "ndnc":
			status = "failed"
		case "8", "sent", "submitted":
			status = "sent"
		}
		if r.RequestID != "" && status != "" {
			out = append(out, DeliveryUpdate{Ref: r.RequestID, Status: status, Error: r.Desc})
		}
	}
	return out, nil
}

// ListFilter narrows GET /v1/messages.
type ListFilter struct {
	Status, Channel, Template, To string
	httpx.Page
}

// List returns a tenant's delivery log, newest first.
func (s *Service) List(ctx context.Context, tenantID string, f ListFilter) ([]Message, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(col, v string) {
		if v != "" {
			args = append(args, v)
			where = append(where, fmt.Sprintf("%s = $%d", col, len(args)))
		}
	}
	add("status", f.Status)
	add("channel", f.Channel)
	add("template", f.Template)
	if f.To != "" {
		if p, err := NormalizePhone(f.To); err == nil {
			add("to_phone", p)
		}
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM messages WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT id, tenant_id, channel, to_phone, template, params, body, media_url,
		status, provider, provider_ref, error, attempts, created_at, sent_at
		FROM messages WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var params []byte
		if err := rows.Scan(&m.ID, &m.TenantID, &m.Channel, &m.To, &m.Template, &params, &m.Body, &m.MediaURL,
			&m.Status, &m.Provider, &m.ProviderRef, &m.Error, &m.Attempts, &m.CreatedAt, &m.SentAt); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(params, &m.Params)
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
