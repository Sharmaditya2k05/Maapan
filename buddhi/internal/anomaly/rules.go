// Package anomaly holds the pure detection rules: discount outliers per cashier
// (3-sigma over a running Welford estimate), cancellation-rate outliers, GSTIN
// gaps on B2B/purchase documents and HSN rate mismatches.
package anomaly

import (
	"math"
	"sort"
)

// Stats is a running mean/variance (Welford).
type Stats struct {
	N    int64
	Mean float64
	M2   float64
}

// Update adds x.
func (s Stats) Update(x float64) Stats {
	s.N++
	d := x - s.Mean
	s.Mean += d / float64(s.N)
	s.M2 += d * (x - s.Mean)
	return s
}

// StdDev is the sample standard deviation.
func (s Stats) StdDev() float64 {
	if s.N < 2 {
		return 0
	}
	return math.Sqrt(s.M2 / float64(s.N-1))
}

// MinHistory is how many bills a cashier needs before discounts are judged.
const MinHistory = 20

// IsDiscountOutlier reports whether pct is above mean+3σ of the cashier's
// history. Tiny discounts (< 2%) are never flagged, and with zero variance any
// discount over 5 points above the mean counts.
func IsDiscountOutlier(s Stats, pct float64) bool {
	if s.N < MinHistory || pct < 2 {
		return false
	}
	sd := s.StdDev()
	if sd == 0 {
		return pct > s.Mean+5
	}
	return pct > s.Mean+3*sd
}

// Line is the part of an invoice line the discount estimate needs.
type Line struct {
	Qty              float64
	UnitPricePaise   int64
	TaxablePaise     int64
	TotalPaise       int64
	DiscountPaise    *int64 // present once aadesh publishes it
	PriceIncludesTax *bool
}

// Discount returns (discount, gross) in paise for an invoice. When the event
// carries discount_paise it is exact; otherwise it is estimated from list value
// vs billed value, treating the line as tax-inclusive if its total is at or below
// list (retail MRP pricing) and tax-exclusive otherwise.
func Discount(lines []Line) (discount, gross int64) {
	for _, l := range lines {
		g := int64(math.Round(l.Qty * float64(l.UnitPricePaise)))
		gross += g
		switch {
		case l.DiscountPaise != nil:
			discount += *l.DiscountPaise
		case l.PriceIncludesTax != nil && *l.PriceIncludesTax:
			discount += max(0, g-l.TotalPaise)
		case l.PriceIncludesTax != nil:
			discount += max(0, g-l.TaxablePaise)
		case l.TotalPaise <= g:
			discount += g - l.TotalPaise
		default:
			discount += max(0, g-l.TaxablePaise)
		}
	}
	return discount, gross
}

// Pct returns discount as a percentage of gross.
func Pct(discount, gross int64) float64 {
	if gross <= 0 {
		return 0
	}
	return 100 * float64(discount) / float64(gross)
}

// CashierCancels is the cancellation tally for one cashier over a window.
type CashierCancels struct {
	CashierID string  `json:"cashier_id"`
	Bills     int     `json:"bills"`
	Cancelled int     `json:"cancelled"`
	Rate      float64 `json:"rate_pct"`
	TenantAvg float64 `json:"tenant_avg_pct"`
}

// HighCancellations flags cashiers with at least 3 cancellations whose rate is
// above both 5% and 3x the tenant-wide rate.
func HighCancellations(rows []CashierCancels) []CashierCancels {
	var bills, canc int
	for _, r := range rows {
		bills += r.Bills
		canc += r.Cancelled
	}
	if bills == 0 {
		return nil
	}
	avg := 100 * float64(canc) / float64(bills)
	var out []CashierCancels
	for _, r := range rows {
		if r.Bills == 0 || r.Cancelled < 3 {
			continue
		}
		r.Rate = 100 * float64(r.Cancelled) / float64(r.Bills)
		r.TenantAvg = math.Round(avg*100) / 100
		if r.Rate > 5 && r.Rate > 3*avg {
			r.Rate = math.Round(r.Rate*100) / 100
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
	return out
}

// B2BThresholdPaise: sales to a saved party at or above this without a GSTIN
// are flagged as "possibly B2B, GSTIN missing" (₹50,000).
const B2BThresholdPaise = 50_000_00

// GSTINCheck classifies a document's GSTIN situation. It returns "" when fine,
// "missing_gstin" or "invalid_gstin".
func GSTINCheck(docType, partyID, gstin string, grandPaise int64, valid func(string) error) string {
	if gstin != "" {
		if valid(gstin) != nil {
			return "invalid_gstin"
		}
		return ""
	}
	switch {
	case docType == "purchase" && partyID != "":
		return "missing_gstin" // input tax credit can't be claimed without the supplier's GSTIN
	case docType == "sale" && partyID != "" && grandPaise >= B2BThresholdPaise:
		return "missing_gstin"
	}
	return ""
}
