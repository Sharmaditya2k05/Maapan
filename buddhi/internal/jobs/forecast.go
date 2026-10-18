// Package jobs holds buddhi's scheduled work (triggered by Cloud Scheduler) and
// the re-order report built from its output.
package jobs

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/buddhi/internal/clients"
	"github.com/taraju/buddhi/internal/forecast"
)

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// Forecaster fits demand models nightly and answers re-order questions.
type Forecaster struct {
	DB          *pgxpool.Pool
	Clients     *clients.Clients
	Log         *slog.Logger
	Now         func() time.Time
	HistoryDays int // FORECAST_HISTORY_DAYS (default 56)
	LeadDays    int // FORECAST_LEAD_DAYS (default 7)
	CoverDays   int // FORECAST_COVER_DAYS (default 7)
}

// RunResult reports a forecast run.
type RunResult struct {
	Series   int    `json:"series"`
	Upserted int    `json:"upserted"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type seriesKey struct{ tenant, store, item string }

type seriesAcc struct {
	itemID *string
	name   string
	qty    map[string]float64
}

// Run refits every (tenant, store, item) that sold in the history window.
// Pure Go, no LLM. Safe to re-run: results are upserts.
func (f *Forecaster) Run(ctx context.Context) (*RunResult, error) {
	today := f.Now().In(IST)
	end := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1) // yesterday (complete day)
	start := end.AddDate(0, 0, -(f.HistoryDays - 1))
	res := &RunResult{From: start.Format(time.DateOnly), To: end.Format(time.DateOnly)}

	rows, err := f.DB.Query(ctx, `SELECT tenant_id::text, store_id::text, item_key, item_id::text, name, day, qty::float8
		FROM item_sales_daily WHERE day BETWEEN $1 AND $2`, start, end)
	if err != nil {
		return nil, err
	}
	acc := map[seriesKey]*seriesAcc{}
	for rows.Next() {
		var k seriesKey
		var itemID *string
		var name string
		var day time.Time
		var qty float64
		if err := rows.Scan(&k.tenant, &k.store, &k.item, &itemID, &name, &day, &qty); err != nil {
			rows.Close()
			return nil, err
		}
		a := acc[k]
		if a == nil {
			a = &seriesAcc{itemID: itemID, name: name, qty: map[string]float64{}}
			acc[k] = a
		}
		a.qty[day.Format(time.DateOnly)] += qty
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.Series = len(acc)

	batch := &pgx.Batch{}
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		err := f.DB.SendBatch(ctx, batch).Close()
		batch = &pgx.Batch{}
		return err
	}
	next := end.AddDate(0, 0, 1)
	for k, a := range acc {
		daily := make([]float64, f.HistoryDays)
		for i := range daily {
			daily[i] = math.Max(0, a.qty[start.AddDate(0, 0, i).Format(time.DateOnly)])
		}
		m := forecast.Fit(daily, start, forecast.DefaultAlpha)
		batch.Queue(`INSERT INTO forecasts (tenant_id, store_id, item_key, item_id, name, daily_level, dow_factors, next_7d, history_days, computed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
			ON CONFLICT (tenant_id, store_id, item_key) DO UPDATE SET item_id=$4, name=$5, daily_level=$6,
				dow_factors=$7, next_7d=$8, history_days=$9, computed_at=now()`,
			k.tenant, k.store, k.item, a.itemID, a.name, m.Level, m.DOW[:], m.Demand(next, 7), m.HistoryDays)
		res.Upserted++
		if batch.Len() >= 500 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	f.Log.Info("forecast run", "series", res.Series, "from", res.From, "to", res.To)
	return res, nil
}

// ReorderLine is one suggestion in GET /v1/forecast/reorder.
type ReorderLine struct {
	ItemID          string  `json:"item_id"`
	Name            string  `json:"name"`
	OnHand          float64 `json:"on_hand"`
	DailyDemand     float64 `json:"forecast_daily_demand"`
	DemandLeadTime  float64 `json:"forecast_demand_over_lead_time"`
	SuggestedQty    float64 `json:"suggested_qty"`
	Reason          string  `json:"reason"` // forecast | below_reorder_level
	PreferredSupply string  `json:"preferred_supplier_id,omitempty"`
}

// ReorderReport is the response.
type ReorderReport struct {
	StoreID   string        `json:"store_id"`
	LeadDays  int           `json:"lead_days"`
	CoverDays int           `json:"cover_days"`
	Items     []ReorderLine `json:"items"`
	Warnings  []string      `json:"warnings"`
}

// Reorder lists items whose forecast demand over the lead time exceeds stock,
// plus items bhandar flagged as below their re-order level.
func (f *Forecaster) Reorder(ctx context.Context, tenant, store string, leadDays int) (*ReorderReport, error) {
	if leadDays <= 0 {
		leadDays = f.LeadDays
	}
	rep := &ReorderReport{StoreID: store, LeadDays: leadDays, CoverDays: f.CoverDays, Items: []ReorderLine{}, Warnings: []string{}}

	onHand := map[string]float64{}
	stockOK := true
	if items, err := f.Clients.StockSummary(ctx, store); err != nil {
		stockOK = false
		rep.Warnings = append(rep.Warnings, "Live stock unavailable from bhandar; using last low-stock alerts only.")
	} else {
		for _, it := range items {
			onHand[it.ItemID] = it.OnHand
		}
	}

	rows, err := f.DB.Query(ctx, `SELECT item_id::text, name, daily_level, dow_factors FROM forecasts
		WHERE tenant_id=$1 AND store_id::text=$2 AND item_id IS NOT NULL AND daily_level > 0`, tenant, store)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	now := f.Now().In(IST)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for rows.Next() {
		var id, name string
		var level float64
		var dow []float64
		if err := rows.Scan(&id, &name, &level, &dow); err != nil {
			rows.Close()
			return nil, err
		}
		have, ok := onHand[id]
		if !stockOK || !ok {
			continue // not a stocked item (or no live stock): can't judge
		}
		m := forecast.Model{Level: level}
		copy(m.DOW[:], dow)
		s := forecast.Reorder(m, from, have, leadDays, f.CoverDays)
		if !s.Needed {
			continue
		}
		seen[id] = true
		rep.Items = append(rep.Items, ReorderLine{ItemID: id, Name: name, OnHand: have, DailyDemand: round3(level),
			DemandLeadTime: s.DemandLead, SuggestedQty: s.SuggestedQty, Reason: "forecast"})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	arows, err := f.DB.Query(ctx, `SELECT item_id::text, item_name, on_hand::float8, reorder_qty::float8, COALESCE(supplier_id,'')
		FROM stock_alerts WHERE tenant_id=$1 AND store_id::text=$2 AND alerted_at > now() - interval '14 days'`, tenant, store)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var l ReorderLine
		var qty float64
		if err := arows.Scan(&l.ItemID, &l.Name, &l.OnHand, &qty, &l.PreferredSupply); err != nil {
			return nil, err
		}
		if seen[l.ItemID] {
			continue
		}
		if cur, ok := onHand[l.ItemID]; ok {
			l.OnHand = cur
		}
		l.SuggestedQty, l.Reason = qty, "below_reorder_level"
		rep.Items = append(rep.Items, l)
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(rep.Items, func(i, j int) bool { return rep.Items[i].SuggestedQty > rep.Items[j].SuggestedQty })
	return rep, nil
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
