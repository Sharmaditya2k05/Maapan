package anomaly

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Anomaly is a stored finding.
type Anomaly struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Severity   string          `json:"severity"`
	StoreID    *string         `json:"store_id,omitempty"`
	Subject    string          `json:"subject"`
	Message    string          `json:"message"`
	Details    json.RawMessage `json:"details"`
	Resolved   bool            `json:"resolved"`
	DetectedAt time.Time       `json:"detected_at"`
}

// Report is GET /v1/insights/anomalies.
type Report struct {
	Since         string           `json:"since"`
	Anomalies     []Anomaly        `json:"anomalies"`
	Cancellations []CashierCancels `json:"high_cancellation_cashiers"`
}

// List returns findings since `since`, plus cashiers with unusual cancellation
// rates (computed live from invoice_facts). store "" = all stores.
func List(ctx context.Context, pool *pgxpool.Pool, tenant, store, kind string, since time.Time, includeResolved bool) (*Report, error) {
	r := &Report{Since: since.Format(time.DateOnly), Anomalies: []Anomaly{}, Cancellations: []CashierCancels{}}
	rows, err := pool.Query(ctx, `SELECT id, kind, severity, store_id, subject, message, details, resolved, detected_at
		FROM anomalies WHERE tenant_id=$1 AND detected_at >= $2
		  AND ($3 = '' OR store_id = $3 OR store_id IS NULL) AND ($4 = '' OR kind = $4) AND ($5 OR NOT resolved)
		ORDER BY detected_at DESC LIMIT 200`, tenant, since, store, kind, includeResolved)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Anomaly
		if err := rows.Scan(&a.ID, &a.Kind, &a.Severity, &a.StoreID, &a.Subject, &a.Message, &a.Details, &a.Resolved, &a.DetectedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.Anomalies = append(r.Anomalies, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if kind != "" && kind != "high_cancellations" {
		return r, nil
	}

	crow, err := pool.Query(ctx, `SELECT created_by, count(*)::int, count(*) FILTER (WHERE cancelled)::int
		FROM invoice_facts WHERE tenant_id=$1 AND doc_type='sale' AND day >= $2 AND ($3 = '' OR store_id::text = $3)
		GROUP BY created_by`, tenant, since, store)
	if err != nil {
		return nil, err
	}
	defer crow.Close()
	var all []CashierCancels
	for crow.Next() {
		var c CashierCancels
		if err := crow.Scan(&c.CashierID, &c.Bills, &c.Cancelled); err != nil {
			return nil, err
		}
		all = append(all, c)
	}
	if err := crow.Err(); err != nil {
		return nil, err
	}
	if flagged := HighCancellations(all); flagged != nil {
		r.Cancellations = flagged
	}
	return r, nil
}

// Resolve marks a finding handled.
func Resolve(ctx context.Context, pool *pgxpool.Pool, tenant string, id int64) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE anomalies SET resolved=true WHERE tenant_id=$1 AND id=$2`, tenant, id)
	return tag.RowsAffected() == 1, err
}
