package gstinsight

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Shaped like kar's GET /v1/returns/gstr2b response (recon.Reconciliation).
const sample = `{
  "gstin": "27AAPFU0939F1ZV", "period": "092026",
  "import": {"id": "imp1", "source": "upload"},
  "counts": {"matched": 1, "mismatch": 1, "missing_in_books": 1, "missing_in_2b": 2},
  "itc": {
    "accepted": {"docs": 0, "igst": 0, "cgst": 0, "sgst": 0, "cess": 0},
    "deemed_accepted": {"docs": 3, "igst": 0, "cgst": 1800.00, "sgst": 1800.00, "cess": 0},
    "pending": {"docs": 1, "igst": 90.50, "cgst": 0, "sgst": 0, "cess": 0},
    "rejected": {"docs": 0, "igst": 0, "cgst": 0, "sgst": 0, "cess": 0},
    "not_available": {"docs": 0, "igst": 0, "cgst": 0, "sgst": 0, "cess": 0},
    "available": {"igst": 0, "cgst": 1800.00, "sgst": 1800.00, "cess": 0}
  },
  "items": [
    {"status": "mismatch", "doc_key": "k1",
     "doc": {"doc_key": "k1", "supplier_gstin": "27AAACR5055K1Z7", "supplier_name": "Ravi Agencies", "doc_type": "INV", "doc_no": "RA/77",
             "doc_date": "2026-09-05", "taxable": 10000.00, "igst": 0, "cgst": 900.00, "sgst": 900.00, "cess": 0, "itc_available": "Y"},
     "purchase": {"id": "p1", "number": "PB-12", "supplier_gstin": "27AAACR5055K1Z7", "supplier_name": "Ravi Agencies",
                  "taxable": 12000.00, "igst": 0, "cgst": 1080.00, "sgst": 1080.00, "cess": 0},
     "diffs": [{"field": "taxable", "books": 12000.00, "gstr2b": 10000.00}, {"field": "cgst", "books": 1080, "gstr2b": 900}]},
    {"status": "missing_in_2b", "purchase": {"id": "p2", "number": "PB-13", "supplier_gstin": "24AAACS1234A1Z5", "supplier_name": "Shah Textiles",
        "taxable": 50000.00, "igst": 9000.00, "cgst": 0, "sgst": 0, "cess": 0}},
    {"status": "missing_in_2b", "purchase": {"id": "p3", "number": "PB-14", "supplier_gstin": "24AAACS1234A1Z5", "supplier_name": "Shah Textiles",
        "taxable": "1000.5", "igst": "180.09", "cgst": 0, "sgst": 0, "cess": 0}},
    {"status": "missing_in_books", "doc_key": "k3", "doc": {"doc_key": "k3", "supplier_gstin": "29AAGCB7383J1Z4", "supplier_name": "Bharat Foods",
        "doc_type": "INV", "doc_no": "BF-1", "doc_date": "2026-09-20", "taxable": 500.00, "igst": 90.50, "cgst": 0, "sgst": 0, "cess": 0}},
    {"status": "matched", "doc_key": "k4"}
  ]
}`

func TestSummarise(t *testing.T) {
	var rec Reconciliation
	if err := json.Unmarshal([]byte(sample), &rec); err != nil {
		t.Fatal(err)
	}
	in := Summarise(rec, time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	if !in.Imported || in.Period != "092026" {
		t.Fatalf("header: %+v", in)
	}
	// missing in 2B: 9000.00 + 180.09 rupees of IGST
	if in.MissingIn2BTaxPaise != 918009 {
		t.Fatalf("missing in 2B tax %d", in.MissingIn2BTaxPaise)
	}
	// mismatch: books 2160 vs 2B 1800 → 360 rupees excess
	if in.MismatchExcessPaise != 36000 || in.ITCAtRiskPaise != 918009+36000 {
		t.Fatalf("excess %d at risk %d", in.MismatchExcessPaise, in.ITCAtRiskPaise)
	}
	if in.UnbookedTaxPaise != 9050 || in.PendingITCPaise != 9050 || in.AvailableITCPaise != 360000 {
		t.Fatalf("unbooked %d pending %d available %d", in.UnbookedTaxPaise, in.PendingITCPaise, in.AvailableITCPaise)
	}
	if len(in.MissingIn2BSuppliers) != 1 || in.MissingIn2BSuppliers[0].Docs != 2 || in.MissingIn2BSuppliers[0].Name != "Shah Textiles" {
		t.Fatalf("suppliers %+v", in.MissingIn2BSuppliers)
	}
	if len(in.Mismatches) != 1 || strings.Join(in.Mismatches[0].Fields, ",") != "taxable,cgst" || in.Mismatches[0].BooksNumber != "PB-12" {
		t.Fatalf("mismatches %+v", in.Mismatches)
	}
	if in.Card.Severity != "warning" || !strings.Contains(in.Card.Value, "at risk") {
		t.Fatalf("card %+v", in.Card)
	}
	if !strings.Contains(strings.Join(in.Highlights, " "), "Shah Textiles") {
		t.Fatalf("highlights %v", in.Highlights)
	}
}

func TestSummariseNotImported(t *testing.T) {
	in := Summarise(Reconciliation{GSTIN: "x", Period: "092026", Warning: "no GSTR-2B imported"}, time.Now())
	if in.Imported || in.Card.Severity != "info" || len(in.Warnings) != 1 {
		t.Fatalf("%+v", in)
	}
	clean := Summarise(Reconciliation{GSTIN: "x", Period: "092026", Import: json.RawMessage(`{}`), Counts: map[string]int{"matched": 4}}, time.Now())
	if clean.Card.Severity != "ok" || clean.ITCAtRiskPaise != 0 {
		t.Fatalf("%+v", clean.Card)
	}
}

func TestPeriods(t *testing.T) {
	if p := PreviousPeriod(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)); p != "122025" {
		t.Fatal(p)
	}
	if !ValidPeriod("092026") || ValidPeriod("2026-09") || ValidPeriod("132026") {
		t.Fatal("period validation")
	}
}
