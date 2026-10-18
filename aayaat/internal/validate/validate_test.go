package validate

import (
	"strings"
	"testing"

	"github.com/taraju/aayaat/internal/mapping"
)

func idx(fields ...string) map[string]int {
	m := map[string]int{}
	for i, f := range fields {
		m[f] = i
	}
	return m
}

func TestPartyRows(t *testing.T) {
	v := New(mapping.KindParties, idx("name", "gstin", "phone", "opening_balance", "dr_cr", "type", "state"))
	ok := v.Row(2, []string{"Ram  Stores", "27aapfu0939f1zv", "098765 43210", "1,200.50", "Cr", "Sundry Creditors", "Maharashtra"})
	if len(ok.Errors) != 0 {
		t.Fatalf("unexpected errors %v", ok.Errors)
	}
	p := ok.Record.(PartyRecord)
	if p.Name != "Ram Stores" || p.GSTIN != "27AAPFU0939F1ZV" || p.Phone != "+919876543210" || p.StateCode != "27" ||
		p.OpeningBalancePaise != 120050 || p.OpeningType != "cr" || p.Type != "supplier" {
		t.Fatalf("record %+v", p)
	}
	if ok.Side != "cr" || ok.Amount != 120050 {
		t.Fatal("reconciliation side/amount")
	}

	bad := v.Row(3, []string{"ram stores", "27AAPFU0939F1ZA", "12345", "abc", "X", "", "Kerala"})
	joined := strings.Join(bad.Errors, "|")
	for _, want := range []string{"duplicate party name (same as row 2)", "gstin:", "phone:", "opening_balance:", "dr_cr:", "does not match GSTIN state"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, bad.Errors)
		}
	}
	if bad.Record != nil {
		t.Error("invalid rows carry no record")
	}

	// Combined "Dr" suffix in the amount column, customer default.
	r := New(mapping.KindParties, idx("name", "opening_balance")).Row(4, []string{"Shyam", "2,000 Dr"})
	if r.Record.(PartyRecord).OpeningType != "dr" || r.Amount != 200000 {
		t.Fatalf("%+v", r)
	}
}

func TestItemRows(t *testing.T) {
	v := New(mapping.KindItems, idx("name", "hsn", "mrp", "sale_price", "purchase_price", "tax_rate", "barcode", "unit"))
	ok := v.Row(2, []string{"Parle-G 100g", "1905", "10", "10.00", "8.50", "5%", "8901719110016", "pcs"})
	if len(ok.Errors) != 0 {
		t.Fatalf("errors %v", ok.Errors)
	}
	it := ok.Record.(ItemRecord)
	if it.TaxRateBP != 500 || it.MRPPaise != 1000 || it.PurchasePricePaise != 850 || it.Unit != "PCS" {
		t.Fatalf("%+v", it)
	}
	bad := v.Row(3, []string{"Biscuit X", "19A5", "10", "12", "", "7", "8901719110016", ""})
	joined := strings.Join(bad.Errors, "|")
	for _, want := range []string{"hsn:", "above MRP", "not a GST slab", "duplicate barcode (same as row 2)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, bad.Errors)
		}
	}
}

func TestStockAndBalanceRows(t *testing.T) {
	v := New(mapping.KindOpeningStock, idx("item_ref", "qty", "unit_cost", "batch_no", "expiry_date"))
	r := v.Row(2, []string{"I001", "1,250.5", "8.50", "B1", "12/27"})
	if len(r.Errors) != 0 || r.Record.(StockRecord).ExpiryDate != "2027-12-31" || r.Record.(StockRecord).Qty != 1250.5 {
		t.Fatalf("%+v", r)
	}
	if d := v.Row(3, []string{"I001", "5", "", "B1", ""}); len(d.Errors) != 1 {
		t.Fatalf("same item+batch is a duplicate: %v", d.Errors)
	}
	b := New(mapping.KindOpeningBalances, idx("account_code", "amount")).Row(2, []string{"CASH", "(5,000)"})
	if b.Side != "cr" || b.Amount != 500000 {
		t.Fatalf("%+v", b)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"27": "27", "Tamil Nadu": "33", "27-Maharashtra": "27", "delhi": "07", "7": "07", "Atlantis": ""} {
		if got := StateCode(in); got != want {
			t.Errorf("StateCode(%q)=%q want %q", in, got, want)
		}
	}
	if _, err := Phone("+91 98765-43210"); err != nil {
		t.Error(err)
	}
	if HSN("19053100") != nil || HSN("190") == nil {
		t.Error("hsn")
	}
}

func TestRefOrName(t *testing.T) {
	if got := refOrName(" P-001 ", "Gupta Kirana"); got != "P-001" {
		t.Fatalf("code wins, got %q", got)
	}
	if got := refOrName("", "  Gupta   KIRANA "); got != "name:gupta kirana" {
		t.Fatalf("name fallback, got %q", got)
	}
	if refOrName("", "") != "" {
		t.Fatal("empty stays empty")
	}
}
