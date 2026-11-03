package posting

import (
	"strings"
	"testing"
	"time"

	"github.com/maapan/mool/events"
)

func TestComputeTDS(t *testing.T) {
	q := Sections[Sec194Q]
	c := Sections[Sec194C]
	jb, _ := LookupSection("194j")
	tcs := Sections[Sec206C1H]
	lower := 50
	cases := []struct {
		name    string
		in      TDSInput
		taxable int64
		rate    int
		tds     int64
		higher  bool
	}{
		{"194Q below ₹50 lakh", TDSInput{Section: q, PAN: "AAACB1234C", AmountPaise: 40_00_000_00, Date: "2026-06-01"}, 0, 0, 0, false},
		{"194Q crossing: only the excess", TDSInput{Section: q, PAN: "AAACB1234C", AmountPaise: 2_00_000_00, PriorPaise: 49_00_000_00,
			Date: "2026-06-01"}, 1_00_000_00, 10, 100_00, false},
		{"194Q above: whole bill", TDSInput{Section: q, PAN: "AAACB1234C", AmountPaise: 1_00_000_00, PriorPaise: 60_00_000_00,
			Date: "2026-06-01"}, 1_00_000_00, 10, 100_00, false},
		{"194Q no PAN: 5 %", TDSInput{Section: q, AmountPaise: 1_00_000_00, PriorPaise: 60_00_000_00, Date: "2026-06-01"},
			1_00_000_00, 500, 5_000_00, true},
		{"194C single within ₹30,000", TDSInput{Section: c, PAN: "AAACB1234C", AmountPaise: 25_000_00, Date: "2026-06-01"}, 0, 0, 0, false},
		{"194C single above: company 2 %", TDSInput{Section: c, PAN: "AAACB1234C", AmountPaise: 40_000_00, Date: "2026-06-01"},
			40_000_00, 200, 800_00, false},
		{"194C individual 1 %", TDSInput{Section: c, PAN: "ABCPE1234F", AmountPaise: 40_000_00, Date: "2026-06-01"},
			40_000_00, 100, 400_00, false},
		{"194C aggregate crossed: catch-up", TDSInput{Section: c, PAN: "ABCPE1234F", AmountPaise: 25_000_00, PriorPaise: 80_000_00,
			Date: "2026-06-01"}, 1_05_000_00, 100, 1_050_00, false},
		{"194C after crossing: only this bill", TDSInput{Section: c, PAN: "ABCPE1234F", AmountPaise: 10_000_00, PriorPaise: 1_05_000_00,
			PriorTaxed: 1_05_000_00, Date: "2026-06-01"}, 10_000_00, 100, 100_00, false},
		{"194C no PAN 20 %", TDSInput{Section: c, AmountPaise: 40_000_00, Date: "2026-06-01"}, 40_000_00, 2000, 8_000_00, true},
		{"194J(b) below ₹50,000", TDSInput{Section: jb, PAN: "ABCPE1234F", AmountPaise: 45_000_00, Date: "2026-06-01"}, 0, 0, 0, false},
		{"194J(b) 10 %, rounded to the rupee", TDSInput{Section: jb, PAN: "ABCPE1234F", AmountPaise: 60_000_55, Date: "2026-06-01"},
			60_000_55, 1000, 6_000_00, false},
		{"lower deduction certificate", TDSInput{Section: jb, PAN: "ABCPE1234F", AmountPaise: 60_000_00, Date: "2026-06-01", LowerRateBP: &lower},
			60_000_00, 50, 300_00, false},
		{"206C(1H) omitted after March 2025", TDSInput{Section: tcs, PAN: "ABCPE1234F", AmountPaise: 10_00_000_00, PriorPaise: 60_00_000_00,
			Date: "2026-06-01"}, 0, 0, 0, false},
		{"206C(1H) before omission", TDSInput{Section: tcs, PAN: "ABCPE1234F", AmountPaise: 10_00_000_00, PriorPaise: 45_00_000_00,
			Date: "2025-01-10"}, 5_00_000_00, 10, 50_000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ComputeTDS(tc.in)
			if r.TaxablePaise != tc.taxable || r.RateBP != tc.rate || r.TDSPaise != tc.tds || r.HigherRate != tc.higher {
				t.Fatalf("got taxable %d rate %d tds %d higher %v (%s)", r.TaxablePaise, r.RateBP, r.TDSPaise, r.HigherRate, r.Reason)
			}
			if r.Applicable != (tc.tds > 0) {
				t.Fatalf("applicable %v", r.Applicable)
			}
		})
	}
}

