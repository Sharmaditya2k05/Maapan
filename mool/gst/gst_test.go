package gst

import "testing"

func TestValidateGSTIN(t *testing.T) {
	good := "27AAPFU0939F1ZV" // well-known valid sample GSTIN
	if err := ValidateGSTIN(good); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if err := ValidateGSTIN("27AAPFU0939F1ZA"); err == nil {
		t.Fatal("expected checksum failure")
	}
	if err := ValidateGSTIN("99AAPFU0939F1ZV"); err == nil {
		t.Fatal("expected unknown state")
	}
	if StateCode(good) != "27" || PAN(good) != "AAPFU0939F" {
		t.Fatal("state/PAN extraction")
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
	if s.GrandPaise != 118000 || s.RoundOffPaise != 0 {
		t.Fatalf("grand: %+v", s)
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

func TestAmountInWords(t *testing.T) {
	got := AmountInWords(12345678950)
	want := "Rupees Twelve Crore Thirty Four Lakh Fifty Six Thousand Seven Hundred Eighty Nine and Fifty Paise Only"
	if got != want {
		t.Fatalf("got %q", got)
	}
}
