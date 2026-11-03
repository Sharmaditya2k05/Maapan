package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/providers"
	"github.com/maapan/soochna/internal/templates"
)

// MaxAttempts is how often a failed message is tried before it is dead-lettered.
const MaxAttempts = 5

// RetryWindow is how long a failed message stays retryable; older failures are
// dead-lettered (a bill reminder three days late is worse than none).
const RetryWindow = 24 * time.Hour

// TenantLookup fetches a tenant's business name and owner phone (pehchaan).
type TenantLookup interface {
	Get(ctx context.Context, id string) (clients.Tenant, error)
}

// Bahi is what soochna needs from the ledger: party contact details and
// overdue receivables. Nil disables e-mail lookups and payment reminders.
type Bahi interface {
	Party(ctx context.Context, tenantID, partyID string) (*clients.Party, error)
	Overdue(ctx context.Context, tenantID, asOf string) ([]clients.OverdueParty, error)
}

// Service sends and tracks messages.
type Service struct {
	DB       *pgxpool.Pool
	SMS      providers.Sender
	WhatsApp providers.Sender
	Email    providers.Mailer
	Tenants  TenantLookup
	Bahi     Bahi
	Log      *slog.Logger

	WebhookToken string
	// Quiet holds promotional messages during TRAI quiet hours (21:00–09:00 IST by default).
	Quiet QuietHours
	// PromoRequiresOptIn sends promotional messages only to recipients with an
	// explicit opt-in (Meta's WhatsApp policy; recommended for SMS too).
	PromoRequiresOptIn bool
	// InvoicePDFURL is a URL pattern for the invoice PDF attached to invoice
	// messages, e.g. https://files.maapan.in/invoices/{tenant_id}/{invoice_id}.pdf.
	// Empty = no attachment. Placeholders: {tenant_id} {invoice_id} {number}.
	InvoicePDFURL string

	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) sender(channel string) providers.Sender {
	if channel == templates.WhatsApp {
		return s.WhatsApp
	}
	return s.SMS
}

// outgoing is one message ready for a provider.
type outgoing struct {
	id, tenantID                    string
	channel, to, template, category string
	params                          map[string]string
	subject, body                   string
	mediaURL, mediaFilename         string
}

// Send stores and delivers a message. tenantID is "" for pre-login messages.
// A repeated idempotency key returns the original message without resending.
// Customer messages to opted-out recipients are stored as "suppressed";
// promotional messages inside quiet hours are stored as "scheduled".
func (s *Service) Send(ctx context.Context, tenantID string, in SendInput) (*SendResult, error) {
	category := string(templates.CategoryOf(in.Template))
	body, err := templates.Render(in.Template, in.Channel, in.Params)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	var subject string
	if in.Channel == templates.Email {
		if subject, err = templates.Subject(in.Template, in.Params); err != nil {
			return nil, httpx.Validation("%s", err.Error())
		}
	}
	scope := "global"
	var tenant *string
	if tenantID != "" {
		scope, tenant = tenantID, &tenantID
	}

	status, reason, err := s.gate(ctx, tenantID, in.Channel, in.To, category)
	if err != nil {
		return nil, err
	}
	var nextAt *time.Time
	if status == StatusScheduled {
		t, _ := s.Quiet.NextAllowed(s.now())
		nextAt = &t
	}

	storedParams, storedBody := redact(in.Template, in.Params, body)
	paramsJSON, _ := json.Marshal(storedParams)
	var id string
	err = s.DB.QueryRow(ctx, `INSERT INTO messages (tenant_id, scope, channel, recipient, template, category, params, subject, body,
			media_url, media_filename, idempotency_key, status, error, next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT (scope, idempotency_key) DO NOTHING RETURNING id`,
		tenant, scope, in.Channel, in.To, in.Template, category, paramsJSON, nilIfEmpty(subject), storedBody,
		nilIfEmpty(in.MediaURL), nilIfEmpty(in.MediaFilename), nilIfEmpty(in.IdempotencyKey), status, nilIfEmpty(reason), nextAt,
	).Scan(&id)
	if db.IsNotFound(err) {
		var st string
		if err := s.DB.QueryRow(ctx, `SELECT id, status FROM messages WHERE scope=$1 AND idempotency_key=$2`,
			scope, in.IdempotencyKey).Scan(&id, &st); err != nil {
			return nil, err
		}
		return &SendResult{ID: id, Status: st}, nil
	}
	if err != nil {
		return nil, err
	}
	if status != StatusQueued {
		s.Log.Info("message not sent now", "id", id, "status", status, "reason", reason, "template", in.Template)
		return &SendResult{ID: id, Status: status, Reason: reason}, nil
	}
	st := s.dispatch(ctx, outgoing{
		id: id, tenantID: tenantID, channel: in.Channel, to: in.To, template: in.Template, category: category,
		params: in.Params, subject: subject, body: body, mediaURL: in.MediaURL, mediaFilename: in.MediaFilename,
	})
	return &SendResult{ID: id, Status: st}, nil
}