func TestPAN(t *testing.T) {
	for pan, want := range map[string]string{"AAACB1234C": "company", "ABCPE1234F": "individual", "AAAHA1234A": "huf",
		"AAAFA1234A": "firm", "AAATA1234A": "other", "ABC": "other", "1BCPE1234F": "other"} {
		if got := DeducteeType(pan); got != want {
			t.Errorf("%s: %s, want %s", pan, got, want)
		}
	}
	if ValidPAN("ABCPE12345") || !ValidPAN("ABCPE1234F") {
		t.Fatal("ValidPAN")
	}
	if _, ok := LookupSection("194 J"); !ok {
		t.Fatal("194J alias")
	}
}

func TestSetOffVoucher(t *testing.T) {
	v, ok, err := SetOff(SetOffInput{GSTIN: "27aapfu0939f1zv", Period: "102026", Date: "2026-11-20",
		Utilisation: []Utilisation{{From: "igst", To: "igst", AmountPaise: 5000}, {From: "IGST", To: "cgst", AmountPaise: 2000},
			{From: "cgst", To: "cgst", AmountPaise: 1000}},
		Cash: map[string]int64{"cgst": 500, "sgst": 3000}})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if v.SourceID != "27AAPFU0939F1ZV/102026" || v.Source != SrcGSTSetOff {
		t.Fatalf("key %s %s", v.Source, v.SourceID)
	}
	sum := map[string]int64{}
	for _, e := range v.Entries {
		sum[e.AccountCode] += e.Debit - e.Credit
	}
	want := map[string]int64{OutputIGST: 5000, InputIGST: -7000, OutputCGST: 3500, InputCGST: -1000, ElectronicCashCGST: -500,
		OutputSGST: 3000, ElectronicCashSGST: -3000}
	for k, w := range want {
		if sum[k] != w {
			t.Errorf("%s = %d, want %d", k, sum[k], w)
		}
	}
	if _, _, err := SetOff(SetOffInput{Period: "102026", Utilisation: []Utilisation{{From: "cgst", To: "sgst", AmountPaise: 1}}}); err == nil {
		t.Fatal("CGST credit must not pay SGST (Sec 49(5))")
	}
	if _, _, err := SetOff(SetOffInput{Period: "102026", Utilisation: []Utilisation{{From: "cess", To: "igst", AmountPaise: 1}}}); err == nil {
		t.Fatal("cess credit pays only cess")
	}
	if _, ok, _ := SetOff(SetOffInput{Period: "102026"}); ok {
		t.Fatal("empty set-off posts nothing")
	}
	cv, ok, err := GSTCashDeposit("27AAPFU0939F1ZV", "102026", "2026-11-18", "bank-id", map[string]int64{"cgst": 500, "sgst": 3000})
	if err != nil || !ok || cv.Total() != 3500 || cv.Entries[len(cv.Entries)-1].AccountID != "bank-id" {
		t.Fatalf("cash deposit %+v %v %v", cv, ok, err)
	}
}

func TestAgeByDue(t *testing.T) {
	asOf := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, time.UTC) }
	bk := AgeByDue([]OpenBill{
		{DueDate: d(11, 5), Outstanding: 100},  // not due
		{DueDate: d(10, 23), Outstanding: 200}, // due today
		{DueDate: d(9, 1), Outstanding: 300},   // 52 days
		{DueDate: d(7, 1), Outstanding: 400},   // 114 days
	}, -150, asOf)
	if bk.NotDue != 100 || bk.D0to30 != 200 || bk.D31to60 != 300 || bk.Over90 != 400 || bk.Total != 1000 || bk.OnAccount != 150 || bk.Net != 850 {
		t.Fatalf("%+v", bk)
	}
	if bk.Oldest != "2026-07-01" {
		t.Fatalf("oldest %s", bk.Oldest)
	}
}

