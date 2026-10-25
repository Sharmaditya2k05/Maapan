package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/buddhi/internal/anomaly"
	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/kpi"
	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/buddhi/internal/store"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/money"
)

// WeeklySystem is the (cached) instruction for the WhatsApp summary.
const WeeklySystem = `You write a short weekly WhatsApp update for an Indian shop owner about their business, in friendly Hinglish (Roman script).

Rules:
- At most 6 short lines, under 600 characters total. No markdown headings, no tables. 1-2 emojis at most.
- Use only the numbers given; quote rupee amounts exactly as given (already formatted).
- Mention: total sales and change vs last week, bills, best-selling items, and one practical suggestion (e.g. re-order, pending dues, a flagged issue) only if the data supports it.
- Do not invent targets, causes or advice not grounded in the data.`

// Weekly sends the Monday business summary for every AI-enabled tenant.
type Weekly struct {
	DB      *pgxpool.Pool
	Guard   *store.Guard
	KPI     *kpi.Service
	LLM     llm.Client
	Clients *clients.Clients
	Log     *slog.Logger
	Now     func() time.Time
}

// WeeklyResult reports one run.
type WeeklyResult struct {
	WeekStart string   `json:"week_start"`
	Tenants   int      `json:"tenants"`
	Sent      int      `json:"sent"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors"`
}

// Run summarises the last complete Monday–Sunday week. Idempotent per tenant/week.
func (w *Weekly) Run(ctx context.Context) (*WeeklyResult, error) {
	if !w.LLM.Enabled() {
		return nil, llm.ErrDisabled
	}
	today := w.Now().In(IST)
	offset := (int(today.Weekday()) + 6) % 7 // days since Monday
	thisMon := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, IST).AddDate(0, 0, -offset)
	weekStart := thisMon.AddDate(0, 0, -7)
	weekEnd := thisMon.AddDate(0, 0, -1)
	prevStart, prevEnd := weekStart.AddDate(0, 0, -7), weekStart.AddDate(0, 0, -1)
	res := &WeeklyResult{WeekStart: weekStart.Format(time.DateOnly), Errors: []string{}}

	tenants, err := w.Guard.EnabledTenants(ctx)
	if err != nil {
		return nil, err
	}
	res.Tenants = len(tenants)
	for _, t := range tenants {
		var exists bool
		if err := w.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM weekly_summaries WHERE tenant_id=$1 AND week_start=$2 AND sent)`,
			t.TenantID, weekStart).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			res.Skipped++
			continue
		}
		if err := w.one(ctx, t, weekStart, weekEnd, prevStart, prevEnd); err != nil {
			w.Log.Warn("weekly summary failed", "tenant_id", t.TenantID, "error", err.Error())
			res.Errors = append(res.Errors, t.TenantID+": "+err.Error())
			continue
		}
		res.Sent++
	}
	return res, nil
}

func (w *Weekly) one(ctx context.Context, t store.Settings, ws, we, ps, pe time.Time) error {
	ctx = auth.WithPrincipal(ctx, &auth.Principal{Internal: true, TenantID: t.TenantID})
	if err := w.Guard.Check(ctx, t.TenantID); err != nil {
		return err
	}
	d := func(x time.Time) string { return x.Format(time.DateOnly) }
	cur, top, err := w.KPI.Range(ctx, t.TenantID, kpi.AllStores, d(ws), d(we))
	if err != nil {
		return err
	}
	if cur.Bills == 0 {
		return nil // nothing sold, nothing to say
	}
	prev, _, err := w.KPI.Range(ctx, t.TenantID, kpi.AllStores, d(ps), d(pe))
	if err != nil {
		return err
	}
	rep, err := anomaly.List(ctx, w.DB, t.TenantID, "", "", ws, false)
	if err != nil {
		return err
	}

	phone := ""
	if t.OwnerPhone != nil {
		phone = *t.OwnerPhone
	}
	tenant, terr := w.Clients.Tenant(ctx, t.TenantID)
	if phone == "" && terr == nil {
		phone = tenant.OwnerPhone
	}
	if phone == "" {
		return fmt.Errorf("no owner phone")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Business: %s\nWeek: %s to %s\n", tenant.BusinessName, d(ws), d(we))
	fmt.Fprintf(&b, "Sales: %s (last week %s)\nBills: %d (last week %d)\nAverage bill: %s\nGST collected: %s\nCancelled bills: %d\n",
		money.Format(cur.SalesPaise), money.Format(prev.SalesPaise), cur.Bills, prev.Bills,
		money.Format(cur.AvgBillPaise), money.Format(cur.TaxPaise), cur.Cancelled)
	if cur.MarginBasePaise > 0 {
		fmt.Fprintf(&b, "Gross margin (items with known cost): %.1f%%\n", cur.MarginPct)
	}
	b.WriteString("Top items:\n")
	for i, it := range top {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "- %s: %s\n", it.Name, money.Format(it.SalesPaise))
	}
	fmt.Fprintf(&b, "Open issues flagged this week: %d\n", len(rep.Anomalies)+len(rep.Cancellations))

	text, usage, err := w.LLM.Chat(ctx, llm.ChatRequest{
		Tier: llm.Smart, Effort: "low", System: WeeklySystem, Prompt: b.String(), MaxTokens: 2000,
	})
	_ = w.Guard.Record(ctx, t.TenantID, "system", "weekly_summary", usage, err == nil)
	if err != nil {
		return err
	}
	if _, err := w.DB.Exec(ctx, `INSERT INTO weekly_summaries (tenant_id, week_start, text) VALUES ($1,$2,$3)
		ON CONFLICT (tenant_id, week_start) DO UPDATE SET text=$3`, t.TenantID, ws, text); err != nil {
		return err
	}
	if err := w.Clients.Send(ctx, clients.Message{
		Channel: "whatsapp", To: phone, Template: "weekly_summary", Params: map[string]string{"text": text},
		IdempotencyKey: "weekly:" + t.TenantID + ":" + d(ws),
	}); err != nil {
		return err
	}
	_, err = w.DB.Exec(ctx, `UPDATE weekly_summaries SET sent=true WHERE tenant_id=$1 AND week_start=$2`, t.TenantID, ws)
	return err
}
