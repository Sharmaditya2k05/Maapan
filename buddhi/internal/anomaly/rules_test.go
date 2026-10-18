package anomaly

import (
	"errors"
	"math"
	"testing"
)

func TestWelford(t *testing.T) {
	var s Stats
	for _, x := range []float64{2, 4, 4, 4, 5, 5, 7, 9} {
		s = s.Update(x)
	}
	if math.Abs(s.Mean-5) > 1e-9 || math.Abs(s.StdDev()-2.138089935) > 1e-6 {
		t.Fatalf("mean %v sd %v", s.Mean, s.StdDev())
	}
}

func TestDiscountOutlier(t *testing.T) {
	var s Stats
	for i := 0; i < 30; i++ {
		s = s.Update(float64(i % 3)) // 0,1,2% discounts
	}
	if IsDiscountOutlier(s, 2.5) {
		t.Fatal("2.5% is normal")
	}
	if !IsDiscountOutlier(s, 25) {
		t.Fatal("25% is an outlier")
	}
	if IsDiscountOutlier(Stats{N: 5, Mean: 1, M2: 1}, 50) {
		t.Fatal("too little history")
	}
}

func TestDiscountEstimate(t *testing.T) {
	d50 := int64(500)
	tr := true
	lines := []Line{
		{Qty: 2, UnitPricePaise: 1000, TaxablePaise: 1695, TotalPaise: 2000},                        // MRP, no discount
		{Qty: 1, UnitPricePaise: 10000, TaxablePaise: 9000, TotalPaise: 10620},                      // exclusive, 10% off
		{Qty: 1, UnitPricePaise: 5000, TaxablePaise: 4237, TotalPaise: 4500, PriceIncludesTax: &tr}, // inclusive, ₹5 off
		{Qty: 1, UnitPricePaise: 5000, DiscountPaise: &d50, TaxablePaise: 4500, TotalPaise: 5310},
	}
	d, g := Discount(lines)
	if g != 22000 || d != 0+1000+500+500 {
		t.Fatalf("discount %d gross %d", d, g)
	}
}

func TestHighCancellations(t *testing.T) {
	rows := []CashierCancels{
		{CashierID: "a", Bills: 200, Cancelled: 2},
		{CashierID: "b", Bills: 200, Cancelled: 3},
		{CashierID: "c", Bills: 50, Cancelled: 10},
	}
	out := HighCancellations(rows)
	if len(out) != 1 || out[0].CashierID != "c" {
		t.Fatalf("got %+v", out)
	}
}

func TestGSTINCheck(t *testing.T) {
	ok := func(string) error { return nil }
	bad := func(string) error { return errors.New("bad") }
	if GSTINCheck("purchase", "p1", "", 100, ok) != "missing_gstin" {
		t.Fatal("purchase without gstin")
	}
	if GSTINCheck("sale", "", "", 10_000_000, ok) != "" {
		t.Fatal("walk-in B2C is fine")
	}
	if GSTINCheck("sale", "p1", "", B2BThresholdPaise, ok) != "missing_gstin" {
		t.Fatal("large sale to saved party")
	}
	if GSTINCheck("sale", "p1", "X", 1, bad) != "invalid_gstin" {
		t.Fatal("invalid")
	}
}
