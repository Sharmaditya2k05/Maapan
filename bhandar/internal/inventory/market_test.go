package inventory

import (
	"reflect"
	"testing"
)

func TestEvaluateScheme(t *testing.T) {
	free := Scheme{Kind: SchemeFreeQty, BuyQty: 10, FreeQty: 1}
	if b := EvaluateScheme(free, 25, 25000, 1000); b.FreeQty != 2 || b.ValuePaise != 2000 {
		t.Errorf("buy 10 get 1 on 25: %+v", b)
	}
	if b := EvaluateScheme(free, 9, 9000, 1000); b.FreeQty != 0 || b.ValuePaise != 0 {
		t.Errorf("below buy qty: %+v", b)
	}
	pct := Scheme{Kind: SchemePercent, Slabs: []Slab{{MinQty: 10, PercentBP: 500}, {MinQty: 24, PercentBP: 1000}}}
	if b := EvaluateScheme(pct, 24, 22800, 0); b.DiscountPaise != 2280 || b.Slab.PercentBP != 1000 {
		t.Errorf("percent top slab: %+v", b)
	}
	if b := EvaluateScheme(pct, 12, 11400, 0); b.DiscountPaise != 570 {
		t.Errorf("percent first slab: %+v", b)
	}
	if b := EvaluateScheme(pct, 5, 4750, 0); b.DiscountPaise != 0 || b.Slab != nil {
		t.Errorf("percent below slabs: %+v", b)
	}
	flat := Scheme{Kind: SchemeFlat, Slabs: []Slab{{MinQty: 0, OffPerUnitPaise: 50}, {MinQty: 6, OffPerUnitPaise: 80}}}
	if b := EvaluateScheme(flat, 6, 6000, 0); b.DiscountPaise != 480 {
		t.Errorf("flat: %+v", b)
	}
	if b := EvaluateScheme(flat, 2, 60, 0); b.DiscountPaise != 60 {
		t.Errorf("flat discount is capped at the line value: %+v", b)
	}
}

func TestVariantKeyAndLabel(t *testing.T) {
	a := Attrs{"size": "M", "colour": "Red"}
	if k := VariantKey(a); k != "colour=red|size=m" {
		t.Errorf("key %q", k)
	}
	if l := variantLabel(a); l != "Red / M" {
		t.Errorf("label %q", l)
	}
	in := VariantsInput{Variants: []VariantInput{{Attrs: Attrs{" Color ": "Blue"}}, {Attrs: Attrs{"colour": "blue"}}}}
	if err := in.Validate(); err == nil {
		t.Error("color/colour Blue/blue must be the same variant")
	}
}

func TestSignificantWords(t *testing.T) {
	got := significantWords("Parle-G Glucose Biscuit 100g x12")
	if want := []string{"parle", "glucose", "biscuit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSchemeAndQuoteValidation(t *testing.T) {
	bad := []SchemeInput{
		{Name: "x", Kind: SchemeFreeQty, ItemIDs: []string{"a"}, ValidFrom: "2026-01-01"},                                  // no buy/free
		{Name: "x", Kind: SchemePercent, ItemIDs: []string{"a"}, ValidFrom: "2026-01-01"},                                  // no slabs
		{Name: "x", Kind: SchemePercent, Category: "c", ValidFrom: "2026-01-01", Slabs: []Slab{{MinQty: 1, PercentBP: 0}}}, // 0 %
		{Name: "x", Kind: SchemeFlat, ValidFrom: "2026-01-01", Slabs: []Slab{{MinQty: 1, OffPerUnitPaise: 5}}},             // no scope
		{Name: "x", Kind: SchemeFlat, Category: "c", Slabs: []Slab{{MinQty: 1, OffPerUnitPaise: 5}}},                       // no date
		{Name: "x", Kind: SchemeFlat, Category: "c", ValidFrom: "2026-02-01", ValidTo: "2026-01-01", Slabs: []Slab{{MinQty: 1, OffPerUnitPaise: 5}}},
	}
	for i, in := range bad {
		if in.Validate() == nil {
			t.Errorf("scheme %d must be invalid", i)
		}
	}
	q := QuoteInput{Channel: "vip", Lines: []QuoteLine{{ItemID: "a", Qty: 1}}}
	if q.Validate() == nil {
		t.Error("unknown channel must be invalid")
	}
}
