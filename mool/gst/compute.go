package gst

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/maapan/mool/money"
)

// Tax categories of a line (Line.TaxCategory). Empty means taxable.
const (
	CatTaxable  = "taxable"
	CatExempt   = "exempt"    // exempt by notification (Sec 11) — GSTR-1 Table 8, GSTR-3B 3.1(c)
	CatNilRated = "nil_rated" // 0 % schedule entry — Table 8, 3.1(c)
	CatNonGST   = "non_gst"   // outside GST (petrol, liquor, electricity) — Table 8, 3.1(e)
)

// Supply categories of an invoice (Options.SupplyCategory). Empty means a
// regular domestic supply taxed per line.
const (
	SupplyRegular              = ""
	SupplySEZWithPayment       = "sez_with_payment"       // zero-rated; IGST charged, refundable
	SupplySEZWithoutPayment    = "sez_without_payment"    // zero-rated under LUT/bond; no tax
	SupplyExportWithPayment    = "export_with_payment"    // zero-rated; IGST charged, refundable
	SupplyExportWithoutPayment = "export_without_payment" // zero-rated under LUT/bond; no tax
	SupplyDeemedExport         = "deemed_export"          // taxed like a domestic B2B supply
)

// Line is one invoice line as entered at the counter.
type Line struct {
	HSN              string  `json:"hsn"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"`                // flat discount on the whole line
	DiscountBP       int     `json:"discount_bp,omitempty"`         // percentage discount on gross (1000 = 10 %), applied before the flat one
	TaxRateBP        int     `json:"tax_rate_bp"`                   // GST rate; split CGST+SGST or charged as IGST
	CessRateBP       int     `json:"cess_rate_bp"`                  // ad-valorem compensation cess
	CessPerUnitPaise int64   `json:"cess_per_unit_paise,omitempty"` // specific cess per unit (tobacco, aerated water, coal)
	PriceIncludesTax bool    `json:"price_includes_tax"`            // MRP-style pricing common at retail POS
	TaxCategory      string  `json:"tax_category,omitempty"`        // "", taxable, exempt, nil_rated, non_gst
}

// LineTax is the computed result for one line.
type LineTax struct {
	GrossPaise    int64  `json:"gross_paise"`
	DiscountPaise int64  `json:"discount_paise"` // line discounts plus this line's share of the invoice discount
	TaxablePaise  int64  `json:"taxable_paise"`  // value of the line net of discounts (and of tax when inclusive)
	CGSTPaise     int64  `json:"cgst_paise"`
	SGSTPaise     int64  `json:"sgst_paise"`
	IGSTPaise     int64  `json:"igst_paise"`
	CessPaise     int64  `json:"cess_paise"`
	TotalPaise    int64  `json:"total_paise"`
	TaxCategory   string `json:"tax_category"` // taxable, exempt, nil_rated, non_gst
	RateBP        int    `json:"rate_bp"`      // GST rate actually applied (0 when zero-rated, composition or non-taxable)
}

// Summary is the invoice-level result.
type Summary struct {
	InterState     bool   `json:"inter_state"`
	SupplyCategory string `json:"supply_category,omitempty"`
	ReverseCharge  bool   `json:"reverse_charge,omitempty"` // tax is payable by the recipient; GrandPaise excludes it
	BillOfSupply   bool   `json:"bill_of_supply,omitempty"` // composition supplier: no tax may be charged

	Lines []LineTax `json:"lines"`

	TaxablePaise         int64 `json:"taxable_paise"` // value of every line net of discounts (the buckets below are subsets)
	ExemptPaise          int64 `json:"exempt_paise,omitempty"`
	NilRatedPaise        int64 `json:"nil_rated_paise,omitempty"`
	NonGSTPaise          int64 `json:"non_gst_paise,omitempty"`
	ZeroRatedPaise       int64 `json:"zero_rated_paise,omitempty"` // SEZ / export lines
	CGSTPaise            int64 `json:"cgst_paise"`
	SGSTPaise            int64 `json:"sgst_paise"`
	IGSTPaise            int64 `json:"igst_paise"`
	CessPaise            int64 `json:"cess_paise"`
	TaxPaise             int64 `json:"tax_paise"`      // CGST + SGST + IGST + cess
	DiscountPaise        int64 `json:"discount_paise"` // every discount, line and invoice level
	InvoiceDiscountPaise int64 `json:"invoice_discount_paise,omitempty"`
	TCSPaise             int64 `json:"tcs_paise,omitempty"` // income-tax TCS (206C(1H)) collected on the invoice value
	RoundOffPaise        int64 `json:"round_off_paise"`
	GrandPaise           int64 `json:"grand_total_paise"` // what the recipient pays the supplier

	ByRate []RateTotal `json:"by_rate"`
}

