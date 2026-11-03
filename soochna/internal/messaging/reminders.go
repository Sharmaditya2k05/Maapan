package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/money"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/templates"
)

// RemindersInput is the body of POST /internal/v1/jobs/payment-reminders.
type RemindersInput struct {
	AsOf      string   `json:"as_of,omitempty"`      // YYYY-MM-DD, default today (IST)
	TenantIDs []string `json:"tenant_ids,omitempty"` // narrow to these tenants (they must still have reminders on)
	DryRun    bool     `json:"dry_run,omitempty"`    // count what would be sent, send nothing
}

// Validate checks the input.
func (in *RemindersInput) Validate() error {
	if in.AsOf != "" {
		if _, err := time.Parse(time.DateOnly, in.AsOf); err != nil {
			return httpx.Validation("as_of must be YYYY-MM-DD")
		}
	}
	if len(in.TenantIDs) > 1000 {
		return httpx.Validation("at most 1000 tenant_ids")
	}
	return nil
}

// TenantReminders is the per-tenant outcome.
type TenantReminders struct {
	TenantID  string         `json:"tenant_id"`
	Overdue   int            `json:"overdue_parties"`
	Due       int            `json:"due_today"` // parties whose days_overdue is in reminder_days
	Statuses  map[string]int `json:"statuses"`
	NoContact int            `json:"no_contact"`
	Error     string         `json:"error,omitempty"`
}

// RemindersResult is the job response.
type RemindersResult struct {
	AsOf    string            `json:"as_of"`
	DryRun  bool              `json:"dry_run"`
	Tenants []TenantReminders `json:"tenants"`
}

// PaymentReminders asks bahi, per tenant that switched reminders on in the
// sender registry, for parties with overdue bills and messages those whose
// days overdue match the tenant's reminder_days (default 0, 7, 15, 30 — so a
// customer hears on the due date and then on a slowing cadence). Each
// party/day/channel is sent at most once (idempotency key), so the job can
// be re-run safely. Opted-out customers are suppressed as usual.
func (s *Service) PaymentReminders(ctx context.Context, in RemindersInput) (*RemindersResult, error) {
	if s.Bahi == nil {
		return nil, httpx.Unavailable("BAHI_URL is not configured")
	}
	asOf := in.AsOf
	if asOf == "" {
		asOf = s.now().In(IST).Format(time.DateOnly)
	}
	q := `SELECT tenant_id::text, reminder_days, reminder_channels FROM tenant_senders WHERE payment_reminders`
	args := []any{}
	if len(in.TenantIDs) > 0 {
		q += ` AND tenant_id::text = ANY($1)`
		args = append(args, in.TenantIDs)
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY tenant_id`, args...)
	if err != nil {
		return nil, err
	}
	type cfg struct {
		tenant   string
		days     []int
		channels []string
	}
	cfgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cfg, error) {
		var c cfg
		err := r.Scan(&c.tenant, &c.days, &c.channels)
		return c, err
	})
	if err != nil {
		return nil, err
	}

	var runID int64
	_ = s.DB.QueryRow(ctx, `INSERT INTO job_runs (job) VALUES ('payment-reminders') RETURNING id`).Scan(&runID)
	res := &RemindersResult{AsOf: asOf, DryRun: in.DryRun, Tenants: []TenantReminders{}}
	for _, c := range cfgs {
		res.Tenants = append(res.Tenants, s.remindTenant(ctx, c.tenant, asOf, c.days, c.channels, in.DryRun))
	}
	if runID != 0 {
		raw, _ := json.Marshal(res)
		_, _ = s.DB.Exec(ctx, `UPDATE job_runs SET finished_at=now(), result=$2 WHERE id=$1`, runID, raw)
	}
	return res, nil
}

func (s *Service) remindTenant(ctx context.Context, tenantID, asOf string, days []int, channels []string, dry bool) TenantReminders {
	out := TenantReminders{TenantID: tenantID, Statuses: map[string]int{}}
	t, err := s.Tenants.Get(ctx, tenantID)
	if err != nil {
		out.Error = "pehchaan: " + err.Error()
		return out
	}
	rows, err := s.Bahi.Overdue(ctx, tenantID, asOf)
	if err != nil {
		if errors.Is(err, clients.ErrOverdueUnsupported) {
			out.Error = clients.ErrOverdueUnsupported.Message
		} else {
			out.Error = "bahi: " + err.Error()
		}
		s.Log.Warn("payment reminders skipped for tenant", "tenant_id", tenantID, "error", out.Error)
		return out
	}
	out.Overdue = len(rows)
	for _, r := range rows {
		if r.OverduePaise <= 0 || !slices.Contains(days, r.DaysOverdue) {
			continue
		}
		out.Due++
		ins := reminderInputs(r, t.BusinessName, asOf, channels)
		if len(ins) == 0 {
			out.NoContact++
			continue
		}
		for _, in := range ins {
			if dry {
				out.Statuses["would_send"]++
				continue
			}
			res, err := s.Send(ctx, tenantID, in)
			if err != nil {
				out.Statuses["error"]++
				s.Log.Warn("payment reminder failed", "tenant_id", tenantID, "party_id", r.PartyID, "error", err.Error())
				continue
			}
			out.Statuses[res.Status]++
		}
	}
	return out
}

// reminderInputs builds the validated messages for one overdue party.
func reminderInputs(r clients.OverdueParty, business, asOf string, channels []string) []SendInput {
	name := r.PartyName
	if name == "" {
		name = "Customer"
	}
	params := map[string]string{
		"party_name": name, "business": business, "amount": money.Format(r.OverduePaise),
		"due_date": istDate(r.OldestDueDate), "days": strconv.Itoa(r.DaysOverdue),
	}
	if r.PayLink != "" {
		params["link"] = r.PayLink
	}
	var out []SendInput
	for _, ch := range channels {
		to := r.Phone
		if ch == templates.Email {
			to = r.Email
		}
		if to == "" {
			continue
		}
		in := SendInput{Channel: ch, To: to, Template: templates.PaymentReminder, Params: params,
			IdempotencyKey: "reminder:" + r.PartyID + ":" + asOf + ":" + ch}
		if in.Validate() == nil {
			out = append(out, in)
		}
	}
	return out
}
