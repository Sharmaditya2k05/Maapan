package inventory

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// Service holds bhandar's dependencies.
type Service struct {
	DB    *pgxpool.Pool
	Relay *events.Relay
	Log   *slog.Logger
	Now   func() time.Time

	// ExpiryAlertDays is how far ahead the expiry scan looks (EXPIRY_ALERT_DAYS).
	ExpiryAlertDays int
}

const source = "bhandar"

// today returns the current calendar date in IST at midnight UTC-less form
// (year, month, day only), used for alert de-duplication and expiry maths.
func (s *Service) today() time.Time {
	t := s.Now().In(ist)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

var ist = time.FixedZone("IST", 5*3600+30*60)

// emit enqueues an event inside tx; the relay publishes it after commit.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, typ, tenantID, storeID string, data any) error {
	e, err := events.New(typ, source, tenantID, storeID, data)
	if err != nil {
		return err
	}
	return events.Enqueue(ctx, tx, e)
}

// kick asks the outbox relay to flush now.
func (s *Service) kick() {
	if s.Relay != nil {
		s.Relay.Kick()
	}
}

// storeFor resolves the store a request acts on: the explicit store_id when
// given (and accessible), else the principal's active store.
func storeFor(p *auth.Principal, storeID string) (string, error) {
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID == "" {
		return "", httpx.Validation("store_id is required")
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return "", httpx.Forbidden("no access to store %s", storeID)
	}
	return storeID, nil
}

// canAccessStore reports whether the principal may act on storeID.
func canAccessStore(p *auth.Principal, storeID string) bool {
	return p.Internal || p.Claims == nil || p.Claims.CanAccessStore(storeID)
}

// nilIfEmpty maps "" to NULL for optional text columns.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func dateString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}