// RateTotal groups value and tax by GST slab and tax category (printed on
// invoices, used by GSTR-1 Table 12 and Table 8).
type RateTotal struct {
	TaxRateBP    int    `json:"tax_rate_bp"`
	TaxCategory  string `json:"tax_category"`
	TaxablePaise int64  `json:"taxable_paise"`
	TaxPaise     int64  `json:"tax_paise"` // CGST + SGST + IGST
	CessPaise    int64  `json:"cess_paise"`
}

// Options drive ComputeWith.
type Options struct {
	StoreState    string // supplier's state code
	PlaceOfSupply string // buyer's state code; empty = store state (walk-in B2C)
	RoundToRupee  bool   // round the grand total to the nearest rupee (round_off_paise)
	RoundTaxHeads bool   // Sec 170 style: round each tax head (CGST, SGST, IGST, cess) to the nearest rupee

	SupplyCategory string // SupplyRegular, SupplySEZ*, SupplyExport*, SupplyDeemedExport
	ReverseCharge  bool   // recipient pays the tax (Sec 9(3)/9(4)); invoice total excludes it
	Composition    bool   // supplier is a composition dealer: bill of supply, no tax charged

	InvoiceDiscountPaise int64 // flat invoice-level discount, allocated pro-rata across lines by net value
	InvoiceDiscountBP    int   // percentage invoice-level discount on the net value of all lines

	TCSBP int // income-tax TCS (206C(1H)) rate on the invoice value including GST; 10 = 0.1 %

	On time.Time // document date; when set, every taxable line's rate must be a slab in force on that date
}

// IsInterState reports whether supply from storeState to placeOfSupply is inter-state.
func IsInterState(storeState, placeOfSupply string) bool {
	return placeOfSupply != "" && storeState != placeOfSupply
}

// Compute calculates taxes for a regular domestic invoice. storeState is the
// supplier's state code; placeOfSupply is the buyer's state code (the store's
// own state for walk-in B2C). It is ComputeWith with default options and is
// kept stable because the POS and dukaan's parity tests depend on it.
func Compute(lines []Line, storeState, placeOfSupply string, roundToRupee bool) (Summary, error) {
	return ComputeWith(lines, Options{StoreState: storeState, PlaceOfSupply: placeOfSupply, RoundToRupee: roundToRupee})
}

type mode struct {
	inter     bool
	zeroRated bool // SEZ/export without payment: no tax, lines keep their category
	noTax     bool // composition: bill of supply
	on        time.Time
}

