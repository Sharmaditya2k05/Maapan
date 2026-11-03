// Package numbering issues gap-free document numbers per tenant, series and
// Indian financial year: PR/2026-27/00001 (production runs), JW/… (job-work
// challans), JWR/… (job-work receipts). Every number is at most 16 characters,
// the limit Rule 46/55 of the CGST Rules puts on a challan serial number.
package numbering

import (
	"context"
	"fmt"
	"time"

	"github.com/maapan/mool/db"
)

// Series prefixes.
const (
	ProductionRun   = "PR"
	JobWorkChallan  = "JW"
	JobWorkReceipt  = "JWR"
	maxNumberLength = 16
)

// FY is the Indian financial year (April–March) of d, e.g. "2026-27".
func FY(d time.Time) string {
	y := d.Year()
	if d.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

// Format renders prefix/fy/n, zero-padding n so the result fits 16 characters.
func Format(prefix, fy string, n int) string {
	width := maxNumberLength - len(prefix) - len(fy) - 2
	if width < 1 {
		width = 1
	}
	return fmt.Sprintf("%s/%s/%0*d", prefix, fy, width, n)
}

// Next reserves the next number of series for the financial year containing
// date. Call it inside the transaction that saves the document so a rollback
// returns the number (no gaps).
func Next(ctx context.Context, q db.Querier, tenantID, series string, date time.Time) (string, error) {
	fy := FY(date)
	var n int
	err := q.QueryRow(ctx, `INSERT INTO number_series (tenant_id, series, fy, last_no) VALUES ($1,$2,$3,1)
		ON CONFLICT (tenant_id, series, fy) DO UPDATE SET last_no = number_series.last_no + 1
		RETURNING last_no`, tenantID, series, fy).Scan(&n)
	if err != nil {
		return "", err
	}
	return Format(series, fy, n), nil
}
