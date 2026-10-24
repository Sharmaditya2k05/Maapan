package recon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNormaliseDocNo(t *testing.T) {
	for in, want := range map[string]string{
		"inv/26-27/0042": "INV262742",
		"INV 2627-42":    "INV262742",
		"ST/0042":        "ST42",
		"st-42":          "ST42",
		"GTA-118":        "GTA118",
		"GTA 118":        "GTA118",
		"A001B007":       "A1B7",
		"0000":           "0",
		" 12/00/3 ":      "1203",
		"BE/26-27/0901":  "BE2627901",
		"BE/26-27/901":   "BE2627901",
		"":               "",
	} {
		if got := NormaliseDocNo(in); got != want {
			t.Errorf("NormaliseDocNo(%q) = %q, want %q", in, got, want)
		}
	}
	if k := DocKey("29aaacb1234c1z5", DocInvoice, "BE/26-27/0901", "2026-10-09"); k != "29AAACB1234C1Z5_INV_BE2627901_2627" {
		t.Errorf("doc key %s", k)
	}
	if k := DocKey("X", DocCreditNote, "1", "2027-03-31"); !strings.HasSuffix(k, "_2627") {
		t.Errorf("FY of March: %s", k)
	}
}

func TestParseRupees(t *testing.T) {
	for in, want := range map[string]Rs{"1234.5": 123450, "1,234.56": 123456, "-0.05": -5, "12.345": 1235, "12": 1200,
		"1e3": 100000, "": 0, ".5": 50, "0.004": 0} {
		got, err := ParseRupees(in)
		if err != nil || got != want {
			t.Errorf("ParseRupees(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseRupees("abc"); err == nil {
		t.Error("abc must fail")
	}
	var v struct{ A, B Rs }
	if err := json.Unmarshal([]byte(`{"A": 1640.00, "B": "70.5"}`), &v); err != nil || v.A != 164000 || v.B != 7050 {
		t.Errorf("unmarshal %+v %v", v, err)
	}
}

func loadSample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/gstr2b_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseGSTR2B(t *testing.T) {
	st, err := ParseGSTR2B(loadSample(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.GSTIN != "27AAPFU0939F1ZV" || st.Period != "102026" || st.GeneratedOn != "2026-11-14" || len(st.Docs) != 5 {
		t.Fatalf("statement %+v", st)
	}
	if len(st.Skipped) != 1 || st.Skipped[0] != "impg" {
		t.Errorf("skipped %v", st.Skipped)
	}
	d := st.Docs[0]
	if d.Section != "b2b" || d.DocType != DocInvoice || d.DocNo != "ST/0042" || d.DocDate != "2026-10-05" || d.Taxable != 150000 ||
		d.CGST != 7000 || d.Value != 164000 || d.SupplierName != "Sharma Traders" || d.SupplierFiledOn != "2026-11-11" || d.ITCAvailable != "Y" {
		t.Errorf("first doc %+v", d)
	}
	if !st.Docs[1].ReverseCharge {
		t.Error("GTA invoice is reverse charge")
	}
	cn := st.Docs[4]
	if cn.Section != "cdnr" || cn.DocType != DocCreditNote || cn.DocNo != "BE-CN-17" || cn.SupplyType != "R" || cn.IGST != 18000 {
		t.Errorf("credit note %+v", cn)
	}
	if st.Docs[3].ITCAvailable != "N" || st.Docs[3].Reason != "P" {
		t.Errorf("itc not available doc %+v", st.Docs[3])
	}

	// Without the data wrapper and with missing sections.
	bare := `{"gstin":"27AAPFU0939F1ZV","rtnprd":"102026","docdata":{"cdnr":[{"ctin":"29AAACB1234C1Z5","nt":[{"ntnum":"D1","typ":"D","dt":"01-10-2026","txval":100,"igst":18}]}]}}`
	st, err = ParseGSTR2B([]byte(bare))
	if err != nil || len(st.Docs) != 1 || st.Docs[0].DocType != DocDebitNote || st.Docs[0].ITCAvailable != "Y" {
		t.Errorf("bare file %+v %v", st, err)
	}
	if st, err := ParseGSTR2B([]byte(`{"data":{"gstin":"27AAPFU0939F1ZV","rtnprd":"102026","docdata":{}}}`)); err != nil || len(st.Docs) != 0 {
		t.Errorf("empty docdata %+v %v", st, err)
	}
	for _, bad := range []string{`[]`, `{"foo":1}`, `not json`} {
		if _, err := ParseGSTR2B([]byte(bad)); err == nil {
			t.Errorf("%s should not parse", bad)
		}
	}
	// An amendment replaces the original document with the same key.
	amd := `{"gstin":"27AAPFU0939F1ZV","docdata":{
		"b2b":[{"ctin":"29AAACB1234C1Z5","inv":[{"inum":"A1","dt":"01-10-2026","txval":100}]}],
		"b2ba":[{"ctin":"29AAACB1234C1Z5","inv":[{"inum":"A1","dt":"01-10-2026","txval":90}]}]}}`
	if st, err := ParseGSTR2B([]byte(amd)); err != nil || len(st.Docs) != 1 || st.Docs[0].Taxable != 9000 || st.Docs[0].Section != "b2ba" {
		t.Errorf("amendment %+v %v", st.Docs, err)
	}
}

// books are the purchase bills matching the sample 2B.
func books() []Purchase {
	return []Purchase{
		{ID: "p1", DocType: "purchase", Number: "PUR/26-27/00001", InvoiceDate: "2026-10-06", SupplierGSTIN: "27AAPFU1111A1Z1",
			SupplierInvoiceNo: "st-42", SupplierInvoiceDate: "2026-10-04", Taxable: 150000, CGST: 7000, SGST: 7000},
		{ID: "p2", DocType: "purchase", Number: "PUR/26-27/00002", InvoiceDate: "2026-10-07", SupplierGSTIN: "27AABCG2222B1Z2",
			SupplierInvoiceNo: "GTA 118", SupplierInvoiceDate: "2026-10-07", ReverseCharge: true, Taxable: 200000, CGST: 5000, SGST: 5000},
		{ID: "p3", DocType: "purchase", Number: "PUR/26-27/00003", InvoiceDate: "2026-10-09", SupplierGSTIN: "29AAACB1234C1Z5",
			SupplierInvoiceNo: "BE/26-27/901", SupplierInvoiceDate: "2026-10-09", Taxable: 1000000, IGST: 180000},
		{ID: "p4", DocType: "purchase_return", Number: "DN/26-27/00001", InvoiceDate: "2026-10-21", SupplierGSTIN: "29AAACB1234C1Z5",
			Taxable: 100000, IGST: 18000},
		{ID: "p5", DocType: "purchase", Number: "PUR/26-27/00005", InvoiceDate: "2026-10-20", SupplierGSTIN: "27AAPFU3333C1Z3",
			SupplierInvoiceNo: "Z-9", SupplierInvoiceDate: "2026-10-20", Taxable: 50000, CGST: 1250, SGST: 1250},
		{ID: "p6", DocType: "purchase", Number: "PUR/26-27/00006", InvoiceDate: "2026-10-20", Taxable: 1000}, // unregistered supplier
		{ID: "p7", DocType: "purchase", Number: "PUR/26-27/00007", InvoiceDate: "2026-09-20", SupplierGSTIN: "27AAPFU4444D1Z4",
			SupplierInvoiceNo: "OLD-1", Taxable: 1000},
		{ID: "p8", DocType: "purchase", Number: "PUR/26-27/00008", InvoiceDate: "2026-10-02", SupplierGSTIN: "27AAPFU5555E1Z5",
			SupplierInvoiceNo: "PREV-7", Taxable: 1000},
	}
}

func TestMatch(t *testing.T) {
	st, err := ParseGSTR2B(loadSample(t))
	if err != nil {
		t.Fatal(err)
	}
	rs := Match(MatchInput{Docs: st.Docs, Purchases: books(), From: "2026-10-01", To: "2026-10-31",
		ReportedElsewhere: map[string]bool{"27AAPFU5555E1Z5|PREV7": true}})
	c := Counts(rs)
	if c[StatusMatched] != 3 || c[StatusMismatch] != 1 || c[StatusMissingInBooks] != 1 || c[StatusMissingIn2B] != 1 {
		t.Fatalf("counts %v\n%+v", c, rs)
	}
	by := map[string]Result{}
	for _, r := range rs {
		if r.Purchase != nil {
			by[r.Purchase.ID] = r
		} else {
			by[r.Doc.DocNo] = r
		}
	}
	if r := by["p1"]; r.Status != StatusMatched || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "date differs") {
		t.Errorf("p1 %+v", r)
	}
	if r := by["p2"]; r.Status != StatusMatched {
		t.Errorf("p2 RCM %+v", r)
	}
	if r := by["p3"]; r.Status != StatusMismatch || len(r.Diffs) != 1 || r.Diffs[0].Field != "igst" ||
		r.Diffs[0].Books != Rs(180000) || r.Diffs[0].GSTR2B != Rs(185000) {
		t.Errorf("p3 %+v", r)
	}
	if r := by["p4"]; r.Status != StatusMatched || r.Doc.DocNo != "BE-CN-17" || !strings.Contains(strings.Join(r.Notes, " "), "amounts") {
		t.Errorf("p4 credit note %+v", r)
	}
	if r := by["p5"]; r.Status != StatusMissingIn2B || r.Doc != nil {
		t.Errorf("p5 %+v", r)
	}
	if r := by["X-1"]; r.Status != StatusMissingInBooks {
		t.Errorf("X-1 %+v", r)
	}
	for _, id := range []string{"p6", "p7", "p8"} {
		if _, ok := by[id]; ok {
			t.Errorf("%s must not be listed", id)
		}
	}
	if rs[0].Status != StatusMismatch {
		t.Errorf("mismatches sort first: %s", rs[0].Status)
	}

	// Tolerance: ₹1 passes, ₹1.01 does not; a date in another FY is a mismatch.
	doc := Doc{Key: "k", SupplierGSTIN: "27AAPFU1111A1Z1", DocType: DocInvoice, DocNo: "T1", DocDate: "2026-03-31", Taxable: 10000, CGST: 900}
	p := Purchase{DocType: "purchase", SupplierGSTIN: "27AAPFU1111A1Z1", SupplierInvoiceNo: "t-1", SupplierInvoiceDate: "2026-03-31",
		InvoiceDate: "2026-04-02", Taxable: 10100, CGST: 900}
	if r := Match(MatchInput{Docs: []Doc{doc}, Purchases: []Purchase{p}})[0]; r.Status != StatusMatched {
		t.Errorf("₹1 tolerance %+v", r)
	}
	p.Taxable = 10101
	if r := Match(MatchInput{Docs: []Doc{doc}, Purchases: []Purchase{p}})[0]; r.Status != StatusMismatch {
		t.Errorf("beyond tolerance %+v", r)
	}
	p.Taxable, p.SupplierInvoiceDate = 10000, "2026-04-01"
	if r := Match(MatchInput{Docs: []Doc{doc}, Purchases: []Purchase{p}})[0]; r.Status != StatusMismatch || r.Diffs[0].Field != "date" {
		t.Errorf("other FY %+v", r)
	}
}

func TestITCSummaryAndIMSFile(t *testing.T) {
	st, _ := ParseGSTR2B(loadSample(t))
	k := func(i int) string { return st.Docs[i].Key }
	actions := map[string]Action{
		k(0): {Action: ActionAccept},
		k(2): {Action: ActionReject, Remark: "IGST overcharged"},
		k(4): {Action: ActionPending},
	}
	s := SummariseITC(st.Docs, actions)
	// Accepted ST/0042, deemed GTA-118 (no action), rejected BE invoice, pending CN, X-1 not available.
	if s.Accepted.Docs != 1 || s.DeemedAccepted.Docs != 1 || s.Rejected.Docs != 1 || s.Pending.Docs != 1 || s.NotAvailable.Docs != 1 {
		t.Fatalf("buckets %+v", s)
	}
	if s.ReverseCharge != (Heads{CGST: 5000, SGST: 5000}) || s.AllOther != (Heads{CGST: 7000, SGST: 7000}) ||
		s.Available != (Heads{CGST: 12000, SGST: 12000}) {
		t.Errorf("available %+v", s)
	}
	if s.Pending.IGST != -18000 {
		t.Errorf("credit note reduces ITC: %+v", s.Pending)
	}

	f := BuildIMSFile("27AAPFU0939F1ZV", "102026", st.Docs, actions)
	if f.Rtin != "27AAPFU0939F1ZV" || f.ReqTyp != "SAVE" || len(f.InvData.B2B) != 2 || len(f.InvData.CDN) != 1 {
		t.Fatalf("ims %+v", f)
	}
	codes := map[string]string{}
	for _, d := range f.InvData.B2B {
		codes[d.Inum] = d.Action
	}
	if codes["ST/0042"] != "A" || codes["BE/26-27/0901"] != "R" || f.InvData.CDN[0].Action != "P" || f.InvData.CDN[0].NtTyp != "C" ||
		f.InvData.CDN[0].NtDt != "20-10-2026" {
		t.Errorf("ims actions %+v", f.InvData)
	}
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), `"iamt":1850.00`) {
		t.Errorf("rupees in IMS file: %s", b)
	}
}
