package validate

import (
	"strings"
	"testing"

	"github.com/maapan/aayaat/internal/mapping"
)

func idx(fields ...string) map[string]int {
	m := map[string]int{}
	for i, f := range fields {
		m[f] = i
	}
	return m
}

func has(t *testing.T, list []string, wants ...string) {
	t.Helper()
	joined := strings.Join(list, "|")
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in %v", w, list)
		}
	}
}

func TestPartyRows(t *testing.T) {
	v := New(mapping.KindParties, idx("name", "gstin", "phone", "opening_balance", "dr_cr", "type", "state"), Options{AsOf: "2026-04-01"})
	ok := v.Row(2, []string{"Ram  Stores", "27aapfu0939f1zv", "098765 43210", "1,200.50", "Cr", "Sundry Creditors", "Maharashtra"})
	if len(ok.Errors) != 0 {
		t.Fatalf("unexpected errors %v", ok.Errors)
	}
	p := ok.Record.(PartyRecord)
	if p.Name != "Ram Stores" || p.GSTIN != "27AAPFU0939F1ZV" || p.Phone != "+919876543210" || p.StateCode != "27" ||
		p.OpeningBalancePaise != 120050 || p.OpeningType != "cr" || p.Type != "supplier" || p.OpeningAsOf != "2026-04-01" ||
		p.PAN != "AAPFU0939F" {
		t.Fatalf("record %+v", p)
	}
	if ok.Side != "cr" || ok.Amount != 120050 {
		t.Fatal("reconciliation side/amount")
	}

	bad := v.Row(3, []string{"ram stores", "27AAPFU0939F1ZA", "12345", "abc", "X", "", "Kerala"})
	has(t, bad.Errors, "duplicate party name (same as row 2)", "gstin:", "opening_balance:", "dr_cr:", "does not match GSTIN state")
	// An unusable phone never fails the row any more: it is dropped with a warning.
	has(t, bad.Warnings, "phone")
	if strings.Contains(strings.Join(bad.Errors, "|"), "phone") {
		t.Errorf("phone must not be an error: %v", bad.Errors)
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

func TestPartyLandlineAndSharedGSTIN(t *testing.T) {
	v := New(mapping.KindParties, idx("name", "gstin", "phone"))
	a := v.Row(2, []string{"Sharma Agencies - Pune", "27AAKFS4821M1ZQ", "020-24451234"})
	b := v.Row(3, []string{"Sharma Agencies - Nashik", "27AAKFS4821M1ZQ", "0253 257 1234"})
	if len(a.Errors)+len(b.Errors) != 0 {
		t.Fatalf("branch ledgers sharing a GSTIN are valid: %v %v", a.Errors, b.Errors)
	}
	if a.Record.(PartyRecord).Phone != "+912024451234" || b.Record.(PartyRecord).Phone != "+912532571234" {
		t.Fatalf("landlines: %v %v", a.Record, b.Record)
	}
	has(t, b.Warnings, "also on row 2")
}

func TestPartyExtras(t *testing.T) {
	v := New(mapping.KindParties, idx("name", "pan", "email", "credit_limit", "credit_days", "address", "address2", "city", "pincode", "shipping_address", "phone"))
	r := v.Row(2, []string{"Gupta Kirana", "abcpg7310q", "Gupta@Example.in; other@x.in", "(50,000.00)", "30 Days", "Main Bazar", "",
		"Satara", "415001", "Godown 2, MIDC", "9890012345 / 02162-234567"})
	if len(r.Errors) != 0 {
		t.Fatal(r.Errors)
	}
	p := r.Record.(PartyRecord)
	if p.PAN != "ABCPG7310Q" || p.Email != "gupta@example.in" || p.CreditLimitPaise != 5000000 || p.CreditDays != 30 ||
		p.Address != "Main Bazar, Satara - 415001" || p.ShippingAddress != "Godown 2, MIDC" || p.Phone != "+919890012345" {
		t.Fatalf("%+v", p)
	}
	bad := v.Row(3, []string{"X", "ABC", "nope", "", "lots", "", "", "", "41100", "", ""})
	has(t, bad.Errors, "pan:", "credit_days:")
	has(t, bad.Warnings, "email", "pincode")
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
	has(t, bad.Errors, "hsn:", "not a GST slab", "duplicate barcode (same as row 2)")
	// Sale above MRP is capped with a warning (bhandar does the same), not rejected.
	w := v.Row(4, []string{"Biscuit Y", "1905", "10", "12", "", "5", "", "Nos"})
	if len(w.Errors) != 0 || w.Record.(ItemRecord).SalePricePaise != 1000 || w.Record.(ItemRecord).Unit != "PCS" {
		t.Fatalf("%+v", w)
	}
	has(t, w.Warnings, "above MRP")
}

func TestItemUnitsBatchCessOpening(t *testing.T) {
	v := New(mapping.KindItems, idx("name", "unit", "alt_unit", "alt_factor", "track_batches", "cess_rate", "category", "brand", "opening_qty", "opening_rate", "purchase_price", "store"),
		Options{StoreID: "11111111-1111-1111-1111-111111111111"})
	r := v.Row(2, []string{"Parle-G", "PCS", "BOX", "1 BOX = 48 PCS", "Yes", "", "Biscuits", "Parle", "1,248", "", "8.50", "Main Godown"})
	if len(r.Errors) != 0 {
		t.Fatal(r.Errors)
	}
	it := r.Record.(ItemRecord)
	if len(it.AltUnits) != 1 || it.AltUnits[0] != (AltUnit{"BOX", 48}) || !it.TrackBatches || it.Category != "Biscuits" || it.Brand != "Parle" ||
		it.OpeningQty != 1248 || it.OpeningCostPaise != 850 || it.StoreID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("%+v", it)
	}
	c := v.Row(3, []string{"Gold Flake", "PCS", "", "", "No", "5", "", "", "", "", "", ""})
	if c.Record.(ItemRecord).CessRateBP != 500 {
		t.Fatalf("%+v", c)
	}
	bad := v.Row(4, []string{"Soap", "PCS", "PCS", "12", "maybe", "", "", "", "", "", "", ""})
	has(t, bad.Errors, "alt_unit PCS is the base unit", "track_batches:")
	// No store: the item still imports, the opening quantity is dropped with a warning.
	ns := New(mapping.KindItems, idx("name", "opening_qty")).Row(2, []string{"Salt", "300"})
	if len(ns.Errors) != 0 || ns.Record.(ItemRecord).OpeningQty != 0 {
		t.Fatalf("%+v", ns)
	}
	has(t, ns.Warnings, "opening_qty ignored")
	for in, want := range map[string]float64{"48": 48, "12 PCS": 12, "2 BOX = 96 PCS": 48, "1 = 0.5": 0.5} {
		if f, err := Factor(in); err != nil || f != want {
			t.Errorf("Factor(%q) = %v %v", in, f, err)
		}
	}
}

func TestStockAndBalanceRows(t *testing.T) {
	store := "22222222-2222-2222-2222-222222222222"
	v := New(mapping.KindOpeningStock, idx("item_ref", "qty", "unit_cost", "batch_no", "expiry_date"), Options{StoreID: store})
	r := v.Row(2, []string{"I001", "1,250.5", "8.50", "B1", "12/27"})
	if len(r.Errors) != 0 || r.Record.(StockRecord).ExpiryDate != "2027-12-31" || r.Record.(StockRecord).Qty != 1250.5 || r.Record.(StockRecord).StoreID != store {
		t.Fatalf("%+v", r)
	}
	if d := v.Row(3, []string{"I001", "5", "", "b1", ""}); len(d.Errors) != 1 {
		t.Fatalf("same store + item + batch is a duplicate: %v", d.Errors)
	}
	if z := v.Row(4, []string{"I002", "0", "", "", ""}); !z.Skip || len(z.Errors) != 0 {
		t.Fatalf("zero qty is skipped: %+v", z)
	}
	if y := v.Row(5, []string{"I003", "4", "", "", "2027"}); len(y.Errors) != 1 || !strings.Contains(y.Errors[0], "only a year") {
		t.Fatalf("bare year expiry: %v", y.Errors)
	}
	nostore := New(mapping.KindOpeningStock, idx("item_ref", "qty")).Row(2, []string{"I001", "5"})
	has(t, nostore.Errors, "store is required")

	b := New(mapping.KindOpeningBalances, idx("account_code", "amount")).Row(2, []string{"CASH", "(5,000)"})
	if b.Side != "cr" || b.Amount != 500000 {
		t.Fatalf("%+v", b)
	}
}

func TestPerRowStore(t *testing.T) {
	main, counter := "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
	v := New(mapping.KindOpeningStock, idx("item_ref", "store", "qty"), Options{
		StoreMap:       map[string]string{"Main Godown": main, "Counter": counter},
		CanAccessStore: func(id string) bool { return id == main },
	})
	if r := v.Row(2, []string{"I1", "main godown", "5"}); r.Record.(StockRecord).StoreID != main {
		t.Fatalf("%+v", r)
	}
	has(t, v.Row(3, []string{"I1", "Counter", "5"}).Errors, "no access to store")
	has(t, v.Row(4, []string{"I1", "Cold Room", "5"}).Errors, `store "Cold Room" is not mapped`)
	// The same item+batch in two stores is not a duplicate.
	v2 := New(mapping.KindOpeningStock, idx("item_ref", "store", "qty"), Options{StoreMap: map[string]string{"A": main, "B": counter}})
	if a, b := v2.Row(2, []string{"I1", "A", "5"}), v2.Row(3, []string{"I1", "B", "5"}); len(a.Errors)+len(b.Errors) != 0 {
		t.Fatalf("%v %v", a.Errors, b.Errors)
	}
}

func TestLedgerBalances(t *testing.T) {
	v := New(mapping.KindOpeningBalances, idx("account_name", "group", "amount"),
		Options{AsOf: "2026-04-01", AccountMap: map[string]string{"Output CGST": "2210"}})
	cases := []struct {
		row          []string
		code, party  string
		skip, errors bool
	}{
		{[]string{"Cash", "Cash-in-Hand", "15000.00 Dr"}, "1100", "", false, false},
		{[]string{"HDFC Bank A/c 0042", "Bank Accounts", "250000.00 Dr"}, "1110", "", false, false},
		{[]string{"Proprietor's Capital", "Capital Account", "255950.00 Cr"}, "3100", "", false, false},
		{[]string{"Output CGST", "Duties & Taxes", "2600 Cr"}, "2210", "", false, false},
		{[]string{"Ram Traders", "Sundry Debtors", "100 Dr"}, "", "name:ram traders", false, false},
		{[]string{"Closing Stock", "Stock-in-Hand", "10000 Dr"}, "", "", true, false},
		{[]string{"Electricity Deposit", "Deposits (Asset)", "5000 Dr"}, "", "", false, true},
		{[]string{"Sales @ 18%", "Sales Accounts", ""}, "", "", true, false},                    // no balance: needs no mapping
		{[]string{"Rent Deposit", "Loans & Advances (Asset)", "1,000 Dr"}, "", "", false, true}, // unmapped
		{[]string{"ICICI Bank", "Bank Accounts", "100 Dr"}, "", "", false, true},                // second ledger on 1110
	}
	for i, c := range cases {
		r := v.Row(i+2, c.row)
		if (len(r.Errors) > 0) != c.errors || r.Skip != c.skip {
			t.Errorf("%v: errors=%v skip=%v", c.row, r.Errors, r.Skip)
			continue
		}
		if !c.errors && !c.skip {
			b := r.Record.(BalanceRecord)
			if b.AccountCode != c.code || b.PartyExternalRef != c.party {
				t.Errorf("%v → %+v", c.row, b)
			}
		}
	}
	z := New(mapping.KindOpeningBalances, idx("account_code", "amount")).Row(2, []string{"1100", "0"})
	if !z.Skip {
		t.Fatalf("zero balance is skipped: %+v", z)
	}
}

func TestBills(t *testing.T) {
	v := New(mapping.KindOpenBills, idx("party_ref", "party_name", "bill_no", "bill_date", "due_date", "credit_days", "amount", "dr_cr"),
		Options{AsOf: "2026-04-01"})
	r := v.Row(2, []string{"", "Sharma Agencies", "GT/1041", "10/02/2026", "", "30", "8,250.00", "Dr"})
	if len(r.Errors) != 0 {
		t.Fatal(r.Errors)
	}
	b := r.Record.(BillRecord)
	if b.PartyExternalRef != "name:sharma agencies" || b.DueDate != "2026-03-12" || b.AmountPaise != 825000 || b.Type != "dr" ||
		b.ExternalRef != "bill:name:sharma agencies/GT/1041" {
		t.Fatalf("%+v", b)
	}
	has(t, v.Row(3, []string{"", "Sharma Agencies", "gt/1041", "", "", "", "1", ""}).Errors, "duplicate bill")
	has(t, v.Row(4, []string{"C1", "", "B2", "10/03/2026", "01/03/2026", "", "5", ""}).Errors, "due_date is before bill_date")
	if s := v.Row(5, []string{"C1", "", "B3", "", "", "", "0.00", ""}); !s.Skip {
		t.Fatalf("settled bill is skipped: %+v", s)
	}
	has(t, v.Row(6, []string{"C1", "", "B4", "05/04/2026", "", "", "10", ""}).Warnings, "after the opening date")
	// Payables file: side from params.
	p := New(mapping.KindOpenBills, idx("party_name", "bill_no", "amount"), Options{Side: "cr"}).Row(2, []string{"Patel", "PD/771", "18,900"})
	if p.Record.(BillRecord).Type != "cr" || p.Side != "cr" {
		t.Fatalf("%+v", p)
	}
}

func TestInvoices(t *testing.T) {
	v := New(mapping.KindInvoices, idx("date", "number", "party_name", "party_gstin", "taxable", "cgst", "sgst", "igst", "cess", "total"),
		Options{DocType: "sale"})
	r := v.Row(2, []string{"01/04/2026", "GT/0001", "Sharma Agencies", "27AAKFS4821M1ZQ", "10,000.00", "900", "900", "0", "0", "11,800.00"})
	if len(r.Errors) != 0 {
		t.Fatal(r.Errors)
	}
	in := r.Record.(InvoiceRecord)
	if in.DocType != "sale" || in.TotalPaise != 1180000 || in.PlaceOfSupply != "27" || in.PartyExternalRef != "name:sharma agencies" ||
		in.ExternalRef != "inv:sale:GT/0001:2026-04-01" {
		t.Fatalf("%+v", in)
	}
	// Register without a round-off column: the paise difference becomes round-off.
	cash := v.Row(3, []string{"01/04/2026", "GT/0002", "Cash", "", "423.73", "10.59", "10.59", "0", "0", "445.00"})
	if len(cash.Errors) != 0 || cash.Record.(InvoiceRecord).RoundOffPaise != 9 || cash.Record.(InvoiceRecord).PartyExternalRef != "" {
		t.Fatalf("%+v", cash)
	}
	has(t, v.Row(4, []string{"03/04/2026", "GT/0004", "Gupta", "", "1,000", "25", "25", "0", "0", "1,200"}).Errors, "but total is")
	has(t, v.Row(5, []string{"03/04/2026", "GT/0005", "R", "", "2000", "180", "180", "360", "0", "2720"}).Errors, "either IGST or CGST")
	has(t, v.Row(6, []string{"01/04/2026", "GT/0001", "Sharma Agencies", "", "10000", "900", "900", "0", "0", "11800"}).Errors, "duplicate invoice")
	// Credit notes exported negative are stored as magnitudes.
	cn := New(mapping.KindInvoices, idx("doc_type", "date", "number", "taxable", "cgst", "sgst", "total")).
		Row(2, []string{"Credit Note", "05/04/2026", "CN/1", "-1000", "-90", "-90", "-1180"})
	if len(cn.Errors) != 0 || cn.Record.(InvoiceRecord).DocType != "sale_return" || cn.Record.(InvoiceRecord).TotalPaise != 118000 {
		t.Fatalf("%+v", cn)
	}
	for in, want := range map[string]string{"Sales": "sale", "Tax Invoice": "sale", "Purchase": "purchase", "Debit Note": "purchase_return",
		"Sales Return": "sale_return", "Receipt": ""} {
		if got := DocType(in); got != want {
			t.Errorf("DocType(%q) = %q want %q", in, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"27": "27", "Tamil Nadu": "33", "27-Maharashtra": "27", "delhi": "07", "7": "07", "Atlantis": ""} {
		if got := StateCode(in); got != want {
			t.Errorf("StateCode(%q)=%q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"+91 98765-43210": "+919876543210", "022-2345 6789": "+912223456789",
		"02223456789": "+912223456789", "0091 98765 43210": "+919876543210"} {
		if got, err := Phone(in); err != nil || got != want {
			t.Errorf("Phone(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"12345", "23456789", "1234567890"} {
		if _, err := Phone(bad); err == nil {
			t.Errorf("Phone(%q) should fail", bad)
		}
	}
	if p, w := Phones("02162-234567, 9890012345"); p != "+919890012345" || w != "" {
		t.Errorf("mobile preferred: %q %q", p, w)
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