// gate decides whether a message may go now: queued (yes), suppressed
// (consent) or scheduled (quiet hours). OTPs and owner alerts always go.
func (s *Service) gate(ctx context.Context, tenantID, channel, to, category string) (status, reason string, err error) {
	blocked, why, err := s.Blocked(ctx, tenantID, channel, to, templates.Category(category))
	if err != nil {
		return "", "", err
	}
	if blocked {
		return StatusSuppressed, why, nil
	}
	if templates.Category(category) == templates.CategoryPromotional {
		if _, quiet := s.Quiet.NextAllowed(s.now()); quiet {
			return StatusScheduled, "quiet hours", nil
		}
	}
	return StatusQueued, "", nil
}

// dispatch calls the provider and records the outcome. It returns the new status.
func (s *Service) dispatch(ctx context.Context, o outgoing) string {
	ov, err := s.overrides(ctx, o.tenantID, o.channel, o.template)
	if err != nil {
		s.Log.Warn("sender registry lookup failed; using defaults", "tenant_id", o.tenantID, "error", err.Error())
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var ref, provider string
	if o.channel == templates.Email {
		provider = s.Email.Name()
		ref, err = s.Email.SendEmail(sctx, providers.Email{
			To: o.to, Template: o.template, Subject: o.subject, Text: o.body,
			AttachmentURL: o.mediaURL, AttachmentName: o.mediaFilename,
			FromAddress: ov.EmailFrom, FromName: ov.EmailFromName, ReplyTo: ov.EmailReplyTo,
		})
	} else {
		snd := s.sender(o.channel)
		provider = snd.Name()
		m := providers.Message{
			Channel: o.channel, To: o.to, Template: o.template, Params: o.params, Body: o.body,
			MediaURL: o.mediaURL, MediaFilename: o.mediaFilename, TemplateID: ov.TemplateID,
		}
		if o.channel == templates.WhatsApp {
			m.SenderID, m.AppName = ov.WhatsAppSource, ov.WhatsAppAppName
		} else {
			m.SenderID = ov.SMSSenderID
		}
		ref, err = snd.Send(sctx, m)
	}
	cancel()
	wctx := context.WithoutCancel(ctx)
	if err == nil {
		_, _ = s.DB.Exec(wctx, `UPDATE messages SET status='sent', provider=$2, provider_ref=$3, error=NULL,
			attempts=attempts+1, sent_at=now(), next_attempt_at=NULL, updated_at=now() WHERE id=$1`, o.id, provider, ref)
		return StatusSent
	}
	s.Log.Warn("message send failed", "id", o.id, "provider", provider, "template", o.template, "error", err.Error())
	return s.recordFailure(wctx, o, provider, err)
}

// recordFailure bumps attempts and either schedules a retry or dead-letters.
func (s *Service) recordFailure(ctx context.Context, o outgoing, provider string, sendErr error) string {
	msg := truncate(sendErr.Error(), 500)
	var attempts int
	if err := s.DB.QueryRow(ctx, `UPDATE messages SET status='failed', provider=$2, error=$3, attempts=attempts+1,
		next_attempt_at=NULL, updated_at=now() WHERE id=$1 RETURNING attempts`, o.id, provider, msg).Scan(&attempts); err != nil {
		s.Log.Error("could not record send failure", "id", o.id, "error", err.Error())
		return StatusFailed
	}
	if o.template == templates.OTP {
		// Never retried: the code is redacted in storage and would be stale.
		s.Log.Error("ALERT: OTP delivery failed", "alert", "soochna.otp_failed", "id", o.id, "provider", provider, "error", msg)
		return StatusFailed
	}
	reason := ""
	switch {
	case errors.Is(sendErr, providers.ErrNotConfigured):
		reason = ReasonNotConfigured // a missing template id will not fix itself
	case attempts >= MaxAttempts:
		reason = ReasonMaxAttempts
	}
	if reason != "" {
		if err := s.deadLetter(ctx, o.id, reason); err != nil {
			s.Log.Error("could not dead-letter message", "id", o.id, "error", err.Error())
			return StatusFailed
		}
		return StatusDead
	}
	_, _ = s.DB.Exec(ctx, `UPDATE messages SET next_attempt_at = now() + $2::interval WHERE id=$1`,
		o.id, Backoff(attempts).String())
	return StatusFailed
}

// Backoff is the wait before retry n (1-based): 1m, 5m, 15m, 1h.
func Backoff(attempts int) time.Duration {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(steps) {
		return steps[len(steps)-1]
	}
	return steps[attempts-1]
}

// RetryResult is the outcome of one retry run.
type RetryResult struct {
	Attempted  int `json:"attempted"`
	Sent       int `json:"sent"`
	Failed     int `json:"failed"`
	Dead       int `json:"dead"`
	Suppressed int `json:"suppressed"`
	Expired    int `json:"expired"`
	Scheduled  int `json:"scheduled_released"`
}

// RetryFailed resends due failed messages and releases scheduled (quiet-hours)
// messages. Failures older than RetryWindow are dead-lettered instead of being
// dropped silently. OTPs are never retried. Called by Cloud Scheduler.
func (s *Service) RetryFailed(ctx context.Context, limit int) (*RetryResult, error) {
	res := &RetryResult{}
	// Expired failures → dead letters.
	rows, err := s.DB.Query(ctx, `SELECT id FROM messages WHERE status='failed' AND template <> 'otp'
		AND created_at <= now() - $1::interval LIMIT $2`, RetryWindow.String(), limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.deadLetter(ctx, id, ReasonExpired); err != nil {
			return res, err
		}
		res.Expired++
	}

	// Claim due rows with a lease so overlapping runs never double-send.
	rows, err = s.DB.Query(ctx, `UPDATE messages m SET next_attempt_at = now() + interval '10 minutes', updated_at=now()
		FROM (SELECT id FROM messages
		      WHERE ((status='failed' AND template <> 'otp' AND attempts < $1) OR status='scheduled')
		        AND COALESCE(next_attempt_at, '-infinity') <= now()
		      ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED) due
		WHERE m.id = due.id
		RETURNING m.id, COALESCE(m.tenant_id::text,''), m.channel, m.recipient, m.template, m.category, m.params,
		          COALESCE(m.subject,''), m.body, COALESCE(m.media_url,''), COALESCE(m.media_filename,''), m.status`, MaxAttempts, limit)
	if err != nil {
		return nil, err
	}
	type job struct {
		o      outgoing
		status string
	}
	var jobs []job
	for rows.Next() {
		var j job
		var params []byte
		if err := rows.Scan(&j.o.id, &j.o.tenantID, &j.o.channel, &j.o.to, &j.o.template, &j.o.category, &params,
			&j.o.subject, &j.o.body, &j.o.mediaURL, &j.o.mediaFilename, &j.status); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(params, &j.o.params)
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.status == StatusScheduled {
			res.Scheduled++
		}
		res.Attempted++
		switch s.resend(ctx, j.o) {
		case StatusSent:
			res.Sent++
		case StatusDead:
			res.Dead++
		case StatusSuppressed:
			res.Suppressed++
		default:
			res.Failed++
		}
	}
	return res, nil
}

// resend re-checks consent (the recipient may have opted out since) and
// quiet hours, then dispatches.
func (s *Service) resend(ctx context.Context, o outgoing) string {
	status, reason, err := s.gate(ctx, o.tenantID, o.channel, o.to, o.category)
	if err != nil {
		s.Log.Warn("consent check failed; will retry", "id", o.id, "error", err.Error())
		return StatusFailed
	}
	switch status {
	case StatusSuppressed:
		_, _ = s.DB.Exec(ctx, `UPDATE messages SET status='suppressed', error=$2, next_attempt_at=NULL, updated_at=now() WHERE id=$1`, o.id, reason)
		return StatusSuppressed
	case StatusScheduled:
		t, _ := s.Quiet.NextAllowed(s.now())
		_, _ = s.DB.Exec(ctx, `UPDATE messages SET status='scheduled', next_attempt_at=$2, updated_at=now() WHERE id=$1`, o.id, t)
		return StatusScheduled
	}
	return s.dispatch(ctx, o)
}

// ListFilter narrows GET /v1/messages.
type ListFilter struct {
	Status, Channel, Template, To string
	httpx.Page
}

const messageCols = `id, tenant_id, channel, recipient, template, category, params, subject, body, media_url, media_filename,
	status, provider, provider_ref, error, attempts, next_attempt_at, created_at, sent_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	var params []byte
	err := row.Scan(&m.ID, &m.TenantID, &m.Channel, &m.To, &m.Template, &m.Category, &params, &m.Subject, &m.Body,
		&m.MediaURL, &m.MediaFilename, &m.Status, &m.Provider, &m.ProviderRef, &m.Error, &m.Attempts, &m.NextAttemptAt,
		&m.CreatedAt, &m.SentAt)
	if err == nil {
		_ = json.Unmarshal(params, &m.Params)
	}
	return m, err
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
		if p, err := NormalizeAddress(f.To); err == nil {
			add("recipient", p)
		}
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM messages WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM messages WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		messageCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
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

// maskAddress hides most of a phone / e-mail for logs.
func maskAddress(a string) string {
	if i := strings.IndexByte(a, '@'); i > 0 {
		return a[:1] + "***" + a[i:]
	}
	if len(a) > 4 {
		return strings.Repeat("*", len(a)-4) + a[len(a)-4:]
	}
	return "****"
}
