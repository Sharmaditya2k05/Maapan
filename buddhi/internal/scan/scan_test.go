package scan

import (
	"strings"
	"testing"
)

var catalog = []CatalogItem{
	{ItemID: "i1", Name: "Parle-G Biscuit 100g"},
	{ItemID: "i2", Name: "Tata Salt 1kg"},
	{ItemID: "i3", Name: "Fortune Sunflower Oil 1L"},
}

func TestBestMatch(t *testing.T) {
	cases := map[string]string{
		"PARLE G BISCUIT 100 G":  "i1",
		"Salt Tata 1 kg":         "i2",
		"Fortune Sunflwr Oil 1L": "i3",
	}
	for in, want := range cases {
		it, score := BestMatch(in, catalog)
		if it.ItemID != want || score < MatchThreshold {
			t.Errorf("%q -> %s (%.2f), want %s", in, it.ItemID, score, want)
		}
	}
	if _, score := BestMatch("Amul Butter 500g", catalog); score >= MatchThreshold {
		t.Errorf("unrelated item matched with %.2f", score)
	}
}

func TestBuildCleanBill(t *testing.T) {
	ext := Extracted{
		SupplierName: "Sharma Traders", SupplierGSTIN: "27AAPFU0939F1ZV", InvoiceNumber: "ST/101", InvoiceDate: "2026-10-01",
		Lines: []ExtractedLine{
			{Name: "Tata Salt 1kg", HSN: "2501", Qty: 10, Unit: "pcs", Rate: 20, GSTRatePercent: 0, Amount: 200},
			{Name: "Parle G 100g", HSN: "1905", Qty: 100, Unit: "pcs", Rate: 8, GSTRatePercent: 18, Amount: 800},
		},
		TaxableTotal: 1000, CGSTTotal: 72, SGSTTotal: 72, GrandTotal: 1144, Confidence: 0.95,
	}
	r := Build(ext, catalog, "27")
	if len(r.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", r.Warnings)
	}
	if r.ComputedGrand != 114400 || r.Draft.Lines[1].ItemID != "i1" || r.Draft.Lines[1].TaxRateBP != 1800 {
		t.Fatalf("bad draft: %+v", r)
	}
	if r.Draft.DocType != "purchase" || r.Draft.PartyID != "" || r.Confidence != 0.95 {
		t.Fatalf("draft header: %+v conf %v", r.Draft, r.Confidence)
	}
}

func TestBuildFlagsProblems(t *testing.T) {
	ext := Extracted{
		SupplierGSTIN: "27AAPFU0939F1ZA", // bad checksum
		Lines:         []ExtractedLine{{Name: "Mystery item", Qty: 1, Rate: 100, GSTRatePercent: 17, Amount: 100}},
		TaxableTotal:  150, GrandTotal: 500, Confidence: 0.9,
	}
	r := Build(ext, catalog, "27")
	joined := strings.Join(r.Warnings, " | ")
	for _, want := range []string{"GSTIN looks wrong", "not a standard slab", "not in your item list", "taxable total", "invoice number"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning %q in %s", want, joined)
		}
	}
	if r.Confidence >= 0.9 {
		t.Errorf("confidence should drop, got %v", r.Confidence)
	}
}
