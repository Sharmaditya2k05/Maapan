package ledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// ErrCodePeriodLocked is the error code of the 409 raised when a voucher is
// dated inside a locked period.
const ErrCodePeriodLocked = "period_locked"

// PeriodLock is GET/PUT /v1/settings/period-lock.
type PeriodLock struct {
	LockedUntil string     `json:"locked_until"` // "" when the books are open
	LockedBy    string     `json:"locked_by,omitempty"`
	LockedAt    *time.Time `json:"locked_at,omitempty"`
}

func (s *Service) periodLock(ctx context.Context, q db.Querier, tenantID string) (*PeriodLock, error) {
	var until time.Time
	var by *string
	var at time.Time
	err := q.QueryRow(ctx, `SELECT locked_until, locked_by, locked_at FROM period_locks WHERE tenant_id=$1`, tenantID).Scan(&until, &by, &at)
	if db.IsNotFound(err) {
		return &PeriodLock{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &PeriodLock{LockedUntil: until.Format(time.DateOnly), LockedBy: deref(by), LockedAt: &at}, nil
}

// checkPeriodLock rejects date (YYYY-MM-DD) when it is on or before the
// tenant's locked_until. Post calls it for every voucher; manual callers see
// the 409, event handlers quarantine the event.
func checkPeriodLock(ctx context.Context, q db.Querier, tenantID, date string) error {
	var until time.Time
	err := q.QueryRow(ctx, `SELECT locked_until FROM period_locks WHERE tenant_id=$1`, tenantID).Scan(&until)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	u := until.Format(time.DateOnly)
	if date <= u {
		return &httpx.Error{Status: http.StatusConflict, Code: ErrCodePeriodLocked,
			Message: fmt.Sprintf("books are locked up to %s; a voucher dated %s cannot be posted", u, date),
			Details: map[string]any{"locked_until": u, "date": date}}
	}
	return nil
}

func isPeriodLocked(err error) bool {
	var e *httpx.Error
	return errors.As(err, &e) && e.Code == ErrCodePeriodLocked
}

func (s *Service) handleGetPeriodLock(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	pl, err := s.periodLock(r.Context(), s.DB, p.TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, pl)
}

type periodLockInput struct {
	LockedUntil string `json:"locked_until"` // YYYY-MM-DD; "" removes the lock
}

func (in *periodLockInput) Validate() error {
	if in.LockedUntil == "" {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, in.LockedUntil); err != nil {
		return httpx.Validation("locked_until must be YYYY-MM-DD")
	}
	return nil
}

// handlePutPeriodLock sets (or, with an empty locked_until, removes) the lock.
func (s *Service) handlePutPeriodLock(w http.ResponseWriter, r *http.Request) error {
	var in periodLockInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if in.LockedUntil > s.today() {
		return httpx.Validation("locked_until cannot be in the future")
	}
	var err error
	if in.LockedUntil == "" {
		_, err = s.DB.Exec(r.Context(), `DELETE FROM period_locks WHERE tenant_id=$1`, p.TenantID)
	} else {
		_, err = s.DB.Exec(r.Context(), `INSERT INTO period_locks (tenant_id, locked_until, locked_by) VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id) DO UPDATE SET locked_until=EXCLUDED.locked_until, locked_by=EXCLUDED.locked_by, locked_at=now()`,
			p.TenantID, in.LockedUntil, p.UserID())
	}
	if err != nil {
		return err
	}
	pl, err := s.periodLock(r.Context(), s.DB, p.TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, pl)
}
