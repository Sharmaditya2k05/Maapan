package mapping

import "testing"

func TestSuggestMargBooksParties(t *testing.T) {
	headers := []string{"S.No.", "Party Name", "GSTIN No.", "Mobile No", "Address", "State", "Opening Bal", "Dr/Cr", "Group"}
	m := Suggest(KindParties, headers)
	want := map[string]string{
		"name": "Party Name", "gstin": "GSTIN No.", "phone": "Mobile No", "address": "Address",
		"state": "State", "opening_balance": "Opening Bal", "dr_cr": "Dr/Cr", "type": "Group",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s → %q, want %q (all: %v)", k, m[k], v, m)
		}
	}
	if _, ok := m["code"]; ok {
		t.Errorf("S.No. must not map to code: %v", m)
	}
}

func TestSuggestItemsFuzzy(t *testing.T) {
	headers := []string{"Item Name", "HSN Code", "MRP", "Sale Rate", "Purc Rate", "GST %", "Unit", "Barcode", "Min Stock"}
	m := Suggest(KindItems, headers)
	want := map[string]string{
		"name": "Item Name", "hsn": "HSN Code", "mrp": "MRP", "sale_price": "Sale Rate", "purchase_price": "Purc Rate",
		"tax_rate": "GST %", "unit": "Unit", "barcode": "Barcode", "reorder_level": "Min Stock",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s → %q, want %q (all: %v)", k, m[k], v, m)
		}
	}
	// Typos still match.
	if got := Suggest(KindItems, []string{"Itme Name", "Purchse Rate"}); got["name"] != "Itme Name" || got["purchase_price"] != "Purchse Rate" {
		t.Errorf("fuzzy: %v", got)
	}
}

func TestDetectHeaderRowSkipsTitle(t *testing.T) {
	rows := [][]string{
		{"SHARMA TRADERS"},
		{"Party Wise Outstanding as on 31/03/2026"},
		{},
		{"Party Name", "GSTIN No", "Opening Bal", "Dr/Cr"},
		{"Ram Stores", "27AAPFU0939F1ZV", "1200", "Dr"},
	}
	if got := DetectHeaderRow(KindParties, rows); got != 4 {
		t.Fatalf("header row = %d", got)
	}
	if got := DetectHeaderRow(KindParties, [][]string{{}, {"foo", "bar"}}); got != 2 {
		t.Fatalf("fallback = %d", got)
	}
}

func TestResolveAndRequired(t *testing.T) {
	idx, missing := Resolve(map[string]string{"name": "party name", "gstin": "Nope"}, []string{"Party Name", "GSTIN"})
	if idx["name"] != 0 || len(missing) != 1 || missing[0] != "Nope" {
		t.Fatalf("%v %v", idx, missing)
	}
	if req := MissingRequired(KindOpeningStock, map[string]string{"item_ref": "Code"}); len(req) != 1 || req[0] != "qty" {
		t.Fatalf("%v", req)
	}
}

// The CSV template for each kind must map straight back onto its own fields.
func TestTemplateLabelsRoundTrip(t *testing.T) {
	for _, kind := range Kinds {
		var labels []string
		for _, f := range Fields[kind] {
			labels = append(labels, f.Label)
		}
		got := Suggest(kind, labels)
		for _, f := range Fields[kind] {
			if got[f.Key] != f.Label {
				t.Errorf("%s: %s → %q, want %q", kind, f.Key, got[f.Key], f.Label)
			}
		}
	}
}

func TestSuggestMargRegisters(t *testing.T) {
	m := Suggest(KindOpenBills, []string{"Party Code", "Party Name", "Bill No", "Bill Date", "Due Date", "Bill Amt", "Pending Amt", "Dr/Cr", "Days"})
	for k, v := range map[string]string{"party_ref": "Party Code", "party_name": "Party Name", "bill_no": "Bill No", "bill_amount": "Bill Amt", "amount": "Pending Amt"} {
		if m[k] != v {
			t.Errorf("bills %s → %q want %q (%v)", k, m[k], v, m)
		}
	}
	m = Suggest(KindInvoices, []string{"Date", "Bill No", "Party Name", "GSTIN", "Taxable Amt", "CGST Amt", "SGST Amt", "IGST Amt", "Cess", "R/Off", "Net Amt"})
	for k, v := range map[string]string{"date": "Date", "number": "Bill No", "party_gstin": "GSTIN", "taxable": "Taxable Amt", "cgst": "CGST Amt",
		"round_off": "R/Off", "total": "Net Amt"} {
		if m[k] != v {
			t.Errorf("invoices %s → %q want %q (%v)", k, m[k], v, m)
		}
	}
}
