package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Dead-letter reasons.
const (
	ReasonMaxAttempts   = "max_attempts"   // failed MaxAttempts times
	ReasonExpired       = "expired"        // still failing after RetryWindow
	ReasonNotConfigured = "not_configured" // provider lacks a key / template id
)

// DeadLetter is a message soochna gave up on.
type DeadLetter struct {
	ID          string     `json:"id"`
	MessageID   string     `json:"message_id"`
	TenantID    *string    `json:"tenant_id,omitempty"`
	Channel     string     `json:"channel"`
	Template    string     `json:"template"`
	To          string     `json:"to"`
	Attempts    int        `json:"attempts"`
	LastError   *string    `json:"last_error,omitempty"`
	Reason      string     `json:"reason"`
	DeadAt      time.Time  `json:"dead_at"`
	ReplayCount int        `json:"replay_count"`
	ReplayedAt  *time.Time `json:"replayed_at,omitempty"`
	ReplayedBy  *string    `json:"replayed_by,omitempty"`
}

// deadLetter parks a message and raises the alert log line that the
// log-based metric soochna_dead_letters counts (runbook: dead-letter replay).
func (s *Service) deadLetter(ctx context.Context, messageID, reason string) error {
	var dl DeadLetter
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE messages SET status='dead', next_attempt_at=NULL, updated_at=now()
			WHERE id=$1 RETURNING id, tenant_id, channel, recipient, template, attempts, error`, messageID).
			Scan(&dl.MessageID, &dl.TenantID, &dl.Channel, &dl.To, &dl.Template, &dl.Attempts, &dl.LastError); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO dead_letters (message_id, tenant_id, channel, template, recipient, attempts, last_error, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (message_id) DO UPDATE SET attempts=EXCLUDED.attempts, last_error=EXCLUDED.last_error,
			  reason=EXCLUDED.reason, dead_at=now(), replayed_at=NULL, replayed_by=NULL`,
			dl.MessageID, dl.TenantID, dl.Channel, dl.Template, dl.To, dl.Attempts, dl.LastError, reason)
		return err
	})
	if err != nil {
		return err
	}
	tenant := ""
	if dl.TenantID != nil {
		tenant = *dl.TenantID
	}
	lastErr := ""
	if dl.LastError != nil {
		lastErr = *dl.LastError
	}
	s.Log.Error("ALERT: message dead-lettered", "alert", "soochna.dead_letter", "message_id", dl.MessageID,
		"tenant_id", tenant, "channel", dl.Channel, "template", dl.Template, "to", maskAddress(dl.To),
		"attempts", dl.Attempts, "reason", reason, "error", lastErr)
	return nil
}

// DeadFilter narrows the dead-letter list.
type DeadFilter struct {
	TenantID string // "" = all tenants (internal only)
	Template string
	Channel  string
	Reason   string
	All      bool // include already replayed rows
	httpx.Page
}

func (f DeadFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(col, v string) {
		if v != "" {
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("d.%s = $%d", col, len(args)))
		}
	}
	add("tenant_id", f.TenantID)
	add("template", f.Template)
	add("channel", f.Channel)
	add("reason", f.Reason)
	if !f.All {
		conds = append(conds, "d.replayed_at IS NULL")
	}
	if len(conds) == 0 {
		return "true", args
	}
	return strings.Join(conds, " AND "), args
}

// ListDead returns dead letters, newest first.
func (s *Service) ListDead(ctx context.Context, f DeadFilter) ([]DeadLetter, int, error) {
	cond, args := f.where()
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM dead_letters d WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT d.id, d.message_id, d.tenant_id, d.channel, d.template, d.recipient, d.attempts,
		d.last_error, d.reason, d.dead_at, d.replay_count, d.replayed_at, d.replayed_by
		FROM dead_letters d WHERE %s ORDER BY d.dead_at DESC LIMIT $%d OFFSET $%d`, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []DeadLetter{}
	for rows.Next() {
		var d DeadLetter
		if err := rows.Scan(&d.ID, &d.MessageID, &d.TenantID, &d.Channel, &d.Template, &d.To, &d.Attempts, &d.LastError,
			&d.Reason, &d.DeadAt, &d.ReplayCount, &d.ReplayedAt, &d.ReplayedBy); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// ReplayInput selects dead letters to resend: explicit ids, or every open one
// matching the filter (Template/Channel/Reason/TenantID), up to Limit.
type ReplayInput struct {
	IDs      []string `json:"ids,omitempty"` // dead_letters.id or messages.id
	TenantID string   `json:"tenant_id,omitempty"`
	Template string   `json:"template,omitempty"`
	Channel  string   `json:"channel,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Limit    int      `json:"limit,omitempty"` // default 100, max 500
}

// Validate checks the input.
func (in *ReplayInput) Validate() error {
	if len(in.IDs) > 500 {
		return httpx.Validation("at most 500 ids per replay")
	}
	if in.Limit <= 0 {
		in.Limit = 100
	}
	in.Limit = min(in.Limit, 500)
	return nil
}

// ReplayResult reports a replay.
type ReplayResult struct {
	Replayed   int            `json:"replayed"`
	Statuses   map[string]int `json:"statuses"`
	MessageIDs []string       `json:"message_ids"`
}

// ReplayDead resends dead letters now. Each replayed message starts a fresh
// retry cycle (attempts reset), so a message that fails again is retried by
// the scheduler and dead-lettered again after MaxAttempts.
func (s *Service) ReplayDead(ctx context.Context, in ReplayInput, by string) (*ReplayResult, error) {
	f := DeadFilter{TenantID: in.TenantID, Template: in.Template, Channel: in.Channel, Reason: in.Reason}
	cond, args := f.where()
	if len(in.IDs) > 0 {
		args = append(args, in.IDs)
		cond += fmt.Sprintf(" AND (d.id::text = ANY($%d) OR d.message_id::text = ANY($%d))", len(args), len(args))
	}
	args = append(args, by, in.Limit)
	var claimed []outgoing
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`UPDATE dead_letters d SET replayed_at=now(), replayed_by=$%d, replay_count=replay_count+1
			FROM (SELECT d.id FROM dead_letters d WHERE %s ORDER BY d.dead_at LIMIT $%d FOR UPDATE SKIP LOCKED) pick
			WHERE d.id = pick.id RETURNING d.message_id`, len(args)-1, cond, len(args)), args...)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(ids) == 0 {
			return err
		}
		rows, err = tx.Query(ctx, `UPDATE messages SET status='failed', attempts=0, next_attempt_at=NULL, updated_at=now()
			WHERE id = ANY($1) RETURNING id, COALESCE(tenant_id::text,''), channel, recipient, template, category, params,
			  COALESCE(subject,''), body, COALESCE(media_url,''), COALESCE(media_filename,'')`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o outgoing
			var params []byte
			if err := rows.Scan(&o.id, &o.tenantID, &o.channel, &o.to, &o.template, &o.category, &params,
				&o.subject, &o.body, &o.mediaURL, &o.mediaFilename); err != nil {
				return err
			}
			_ = json.Unmarshal(params, &o.params)
			claimed = append(claimed, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	res := &ReplayResult{Statuses: map[string]int{}, MessageIDs: []string{}}
	for _, o := range claimed {
		res.Statuses[s.resend(ctx, o)]++
		res.Replayed++
		res.MessageIDs = append(res.MessageIDs, o.id)
	}
	s.Log.Info("dead letters replayed", "count", res.Replayed, "by", by, "statuses", res.Statuses)
	return res, nil
}
