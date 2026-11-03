package posting

import (
	"fmt"
	"strings"

	"github.com/maapan/mool/money"
)

// TDS / TCS sections bahi computes. Rates are basis points of the base
// (10 bp = 0.1 %). Thresholds are per party per financial year.
//
// The codes are the Income-tax Act 1961 section numbers that accountants,
// TRACES and the RPU still key on; the Income-tax Act 2025 (in force from
// 1 April 2026) renumbers the provisions but keeps these rates and limits.
const (
	Sec194Q   = "194Q"     // purchase of goods: 0.1 % above ₹50 lakh per seller per FY
	Sec194C   = "194C"     // contractors: 1 % individual/HUF, 2 % others; ₹30,000 single or ₹1,00,000 aggregate
	Sec194JA  = "194J(a)"  // fees for technical services: 2 %
	Sec194JB  = "194J(b)"  // professional fees: 10 %
	Sec194J   = "194J"     // alias of 194J(b)
	Sec206C1H = "206C(1H)" // TCS on sale of goods: 0.1 % above ₹50 lakh per buyer per FY
)

// Section describes one TDS/TCS section.
type Section struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`               // tds | tcs
	RateBP      int    `json:"rate_bp"`            // with PAN, deductee other than individual/HUF
	RateIndivBP int    `json:"rate_individual_bp"` // individual / HUF
	NoPANRateBP int    `json:"no_pan_rate_bp"`     // Sec 206AA / 206CC
	Single      int64  `json:"single_threshold_paise,omitempty"`
	Aggregate   int64  `json:"aggregate_threshold_paise,omitempty"`
	ExcessOnly  bool   `json:"excess_only"` // deduct only on the part of the FY aggregate above Aggregate
	Code26Q     string `json:"code_26q"`    // section code in the 26Q / 27EQ deductee annexure
	ValidUntil  string `json:"valid_until,omitempty"`
}

// Sections lists the supported sections by code.
var Sections = map[string]Section{
	Sec194Q: {Code: Sec194Q, Name: "Purchase of goods", Kind: "tds", RateBP: 10, RateIndivBP: 10, NoPANRateBP: 500,
		Aggregate: 50_00_000_00, ExcessOnly: true, Code26Q: "94Q"},
	Sec194C: {Code: Sec194C, Name: "Payment to contractors", Kind: "tds", RateBP: 200, RateIndivBP: 100, NoPANRateBP: 2000,
		Single: 30_000_00, Aggregate: 1_00_000_00, Code26Q: "94C"},
	Sec194JA: {Code: Sec194JA, Name: "Fees for technical services", Kind: "tds", RateBP: 200, RateIndivBP: 200, NoPANRateBP: 2000,
		Aggregate: 50_000_00, Code26Q: "4JA"},
	Sec194JB: {Code: Sec194JB, Name: "Fees for professional services", Kind: "tds", RateBP: 1000, RateIndivBP: 1000, NoPANRateBP: 2000,
		Aggregate: 50_000_00, Code26Q: "4JB"},
	// The Finance Act 2025 omitted 206C(1H) from 1 April 2025. bahi still
	// tracks the threshold and books any TCS an invoice carries, but no longer
	// proposes a collection after that date.
	Sec206C1H: {Code: Sec206C1H, Name: "TCS on sale of goods", Kind: "tcs", RateBP: 10, RateIndivBP: 10, NoPANRateBP: 100,
		Aggregate: 50_00_000_00, ExcessOnly: true, Code26Q: "6CR", ValidUntil: "2025-03-31"},
}

// LookupSection resolves a code case-insensitively; "194J" means 194J(b).
func LookupSection(code string) (Section, bool) {
	c := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	if c == Sec194J {
		c = strings.ToUpper(Sec194JB)
	}
	for k, s := range Sections {
		if strings.ToUpper(k) == c {
			return s, true
		}
	}
	return Section{}, false
}

// ValidPAN checks the AAAAA9999A shape of a PAN.
func ValidPAN(pan string) bool {
	if len(pan) != 10 {
		return false
	}
	for i := 0; i < 10; i++ {
		c := pan[i]
		letter := c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if i >= 5 && i < 9 {
			if !digit {
				return false
			}
		} else if !letter {
			return false
		}
	}
	return true
}

// DeducteeType derives individual / huf / company / firm / other from the 4th
// character of a PAN.
func DeducteeType(pan string) string {
	if !ValidPAN(pan) {
		return "other"
	}
	switch pan[3] {
	case 'P':
		return "individual"
	case 'H':
		return "huf"
	case 'C':
		return "company"
	case 'F', 'L':
		return "firm"
	}
	return "other"
}

// TDSInput is one bill or payment tested against a section.
type TDSInput struct {
	Section      Section
	PAN          string // "" or invalid → higher rate (206AA / 206CC)
	DeducteeType string // individual | huf | company | firm | other ("" = from PAN)
	AmountPaise  int64  // this bill/payment (excluding GST where shown separately)
	PriorPaise   int64  // FY aggregate already recorded under this section for the party
	PriorTaxed   int64  // part of PriorPaise TDS was already computed on
	Date         string // YYYY-MM-DD
	LowerRateBP  *int   // lower-deduction certificate (Sec 197), optional
}

// TDSResult is what to deduct.
type TDSResult struct {
	Section        string `json:"section"`
	Applicable     bool   `json:"applicable"`
	BasePaise      int64  `json:"base_paise"`
	TaxablePaise   int64  `json:"taxable_paise"` // what the rate is applied to (incl. catch-up)
	RateBP         int    `json:"rate_bp"`
	TDSPaise       int64  `json:"tds_paise"` // rounded to the nearest rupee (Sec 288B)
	HigherRate     bool   `json:"higher_rate_no_pan"`
	AggregatePaise int64  `json:"aggregate_paise"` // FY aggregate including this transaction
	Reason         string `json:"reason"`
}

// ComputeTDS applies a section's threshold and rate.
//
//   - ExcessOnly (194Q, 206C(1H)): only the part of the FY aggregate above the
//     threshold is taxed.
//   - Otherwise (194C, 194J): nothing is deducted while the amount is within
//     the single-payment limit and the aggregate within the aggregate limit.
//     Once the aggregate is crossed the whole amount is taxed, and the
//     transaction that first crosses it also catches up the year's earlier
//     untaxed amounts.
func ComputeTDS(in TDSInput) TDSResult {
	s := in.Section
	r := TDSResult{Section: s.Code, BasePaise: in.AmountPaise, AggregatePaise: in.PriorPaise + in.AmountPaise}
	if in.AmountPaise <= 0 {
		r.Reason = "nothing to deduct"
		return r
	}
	if s.ValidUntil != "" && in.Date > s.ValidUntil {
		r.Reason = fmt.Sprintf("section %s does not apply after %s", s.Code, s.ValidUntil)
		return r
	}
	agg := r.AggregatePaise
	switch {
	case s.ExcessOnly:
		r.TaxablePaise = max(agg-s.Aggregate, 0) - max(in.PriorPaise-s.Aggregate, 0)
	case s.Aggregate > 0 && agg > s.Aggregate:
		r.TaxablePaise = agg - in.PriorTaxed
	case s.Single > 0 && in.AmountPaise > s.Single:
		r.TaxablePaise = in.AmountPaise
	}
	if r.TaxablePaise <= 0 {
		r.TaxablePaise = 0
		r.Reason = "within threshold"
		return r
	}
	dt := in.DeducteeType
	if dt == "" {
		dt = DeducteeType(in.PAN)
	}
	rate := s.RateBP
	if dt == "individual" || dt == "huf" {
		rate = s.RateIndivBP
	}
	if in.LowerRateBP != nil && *in.LowerRateBP >= 0 {
		rate = *in.LowerRateBP
	}
	if !ValidPAN(in.PAN) {
		rate, r.HigherRate = max(rate, s.NoPANRateBP), true
	}
	r.RateBP = rate
	r.TDSPaise = money.MulDiv(money.ApplyBP(r.TaxablePaise, rate), 1, 100) * 100
	if r.TDSPaise == 0 {
		r.Reason = "rounds to zero"
		return r
	}
	r.Applicable = true
	r.Reason = "threshold crossed"
	if r.HigherRate {
		r.Reason += "; PAN missing or invalid, higher rate"
	}
	return r
}

// DeducteeTypeOr returns t when set, else the type derived from the PAN.
func DeducteeTypeOr(t, pan string) string {
	if t != "" {
		return t
	}
	return DeducteeType(pan)
}
