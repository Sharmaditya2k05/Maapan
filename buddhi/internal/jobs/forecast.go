// Package jobs holds buddhi's scheduled work (triggered by Cloud Scheduler) and
// the re-order report built from its output.
package jobs

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/forecast"
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