// ComputeWith calculates taxes for an invoice under o.
func ComputeWith(lines []Line, o Options) (Summary, error) {
	if !ValidState(o.StoreState) {
		return Summary{}, errors.New("invalid store state code")
	}
	pos := o.PlaceOfSupply
	if pos == "" {
		pos = o.StoreState
	}
	if !ValidState(pos) {
		return Summary{}, errors.New("invalid place of supply")
	}
	if len(lines) == 0 {
		return Summary{}, errors.New("no lines")
	}
	m := mode{inter: IsInterState(o.StoreState, pos), on: o.On, noTax: o.Composition}
	s := Summary{SupplyCategory: o.SupplyCategory, ReverseCharge: o.ReverseCharge, BillOfSupply: o.Composition}
	switch o.SupplyCategory {
	case SupplyRegular, SupplyDeemedExport:
	case SupplySEZWithPayment, SupplyExportWithPayment:
		m.inter = true // IGST is charged on zero-rated supplies made with payment of tax
	case SupplySEZWithoutPayment, SupplyExportWithoutPayment:
		m.zeroRated = true
	default:
		return Summary{}, errors.New("unknown supply category " + o.SupplyCategory)
	}
	if o.Composition && o.ReverseCharge {
		return Summary{}, errors.New("a composition supplier cannot issue a reverse-charge invoice")
	}
	s.InterState = m.inter

	// Pass 1: gross, line discounts, net — needed to allocate the invoice discount.
	nets := make([]int64, len(lines))
	discs := make([]int64, len(lines))
	var netTotal int64
	for i, l := range lines {
		gross, disc, err := grossAndDiscount(l)
		if err != nil {
			return Summary{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		nets[i], discs[i] = gross-disc, disc
		netTotal += gross - disc
	}
	invDisc := o.InvoiceDiscountPaise + money.ApplyBP(netTotal, o.InvoiceDiscountBP)
	if invDisc < 0 {
		return Summary{}, errors.New("invoice discount cannot be negative")
	}
	if invDisc > netTotal {
		return Summary{}, errors.New("invoice discount exceeds invoice value")
	}
	shares := allocate(invDisc, nets)
	s.InvoiceDiscountPaise = invDisc

	// Pass 2: tax every line with its total discount.
	byRate := map[rateKey]*RateTotal{}
	for i, l := range lines {
		lt, err := computeLine(l, discs[i]+shares[i], m)
		if err != nil {
			return Summary{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		s.Lines = append(s.Lines, lt)
		s.TaxablePaise += lt.TaxablePaise
		s.CGSTPaise += lt.CGSTPaise
		s.SGSTPaise += lt.SGSTPaise
		s.IGSTPaise += lt.IGSTPaise
		s.CessPaise += lt.CessPaise
		s.DiscountPaise += lt.DiscountPaise
		switch {
		case lt.TaxCategory == CatExempt:
			s.ExemptPaise += lt.TaxablePaise
		case lt.TaxCategory == CatNilRated:
			s.NilRatedPaise += lt.TaxablePaise
		case lt.TaxCategory == CatNonGST:
			s.NonGSTPaise += lt.TaxablePaise
		case m.zeroRated || (o.SupplyCategory == SupplySEZWithPayment || o.SupplyCategory == SupplyExportWithPayment):
			s.ZeroRatedPaise += lt.TaxablePaise
		}
		k := rateKey{lt.RateBP, lt.TaxCategory}
		rt := byRate[k]
		if rt == nil {
			rt = &RateTotal{TaxRateBP: lt.RateBP, TaxCategory: lt.TaxCategory}
			byRate[k] = rt
		}
		rt.TaxablePaise += lt.TaxablePaise
		rt.TaxPaise += lt.CGSTPaise + lt.SGSTPaise + lt.IGSTPaise
		rt.CessPaise += lt.CessPaise
	}

	if o.RoundTaxHeads {
		s.CGSTPaise = roundHead(s.CGSTPaise)
		s.SGSTPaise = roundHead(s.SGSTPaise)
		s.IGSTPaise = roundHead(s.IGSTPaise)
		s.CessPaise = roundHead(s.CessPaise)
	}
	s.TaxPaise = s.CGSTPaise + s.SGSTPaise + s.IGSTPaise + s.CessPaise

	total := s.TaxablePaise
	if !o.ReverseCharge {
		total += s.TaxPaise
	}
	if o.TCSBP < 0 {
		return Summary{}, errors.New("tcs rate cannot be negative")
	}
	s.TCSPaise = money.ApplyBP(total, o.TCSBP)
	total += s.TCSPaise
	s.GrandPaise = total
	if o.RoundToRupee {
		s.GrandPaise, s.RoundOffPaise = money.RoundToRupee(total)
	}
	for _, rt := range byRate {
		s.ByRate = append(s.ByRate, *rt)
	}
	sort.Slice(s.ByRate, func(i, j int) bool {
		if s.ByRate[i].TaxRateBP != s.ByRate[j].TaxRateBP {
			return s.ByRate[i].TaxRateBP < s.ByRate[j].TaxRateBP
		}
		return s.ByRate[i].TaxCategory < s.ByRate[j].TaxCategory
	})
	return s, nil
}

type rateKey struct {
	bp  int
	cat string
}

func validCategory(c string) bool {
	switch c {
	case "", CatTaxable, CatExempt, CatNilRated, CatNonGST:
		return true
	}
	return false
}

// grossAndDiscount validates the line shape and returns gross and the line's own discount.
func grossAndDiscount(l Line) (gross, discount int64, err error) {
	switch {
	case l.Qty <= 0:
		return 0, 0, errors.New("quantity must be positive")
	case l.UnitPricePaise < 0 || l.DiscountPaise < 0 || l.DiscountBP < 0:
		return 0, 0, errors.New("price and discount cannot be negative")
	case l.DiscountBP > 10000:
		return 0, 0, errors.New("discount cannot exceed 100%")
	case l.TaxRateBP < 0 || l.TaxRateBP > 10000 || l.CessRateBP < 0 || l.CessPerUnitPaise < 0:
		return 0, 0, errors.New("invalid tax rate")
	case !validCategory(l.TaxCategory):
		return 0, 0, errors.New("invalid tax category " + l.TaxCategory)
	}
	gross = int64(math.Round(l.Qty * float64(l.UnitPricePaise)))
	discount = money.ApplyBP(gross, l.DiscountBP) + l.DiscountPaise
	if discount > gross {
		return 0, 0, errors.New("discount exceeds line value")
	}
	return gross, discount, nil
}

func computeLine(l Line, discount int64, m mode) (LineTax, error) {
	gross, _, err := grossAndDiscount(l)
	if err != nil {
		return LineTax{}, err
	}
	net := gross - discount
	if net < 0 {
		return LineTax{}, errors.New("discount exceeds line value")
	}
	cat := l.TaxCategory
	if cat == "" {
		cat = CatTaxable
	}
	rate, cessRate, perUnit := l.TaxRateBP, l.CessRateBP, l.CessPerUnitPaise
	if cat != CatTaxable || m.zeroRated || m.noTax {
		rate, cessRate, perUnit = 0, 0, 0
	} else if !m.on.IsZero() && !ValidRate(l.TaxRateBP, m.on) {
		return LineTax{}, fmt.Errorf("GST rate %s%% was not in force on %s", bpPct(l.TaxRateBP), m.on.In(IST).Format(time.DateOnly))
	}

	specific := int64(math.Round(l.Qty * float64(perUnit)))
	taxable := net
	if l.PriceIncludesTax && (rate+cessRate > 0 || specific > 0) {
		// net = taxable * (1 + (rate+cess)/10000) + specific  →  taxable = (net-specific)*10000/(10000+rate+cess)
		taxable = money.MulDiv(net-specific, 10000, int64(10000+rate+cessRate))
		if taxable < 0 {
			return LineTax{}, errors.New("specific cess exceeds the inclusive price")
		}
	}
	lt := LineTax{GrossPaise: gross, DiscountPaise: discount, TaxablePaise: taxable, TaxCategory: cat, RateBP: rate}
	if m.inter {
		lt.IGSTPaise = money.ApplyBP(taxable, rate)
	} else {
		// CGST and SGST are each levied at half the rate and rounded independently.
		half := money.MulDiv(taxable, int64(rate), 20000)
		lt.CGSTPaise, lt.SGSTPaise = half, half
	}
	lt.CessPaise = money.ApplyBP(taxable, cessRate) + specific
	lt.TotalPaise = taxable + lt.CGSTPaise + lt.SGSTPaise + lt.IGSTPaise + lt.CessPaise
	if l.PriceIncludesTax && lt.TotalPaise != net {
		// Absorb the 1-paisa rounding gap into taxable value so the customer pays exactly the shelf price.
		diff := net - lt.TotalPaise
		lt.TaxablePaise += diff
		lt.TotalPaise = net
	}
	return lt, nil
}

// allocate splits total across weights pro-rata using the largest-remainder
// method, so the shares always add up to total exactly.
func allocate(total int64, weights []int64) []int64 {
	shares := make([]int64, len(weights))
	if total == 0 || len(weights) == 0 {
		return shares
	}
	var sum int64
	for _, w := range weights {
		sum += w
	}
	if sum <= 0 {
		return shares
	}
	type rem struct {
		i int
		r int64
	}
	rems := make([]rem, len(weights))
	var given int64
	for i, w := range weights {
		n := total * w
		shares[i] = n / sum
		rems[i] = rem{i, n % sum}
		given += shares[i]
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r > rems[b].r })
	for k := 0; given < total && k < len(rems); k++ {
		shares[rems[k].i]++
		given++
	}
	return shares
}

// roundHead rounds a tax head to the nearest rupee (Sec 170 CGST Act).
func roundHead(p int64) int64 {
	r, _ := money.RoundToRupee(p)
	return r
}

func bpPct(bp int) string {
	if bp%100 == 0 {
		return itoa(bp / 100)
	}
	return fmt.Sprintf("%d.%02d", bp/100, bp%100)
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
