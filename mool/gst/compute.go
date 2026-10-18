package gst

import (
	"errors"
	"math"
	"sort"

	"github.com/taraju/mool/money"
)

// StandardRatesBP are the GST slabs in basis points.
var StandardRatesBP = []int{0, 25, 300, 500, 1200, 1800, 2800, 4000}

// Line is one invoice line as entered at the counter.
type Line struct {
	HSN              string  `json:"hsn"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"` // flat discount on the whole line
	TaxRateBP        int     `json:"tax_rate_bp"`
	CessRateBP       int     `json:"cess_rate_bp"`
	PriceIncludesTax bool    `json:"price_includes_tax"` // MRP-style pricing common at retail POS
}

// LineTax is the computed result for one line.
type LineTax struct {
	GrossPaise    int64 `json:"gross_paise"`
	DiscountPaise int64 `json:"discount_paise"`
	TaxablePaise  int64 `json:"taxable_paise"`
	CGSTPaise     int64 `json:"cgst_paise"`
	SGSTPaise     int64 `json:"sgst_paise"`
	IGSTPaise     int64 `json:"igst_paise"`
	CessPaise     int64 `json:"cess_paise"`
	TotalPaise    int64 `json:"total_paise"`
}

// Summary is the invoice-level result.
type Summary struct {
	InterState    bool        `json:"inter_state"`
	Lines         []LineTax   `json:"lines"`
	TaxablePaise  int64       `json:"taxable_paise"`
	CGSTPaise     int64       `json:"cgst_paise"`
	SGSTPaise     int64       `json:"sgst_paise"`
	IGSTPaise     int64       `json:"igst_paise"`
	CessPaise     int64       `json:"cess_paise"`
	DiscountPaise int64       `json:"discount_paise"`
	RoundOffPaise int64       `json:"round_off_paise"`
	GrandPaise    int64       `json:"grand_total_paise"`
	ByRate        []RateTotal `json:"by_rate"`
}

// RateTotal groups taxable value and tax by GST slab (printed on invoices, used by GSTR-1).
type RateTotal struct {
	TaxRateBP    int   `json:"tax_rate_bp"`
	TaxablePaise int64 `json:"taxable_paise"`
	TaxPaise     int64 `json:"tax_paise"`
}

// IsInterState reports whether supply from storeState to placeOfSupply is inter-state.
func IsInterState(storeState, placeOfSupply string) bool {
	return placeOfSupply != "" && storeState != placeOfSupply
}

// Compute calculates taxes for an invoice. storeState is the supplier's state
// code; placeOfSupply is the buyer's state code (the store's own state for walk-in B2C).
func Compute(lines []Line, storeState, placeOfSupply string, roundToRupee bool) (Summary, error) {
	if !ValidState(storeState) {
		return Summary{}, errors.New("invalid store state code")
	}
	if placeOfSupply == "" {
		placeOfSupply = storeState
	}
	if !ValidState(placeOfSupply) {
		return Summary{}, errors.New("invalid place of supply")
	}
	s := Summary{InterState: IsInterState(storeState, placeOfSupply)}
	byRate := map[int]*RateTotal{}
	for i, l := range lines {
		lt, err := computeLine(l, s.InterState)
		if err != nil {
			return Summary{}, errors.Join(errors.New("line "+itoa(i+1)), err)
		}
		s.Lines = append(s.Lines, lt)
		s.TaxablePaise += lt.TaxablePaise
		s.CGSTPaise += lt.CGSTPaise
		s.SGSTPaise += lt.SGSTPaise
		s.IGSTPaise += lt.IGSTPaise
		s.CessPaise += lt.CessPaise
		s.DiscountPaise += lt.DiscountPaise
		rt := byRate[l.TaxRateBP]
		if rt == nil {
			rt = &RateTotal{TaxRateBP: l.TaxRateBP}
			byRate[l.TaxRateBP] = rt
		}
		rt.TaxablePaise += lt.TaxablePaise
		rt.TaxPaise += lt.CGSTPaise + lt.SGSTPaise + lt.IGSTPaise
	}
	total := s.TaxablePaise + s.CGSTPaise + s.SGSTPaise + s.IGSTPaise + s.CessPaise
	s.GrandPaise = total
	if roundToRupee {
		s.GrandPaise, s.RoundOffPaise = money.RoundToRupee(total)
	}
	for _, rt := range byRate {
		s.ByRate = append(s.ByRate, *rt)
	}
	sort.Slice(s.ByRate, func(i, j int) bool { return s.ByRate[i].TaxRateBP < s.ByRate[j].TaxRateBP })
	return s, nil
}

func computeLine(l Line, inter bool) (LineTax, error) {
	switch {
	case l.Qty <= 0:
		return LineTax{}, errors.New("quantity must be positive")
	case l.UnitPricePaise < 0 || l.DiscountPaise < 0:
		return LineTax{}, errors.New("price and discount cannot be negative")
	case l.TaxRateBP < 0 || l.TaxRateBP > 10000 || l.CessRateBP < 0:
		return LineTax{}, errors.New("invalid tax rate")
	}
	gross := int64(math.Round(l.Qty * float64(l.UnitPricePaise)))
	if l.DiscountPaise > gross {
		return LineTax{}, errors.New("discount exceeds line value")
	}
	net := gross - l.DiscountPaise
	taxable := net
	if l.PriceIncludesTax {
		// net = taxable * (1 + (rate+cess)/10000)  →  taxable = net*10000/(10000+rate+cess)
		taxable = money.MulDiv(net, 10000, int64(10000+l.TaxRateBP+l.CessRateBP))
	}
	lt := LineTax{GrossPaise: gross, DiscountPaise: l.DiscountPaise, TaxablePaise: taxable}
	if inter {
		lt.IGSTPaise = money.ApplyBP(taxable, l.TaxRateBP)
	} else {
		// CGST and SGST are each levied at half the rate and rounded independently.
		half := money.MulDiv(taxable, int64(l.TaxRateBP), 20000)
		lt.CGSTPaise, lt.SGSTPaise = half, half
	}
	lt.CessPaise = money.ApplyBP(taxable, l.CessRateBP)
	lt.TotalPaise = taxable + lt.CGSTPaise + lt.SGSTPaise + lt.IGSTPaise + lt.CessPaise
	if l.PriceIncludesTax && lt.TotalPaise != net {
		// Absorb the 1-paisa rounding gap into taxable value so the customer pays exactly the shelf price.
		diff := net - lt.TotalPaise
		lt.TaxablePaise += diff
		lt.TotalPaise = net
	}
	return lt, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
