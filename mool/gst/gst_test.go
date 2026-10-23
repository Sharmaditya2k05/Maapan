package gst

import (
	"strings"
	"testing"
	"time"
)

func TestValidateGSTIN(t *testing.T) {
	good := "27AAPFU0939F1ZV" // well-known valid sample GSTIN
	if err := ValidateGSTIN(good); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if err := ValidateGSTIN("27AAPFU0939F1ZA"); err == nil {
		t.Fatal("expected checksum failure")
	}
	if err := ValidateGSTIN("00AAPFU0939F1ZV"); err == nil {
		t.Fatal("expected unknown state")
	}
	if err := ValidateGSTIN("27AAPF10939F1ZV"); err == nil {
		t.Fatal("expected PAN shape failure")
	}
	if StateCode(good) != "27" || PAN(good) != "AAPFU0939F" {
		t.Fatal("state/PAN extraction")
	}
	if !ValidState("25") || !ValidState("99") {
		t.Fatal("legacy Daman & Diu and Centre jurisdiction codes must be known")
	}
	// A deductor GSTIN has D in position 14; the checksum is recomputed for the sample.
	d := "27AAPFU0939F1D" + string(checksum("27AAPFU0939F1D"))
	if err := ValidateGSTIN(d); err != nil || !IsTaxDeductor(d) || IsTaxCollector(d) {
		t.Fatalf("deductor GSTIN: %v", err)
	}
}

func TestComputeIntraState(t *testing.T) {
	s, err := Compute([]Line{{Qty: 2, UnitPricePaise: 50000, TaxRateBP: 1800}}, "27", "27", true)
	if err != nil {
		t.Fatal(err)
	}
	if s.TaxablePaise != 100000 || s.CGSTPaise != 9000 || s.SGSTPaise != 9000 || s.IGSTPaise != 0 {
		t.Fatalf("unexpected: %+v", s)
	}
	if s.GrandPaise != 118000 || s.RoundOffPaise != 0 || s.TaxPaise != 18000 {
		t.Fatalf("grand: %+v", s)
	}
	if len(s.ByRate) != 1 || s.ByRate[0].TaxRateBP != 1800 || s.ByRate[0].TaxCategory != CatTaxable {
		t.Fatalf("by_rate: %+v", s.ByRate)
	}
}

func TestComputeInterStateWithRoundOff(t *testing.T) {
	s, err := Compute([]Line{{Qty: 1, UnitPricePaise: 9999, TaxRateBP: 1200}}, "27", "29", true)
	if err != nil {
		t.Fatal(err)
	}
	// 99.99 + 12% = 11.9988 → IGST 12.00 ; total 111.99 → rounds to 112.00
	if !s.InterState || s.IGSTPaise != 1200 || s.GrandPaise != 11200 || s.RoundOffPaise != 1 {
		t.Fatalf("unexpected: %+v", s)
	}
}

