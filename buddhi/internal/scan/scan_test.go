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

func TestRatesAreEffectiveDated(t *testing.T) {
	line := ExtractedLine{Name: "Tata Salt 1kg", HSN: "2501", Qty: 1, Unit: "pcs", Rate: 100, GSTRatePercent: 12, Amount: 100}
	old := Build(Extracted{SupplierGSTIN: "27AAPFU0939F1ZV", InvoiceNumber: "A1", InvoiceDate: "2025-08-10",
		Lines: []ExtractedLine{line}, Confidence: 0.9}, catalog, "27")
	if w := strings.Join(old.Warnings, " | "); strings.Contains(w, "standard slab") {
		t.Fatalf("12%% was a slab in Aug 2025: %s", w)
	}
	cur := Build(Extracted{SupplierGSTIN: "27AAPFU0939F1ZV", InvoiceNumber: "A2", InvoiceDate: "2026-10-10",
		Lines: []ExtractedLine{line}, Confidence: 0.9}, catalog, "27")
	if w := strings.Join(cur.Warnings, " | "); !strings.Contains(w, "not a standard slab on 10 Oct 2026") {
		t.Fatalf("12%% after 22 Sep 2025 must warn: %s", w)
	}
	forty := line
	forty.GSTRatePercent = 40
	pre := Build(Extracted{SupplierGSTIN: "27AAPFU0939F1ZV", InvoiceNumber: "A3", InvoiceDate: "2025-09-01",
		Lines: []ExtractedLine{forty}, Confidence: 0.9}, catalog, "27")
	if w := strings.Join(pre.Warnings, " | "); !strings.Contains(w, "not a standard slab") {
		t.Fatalf("40%% did not exist before 22 Sep 2025: %s", w)
	}
}

func TestItemMasterMatching(t *testing.T) {
	master := Merge(catalog, []CatalogItem{
		{ItemID: "i2", Name: "Tata Salt 1kg", HSN: "25010020", Unit: "PCS", TaxRateBP: 0, PurchasePricePaise: 2000},
		{ItemID: "i9", Name: "Amul Butter 500g", SKU: "AMB500", Barcode: "8901262010016", HSN: "0405", Unit: "PCS", TaxRateBP: 1200, PurchasePricePaise: 24000},
		{ItemID: "i4", Name: "Tata Salt Lite 1kg", HSN: "2501", Unit: "PCS", TaxRateBP: 0, PurchasePricePaise: 3500},
	})
	ext := Extracted{SupplierGSTIN: "27AAPFU0939F1ZV", InvoiceNumber: "B1", InvoiceDate: "2026-10-01", Confidence: 0.9,
		Lines: []ExtractedLine{
			{Name: "TATA SALT 1KG", HSN: "2501", Qty: 10, Unit: "pcs", Rate: 20, GSTRatePercent: 0, Amount: 200},
			{Name: "Butter 8901262010016", Qty: 2, Unit: "pcs", Rate: 240, GSTRatePercent: 5, Amount: 480},
			{Name: "Unknown widget", Qty: 1, Rate: 10, GSTRatePercent: 18, Amount: 10},
		}}
	r := Build(ext, master, "27")
	if r.Draft.Lines[0].ItemID != "i2" || r.Matches[0].Method != "name" || r.Matches[0].Score < 0.9 {
		t.Fatalf("salt: %+v", r.Matches[0])
	}
	if len(r.Matches[0].Alternatives) == 0 || r.Matches[0].Alternatives[0].ItemID != "i4" {
		t.Fatalf("salt alternatives: %+v", r.Matches[0].Alternatives)
	}
	if r.Draft.Lines[1].ItemID != "i9" || r.Matches[1].Method != "identifier" {
		t.Fatalf("barcode match: %+v", r.Matches[1])
	}
	if r.Draft.Lines[1].HSN != "0405" {
		t.Fatalf("HSN should be filled from the master: %+v", r.Draft.Lines[1])
	}
	joined := strings.Join(r.Warnings, " | ")
	if !strings.Contains(joined, "master still has 12%") {
		t.Errorf("rate mismatch vs master not flagged: %s", joined)
	}
	if r.Draft.Lines[2].ItemID != "" || !strings.Contains(joined, "Unknown widget") {
		t.Errorf("unknown line must stay unmatched: %+v", r.Matches[2])
	}

	// A different HSN pulls a borderline name match below the threshold.
	m := MatchLines([]MatchInput{{Name: "Tata Salt", HSN: "1905"}}, master)
	if m[0].ItemID != "" && m[0].Score >= 0.95 {
		t.Errorf("HSN conflict should lower the score: %+v", m[0])
	}

	ids, codes := Shortlist([]string{"Tata Salt 1kg", "Butter 8901262010016"}, catalog, 2)
	if len(ids) == 0 || ids[0] != "i2" || len(codes) != 1 || codes[0] != "8901262010016" {
		t.Fatalf("shortlist %v %v", ids, codes)
	}
}
