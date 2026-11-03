package gstinsight

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Save caches the latest insight for (tenant, gstin, period); the dashboard
// card reads it so loading the dashboard never calls kar.
func Save(ctx context.Context, q db.Querier, tenantID string, in Insight) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO gst_insights (tenant_id, gstin, period, summary, computed_at) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, gstin, period) DO UPDATE SET summary=$4, computed_at=$5`,
		tenantID, in.GSTIN, in.Period, b, in.ComputedAt)
	return err
}

// Latest returns the most recently computed insight (nil if none).
func Latest(ctx context.Context, q db.Querier, tenantID string) (*Insight, error) {
	var b []byte
	err := q.QueryRow(ctx, `SELECT summary FROM gst_insights WHERE tenant_id=$1 ORDER BY computed_at DESC LIMIT 1`, tenantID).Scan(&b)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in Insight
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// Source fetches kar's reconciliation (clients.Clients.GSTR2B).
type Source func(ctx context.Context, gstin, period string) (json.RawMessage, error)

// Refresh pulls kar's 2B matching for gstin/period, summarises and caches it.
func Refresh(ctx context.Context, q db.Querier, src Source, tenantID, gstin, period string, now time.Time) (*Insight, error) {
	raw, err := src(ctx, gstin, period)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && (he.Status == http.StatusUnprocessableEntity || he.Status == http.StatusBadRequest || he.Status == http.StatusForbidden) {
			return nil, he // kar's own validation (unknown GSTIN, bad period) is the caller's to fix
		}
		return nil, httpx.Unavailable("GSTR-2B matching is unavailable from kar right now (%s)", err.Error())
	}
	var rec Reconciliation
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, httpx.Upstream("kar returned an unexpected GSTR-2B shape")
	}
	if rec.Period == "" {
		rec.Period = period
	}
	in := Summarise(rec, now)
	if in.GSTIN != "" {
		if err := Save(ctx, q, tenantID, in); err != nil {
			return nil, err
		}
	}
	return &in, nil
}