func TestComputeTaxInclusiveKeepsShelfPrice(t *testing.T) {
	s, err := Compute([]Line{{Qty: 3, UnitPricePaise: 9900, TaxRateBP: 500, PriceIncludesTax: true}}, "07", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Lines[0].TotalPaise != 29700 {
		t.Fatalf("customer must pay shelf price, got %+v", s.Lines[0])
	}
}

func TestPercentageAndInvoiceDiscounts(t *testing.T) {
	// 10 % line discount on ₹1,000 → ₹900 net; invoice discount ₹90 shared pro-rata over two lines.
	lines := []Line{
		{Qty: 1, UnitPricePaise: 100000, DiscountBP: 1000, TaxRateBP: 1800},
		{Qty: 1, UnitPricePaise: 30000, TaxRateBP: 500},
	}
	s, err := ComputeWith(lines, Options{StoreState: "27", InvoiceDiscountPaise: 9000})
	if err != nil {
		t.Fatal(err)
	}
	if s.Lines[0].DiscountPaise+s.Lines[1].DiscountPaise != 10000+9000 {
		t.Fatalf("discounts must add up: %+v", s.Lines)
	}
	if s.InvoiceDiscountPaise != 9000 || s.DiscountPaise != 19000 {
		t.Fatalf("summary discounts: %+v", s)
	}
	// Shares by net value 90,000 : 30,000 → 6,750 : 2,250.
	if s.Lines[0].DiscountPaise != 16750 || s.Lines[1].DiscountPaise != 2250 {
		t.Fatalf("pro-rata shares: %d %d", s.Lines[0].DiscountPaise, s.Lines[1].DiscountPaise)
	}
	if s.TaxablePaise != 83250+27750 {
		t.Fatalf("taxable: %d", s.TaxablePaise)
	}
}

func TestAllocateSumsExactly(t *testing.T) {
	for _, total := range []int64{0, 1, 2, 99, 1001} {
		shares := allocate(total, []int64{333, 333, 334})
		var sum int64
		for _, s := range shares {
			sum += s
		}
		if sum != total {
			t.Fatalf("total %d: shares %v", total, shares)
		}
	}
}

func TestSpecificCess(t *testing.T) {
	// Aerated water: 40 % GST (exclusive) with ₹1.50 per litre specific cess, 10 litres.
	s, err := ComputeWith([]Line{{Qty: 10, UnitPricePaise: 5000, TaxRateBP: 4000, CessPerUnitPaise: 150}},
		Options{StoreState: "27", On: time.Date(2026, 1, 10, 0, 0, 0, 0, IST)})
	if err != nil {
		t.Fatal(err)
	}
	if s.CessPaise != 1500 || s.CGSTPaise != 10000 || s.SGSTPaise != 10000 || s.GrandPaise != 50000+20000+1500 {
		t.Fatalf("specific cess: %+v", s)
	}
	// Inclusive: shelf price ₹71.50 per litre contains 40 % GST and ₹1.50 cess.
	s, err = ComputeWith([]Line{{Qty: 1, UnitPricePaise: 7150, TaxRateBP: 4000, CessPerUnitPaise: 150, PriceIncludesTax: true}},
		Options{StoreState: "27"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Lines[0].TotalPaise != 7150 || s.Lines[0].TaxablePaise != 5000 || s.CessPaise != 150 {
		t.Fatalf("inclusive specific cess: %+v", s.Lines[0])
	}
}

func TestExemptNilRatedAndNonGSTBuckets(t *testing.T) {
	lines := []Line{
		{Qty: 1, UnitPricePaise: 10000, TaxRateBP: 1800},
		{Qty: 1, UnitPricePaise: 20000, TaxRateBP: 1800, TaxCategory: CatExempt}, // rate ignored
		{Qty: 1, UnitPricePaise: 30000, TaxCategory: CatNilRated},
		{Qty: 1, UnitPricePaise: 40000, TaxCategory: CatNonGST},
	}
	s, err := ComputeWith(lines, Options{StoreState: "27"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ExemptPaise != 20000 || s.NilRatedPaise != 30000 || s.NonGSTPaise != 40000 || s.TaxablePaise != 100000 {
		t.Fatalf("buckets: %+v", s)
	}
	if s.TaxPaise != 1800 || s.GrandPaise != 101800 {
		t.Fatalf("only the taxable line is taxed: %+v", s)
	}
	if len(s.ByRate) != 4 || s.ByRate[0].TaxCategory != CatExempt || s.ByRate[3].TaxRateBP != 1800 {
		t.Fatalf("by_rate must split categories: %+v", s.ByRate)
	}
}

func TestCompositionBillOfSupply(t *testing.T) {
	s, err := ComputeWith([]Line{{Qty: 2, UnitPricePaise: 5000, TaxRateBP: 1800, PriceIncludesTax: true}},
		Options{StoreState: "27", Composition: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.BillOfSupply || s.TaxPaise != 0 || s.TaxablePaise != 10000 || s.GrandPaise != 10000 || s.Lines[0].RateBP != 0 {
		t.Fatalf("composition: %+v", s)
	}
}

func TestReverseChargeExcludesTaxFromTotal(t *testing.T) {
	s, err := ComputeWith([]Line{{Qty: 1, UnitPricePaise: 100000, TaxRateBP: 500}},
		Options{StoreState: "27", ReverseCharge: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.ReverseCharge || s.CGSTPaise != 2500 || s.SGSTPaise != 2500 || s.GrandPaise != 100000 {
		t.Fatalf("rcm: %+v", s)
	}
}

func TestZeroRatedSupplies(t *testing.T) {
	line := []Line{{Qty: 1, UnitPricePaise: 100000, TaxRateBP: 1800}}
	s, err := ComputeWith(line, Options{StoreState: "27", PlaceOfSupply: "96", SupplyCategory: SupplyExportWithoutPayment})
	if err != nil {
		t.Fatal(err)
	}
	if s.TaxPaise != 0 || s.ZeroRatedPaise != 100000 || s.GrandPaise != 100000 || !s.InterState {
		t.Fatalf("export under LUT: %+v", s)
	}
	s, err = ComputeWith(line, Options{StoreState: "27", PlaceOfSupply: "27", SupplyCategory: SupplySEZWithPayment})
	if err != nil {
		t.Fatal(err)
	}
	if s.IGSTPaise != 18000 || s.CGSTPaise != 0 || !s.InterState || s.ZeroRatedPaise != 100000 {
		t.Fatalf("SEZ with payment must charge IGST even within the state: %+v", s)
	}
}

func TestRoundTaxHeads(t *testing.T) {
	// Three lines of ₹33.33 at 18 %: per-line halves of 3.00 → heads 8.99/8.99; Sec 170 rounds each to ₹9.
	lines := []Line{
		{Qty: 1, UnitPricePaise: 3333, TaxRateBP: 1800},
		{Qty: 1, UnitPricePaise: 3333, TaxRateBP: 1800},
		{Qty: 1, UnitPricePaise: 3333, TaxRateBP: 1800},
	}
	s, err := ComputeWith(lines, Options{StoreState: "27", RoundTaxHeads: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.CGSTPaise != 900 || s.SGSTPaise != 900 || s.GrandPaise != 9999+1800 {
		t.Fatalf("head rounding: %+v", s)
	}
}

func TestTCSOnInvoiceValue(t *testing.T) {
	s, err := ComputeWith([]Line{{Qty: 1, UnitPricePaise: 10000000, TaxRateBP: 1800}},
		Options{StoreState: "27", TCSBP: 10, RoundToRupee: true})
	if err != nil {
		t.Fatal(err)
	}
	// ₹1,00,000 + 18 % = ₹1,18,000; TCS 0.1 % = ₹118.
	if s.TCSPaise != 11800 || s.GrandPaise != 11800000+11800 {
		t.Fatalf("tcs: %+v", s)
	}
}

func TestRateValidityByDate(t *testing.T) {
	aug := time.Date(2025, 8, 15, 0, 0, 0, 0, IST)
	oct := time.Date(2025, 10, 15, 0, 0, 0, 0, IST)
	if !ValidRate(1200, aug) || ValidRate(1200, oct) || ValidRate(4000, aug) || !ValidRate(4000, oct) {
		t.Fatal("12 % ends and 40 % starts on 22 Sep 2025")
	}
	if !ValidRate(2800, oct) || !ValidRate(0, oct) || ValidRate(700, oct) {
		t.Fatal("28 % stays (tobacco), 0 % always, 7 % never")
	}
	if got := RatesOn(oct); strings.Join(strings.Fields(strings.Trim(strings.ReplaceAll(fmtInts(got), ",", " "), "[]")), ",") != "0,25,150,300,500,1800,2800,4000" {
		t.Fatalf("rates on %s: %v", oct, got)
	}
	line := []Line{{Qty: 1, UnitPricePaise: 1000, TaxRateBP: 1200}}
	if _, err := ComputeWith(line, Options{StoreState: "27", On: aug}); err != nil {
		t.Fatalf("12 %% in Aug 2025 must pass: %v", err)
	}
	if _, err := ComputeWith(line, Options{StoreState: "27", On: oct}); err == nil {
		t.Fatal("12 % in Oct 2025 must fail")
	}
	// Non-taxable lines never fail the rate check.
	if _, err := ComputeWith([]Line{{Qty: 1, UnitPricePaise: 1000, TaxRateBP: 1200, TaxCategory: CatExempt}},
		Options{StoreState: "27", On: oct}); err != nil {
		t.Fatal(err)
	}
}

func fmtInts(xs []int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range xs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(itoa(x))
	}
	b.WriteByte(']')
	return b.String()
}

func TestPlaceOfSupply(t *testing.T) {
	cases := []struct {
		in   SupplyInput
		want string
	}{
		{SupplyInput{Goods: true, SupplierState: "27"}, "27"},                                              // walk-in
		{SupplyInput{Goods: true, SupplierState: "27", BillToState: "29"}, "29"},                           // delivered to buyer
		{SupplyInput{Goods: true, SupplierState: "27", BillToState: "29", ShipToState: "24"}, "29"},        // bill-to/ship-to
		{SupplyInput{Goods: true, SupplierState: "27", ShipToState: "24"}, "24"},                           // ship-to only
		{SupplyInput{Goods: false, SupplierState: "27", BillToState: "29", Registered: true}, "29"},        // B2B service
		{SupplyInput{Goods: false, SupplierState: "27"}, "27"},                                             // B2C service, no address
		{SupplyInput{Goods: false, SupplierState: "27", BillToState: "07", Registered: false}, "07"},       // B2C service, address on record
	}
	for _, c := range cases {
		if got := PlaceOfSupply(c.in); got != c.want {
			t.Errorf("%+v: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestAmountInWords(t *testing.T) {
	got := AmountInWords(12345678950)
	want := "Rupees Twelve Crore Thirty Four Lakh Fifty Six Thousand Seven Hundred Eighty Nine and Fifty Paise Only"
	if got != want {
		t.Fatalf("got %q", got)
	}
}