func TestAutoMatch(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC) }
	lines := []StmtLine{
		{ID: "L1", Date: d(12), Amount: -500000, Text: "CHQ PAID 000777 SHARMA"},
		{ID: "L2", Date: d(10), Amount: 1000000, Text: "NEFT-HDFCR52026101012345-RAMESH TRADERS"},
		{ID: "L3", Date: d(15), Amount: -5900, Text: "SMS CHARGES"},
		{ID: "L4", Date: d(20), Amount: 20000, Text: "CASH DEP"},
		{ID: "L5", Date: d(20), Amount: 20000, Text: "CASH DEP"},
	}
	books := []BookEntry{
		{ID: "E1", Date: d(5), Amount: -500000, Refs: []string{"PY/26-27/00001", "Cheque 777"}},
		{ID: "E2", Date: d(9), Amount: 1000000, Refs: []string{"RV/26-27/00004", "HDFCR52026101012345"}},
		{ID: "E3", Date: d(9), Amount: 1000000, Refs: []string{"RV/26-27/00005"}}, // same amount, no reference
		{ID: "E4", Date: d(19), Amount: 20000},
		{ID: "E5", Date: d(1), Amount: -5900}, // outside the window of L3
	}
	ms := AutoMatch(lines, books, DefaultWindow)
	got := map[string]string{}
	for _, m := range ms {
		got[m.LineID] = m.EntryID + "/" + m.Rule
	}
	if got["L1"] != "E1/reference" {
		t.Errorf("cheque 000777 ↔ cheque 777: %q", got["L1"])
	}
	if got["L2"] != "E2/reference" {
		t.Errorf("UTR inside the narration: %q", got["L2"])
	}
	if _, ok := got["L3"]; ok {
		t.Error("L3 has no candidate inside the window")
	}
	if _, ok := got["L4"]; ok || got["L5"] != "" {
		t.Errorf("two identical deposits for one entry are ambiguous: %v", got)
	}
	// A unique amount + date pairing matches without a reference.
	ms = AutoMatch([]StmtLine{{ID: "X", Date: d(11), Amount: 777}}, []BookEntry{{ID: "Y", Date: d(10), Amount: 777}}, DefaultWindow)
	if len(ms) != 1 || ms[0].Rule != "amount_date" {
		t.Fatalf("%+v", ms)
	}
}

func TestInvoiceWithTCSBalances(t *testing.T) {
	p := events.InvoicePayload{InvoiceID: "i1", DocType: "sale", Number: "INV/1", InvoiceDate: "2026-10-01", PartyID: "p1",
		TaxablePaise: 100000, CGSTPaise: 9000, SGSTPaise: 9000, TCSPaise: 118, GrandTotalPaise: 118118, DueDate: "2026-11-15"}
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	var tcs int64
	for _, e := range v.Entries {
		if e.AccountCode == TCSPayable {
			tcs = e.Credit
		}
	}
	if tcs != 118 || v.DueDate != "2026-11-15" {
		t.Fatalf("tcs %d due %s", tcs, v.DueDate)
	}
	p.DocType = "purchase"
	v, err = Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range v.Entries {
		if e.AccountCode == TCSReceivable && e.Debit != 118 {
			t.Fatalf("purchase TCS %+v", e)
		}
	}
	p.DocType, p.RefInvoiceID = "sale_return", "i0"
	v, _ = Invoice(p)
	if v.AgainstSource != SrcInvoice || v.AgainstSourceID != "i0" || v.DueDate != "" {
		t.Fatalf("return against %s/%s", v.AgainstSource, v.AgainstSourceID)
	}
	pay, _ := Payment(events.PaymentPayload{PaymentID: "x", InvoiceID: "i1", AmountPaise: 5, Method: "cash"}, "p1", "", "2026-10-02", false)
	if pay.AgainstSourceID != "i1" {
		t.Fatal("payment against its invoice")
	}
	ref, _ := Payment(events.PaymentPayload{PaymentID: "x", RefundID: "r", InvoiceID: "i1", AmountPaise: 5, Method: "cash"}, "p1", "", "2026-10-02", true)
	if ref.AgainstSourceID != "" {
		t.Fatal("a refund is not allocated")
	}
	if !strings.HasPrefix(ref.Narration, "Refund") {
		t.Fatal(ref.Narration)
	}
}
