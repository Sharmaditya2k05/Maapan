package ledger

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/mool/logx"
)

// maxEventAttempts is how many deliveries of one event may fail with a
// retryable error before it is quarantined instead of retried again.
const maxEventAttempts = 3

// QuarantinedEvent is an event bahi could not post as delivered.
type QuarantinedEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	Reason     string          `json:"reason"`
	Payload    json.RawMessage `json:"payload"` // the full envelope
	CreatedAt  time.Time       `json:"created_at"`
	ResolvedAt *time.Time      `json:"resolved_at,omitempty"`
}

// quarantine parks e: in one transaction it marks the event processed and
// records why it could not be posted, then logs at ERROR and acknowledges
// (returns nil). Re-quarantining a retried event refreshes the row.
func (s *Service) quarantine(ctx context.Context, e events.Envelope, reason string) error {
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := db.MarkProcessed(ctx, tx, e.ID, e.Type); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO quarantined_events (event_id, tenant_id, event_type, reason, payload)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5)
			ON CONFLICT (event_id) DO UPDATE SET reason=EXCLUDED.reason, payload=EXCLUDED.payload, resolved_at=NULL`,
			e.ID, e.TenantID, e.Type, reason, e); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM event_attempts WHERE event_id=$1`, e.ID)
		return err
	})
	if err != nil {
		return err
	}
	logx.From(ctx).Error("event quarantined", "event_id", e.ID, "event_type", e.Type, "tenant_id", e.TenantID, "reason", reason)
	return nil
}

// recordAttempt counts one failed delivery of e and returns the total so far.
// It runs outside the (rolled back) handler transaction.
func (s *Service) recordAttempt(ctx context.Context, e events.Envelope, cause error) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `INSERT INTO event_attempts (event_id, attempts, last_error) VALUES ($1, 1, $2)
		ON CONFLICT (event_id) DO UPDATE SET attempts=event_attempts.attempts+1, last_error=EXCLUDED.last_error, updated_at=now()
		RETURNING attempts`, e.ID, cause.Error()).Scan(&n)
	return n, err
}

// handleListQuarantine lists the tenant's unresolved quarantined events.
func (s *Service) handleListQuarantine(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	page := httpx.ParsePage(r)
	var total int
	if err := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM quarantined_events WHERE tenant_id=$1 AND resolved_at IS NULL`,
		p.TenantID).Scan(&total); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT event_id, COALESCE(event_type,''), COALESCE(reason,''), payload, created_at, resolved_at
		FROM quarantined_events WHERE tenant_id=$1 AND resolved_at IS NULL ORDER BY created_at LIMIT $2 OFFSET $3`,
		p.TenantID, page.Limit, page.Offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []QuarantinedEvent{}
	for rows.Next() {
		var q QuarantinedEvent
		var payload []byte
		if err := rows.Scan(&q.EventID, &q.EventType, &q.Reason, &payload, &q.CreatedAt, &q.ResolvedAt); err != nil {
			return err
		}
		q.Payload = payload
		items = append(items, q)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[QuarantinedEvent]{Items: items, Total: total, Limit: page.Limit, Offset: page.Offset})
}

// handleRetryQuarantine re-runs the original handler on the stored envelope.
// The event is un-marked (processed_events, event_attempts) and optimistically
// resolved first; if the handler fails or quarantines it again the row is left
// (or put back) unresolved and the call answers 409 with the new reason.
func (s *Service) handleRetryQuarantine(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	id := r.PathValue("event_id")
	var raw []byte
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		var resolved *time.Time
		err := tx.QueryRow(r.Context(), `SELECT payload, resolved_at FROM quarantined_events WHERE tenant_id=$1 AND event_id=$2 FOR UPDATE`,
			p.TenantID, id).Scan(&raw, &resolved)
		if db.IsNotFound(err) {
			return httpx.NotFound("quarantined event not found")
		}
		if err != nil {
			return err
		}
		if resolved != nil {
			return httpx.Conflict("event %s is already resolved", id)
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM processed_events WHERE event_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM event_attempts WHERE event_id=$1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE quarantined_events SET resolved_at=now() WHERE event_id=$1`, id)
		return err
	})
	if err != nil {
		return err
	}
	unresolve := func() {
		_, _ = s.DB.Exec(r.Context(), `UPDATE quarantined_events SET resolved_at=NULL WHERE event_id=$1`, id)
	}
	var e events.Envelope
	if err := json.Unmarshal(raw, &e); err != nil || e.TenantID != p.TenantID {
		unresolve()
		return httpx.Conflict("stored payload is not a replayable envelope for this tenant")
	}
	h, ok := s.EventRoutes()[e.Type]
	if !ok {
		unresolve()
		return httpx.Conflict("no handler for event type %s", e.Type)
	}
	ctx := auth.WithPrincipal(r.Context(), &auth.Principal{Internal: true, TenantID: e.TenantID, StoreID: e.StoreID})
	ctx = logx.With(ctx, logx.From(ctx).With("event_id", e.ID, "event_type", e.Type, "retry", true))
	if err := h(ctx, e); err != nil {
		unresolve()
		return httpx.Conflict("retry failed, event stays quarantined: %s", err.Error())
	}
	var q QuarantinedEvent
	var payload []byte
	if err := s.DB.QueryRow(r.Context(), `SELECT event_id, COALESCE(event_type,''), COALESCE(reason,''), payload, created_at, resolved_at
		FROM quarantined_events WHERE tenant_id=$1 AND event_id=$2`, p.TenantID, id).Scan(
		&q.EventID, &q.EventType, &q.Reason, &payload, &q.CreatedAt, &q.ResolvedAt); err != nil {
		return err
	}
	q.Payload = payload
	if q.ResolvedAt == nil {
		return httpx.Conflict("event quarantined again: %s", q.Reason)
	}
	return httpx.OK(w, q)
}
