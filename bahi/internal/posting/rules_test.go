package posting

import (
	"testing"
	"time"

	"github.com/taraju/mool/events"
)

func sum(es []Entry, key string) (dr, cr int64) {
	for _, e := range es {
		k := e.AccountCode
		if e.PartyID != "" {
			k = "party:" + e.PartyID
		}
		if k == key {
			dr += e.Debit
			cr += e.Credit
		}
	}
	return
}

func baseInvoice(doc string) events.InvoicePayload {
	// 1000.00 taxable, 18% intra-state, round-off +0.40 → 1180.40 ... use clean numbers:
	return events.InvoicePayload{
		InvoiceID: "inv-1", StoreID: "st-1", DocType: doc, Number: "INV/26-27/00001", InvoiceDate: "2026-10-17",
		PartyName: "Ramesh", TaxablePaise: 100000, CGSTPaise: 9000, SGSTPaise: 9000, RoundOffPaise: 0,
		GrandTotalPaise: 118000,
	}
}

func TestCashSale(t *testing.T) {
	p := baseInvoice("sale")
	p.Tenders = []events.Tender{{Mode: "cash", AmountPaise: 120000}} // 20 rupees change
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != VSales || v.Total() != 118000 {
		t.Fatalf("type/total: %s %d", v.Type, v.Total())
	}
	if dr, _ := sum(v.Entries, Cash); dr != 118000 {
		t.Fatalf("cash debit = %d, change must not be booked", dr)
	}
	if _, cr := sum(v.Entries, Sales); cr != 100000 {
		t.Fatalf("sales credit = %d", cr)
	}
	if _, cr := sum(v.Entries, OutputCGST); cr != 9000 {
		t.Fatal("cgst")
	}
}

func TestCreditSaleSplitTenderWithRoundOff(t *testing.T) {
	p := baseInvoice("sale")
	p.PartyID = "p-1"
	p.TaxablePaise, p.CGSTPaise, p.SGSTPaise = 9999, 600, 600 // 111.99
	p.RoundOffPaise, p.GrandTotalPaise = 1, 11200
	p.Tenders = []events.Tender{{Mode: "upi", AmountPaise: 5000}, {Mode: "credit", AmountPaise: 6200}}
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, Bank); dr != 5000 {
		t.Fatal("upi goes to bank")
	}
	if dr, _ := sum(v.Entries, "party:p-1"); dr != 6200 {
		t.Fatal("balance on party")
	}
	if _, cr := sum(v.Entries, RoundOff); cr != 1 {
		t.Fatal("round off credit")
	}
}

func TestNegativeRoundOffIsDebit(t *testing.T) {
	p := baseInvoice("sale")
	p.TaxablePaise, p.CGSTPaise, p.SGSTPaise = 10040, 0, 0
	p.RoundOffPaise, p.GrandTotalPaise = -40, 10000
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, RoundOff); dr != 40 {
		t.Fatalf("negative round-off must be a debit: %+v", v.Entries)
	}
	if dr, _ := sum(v.Entries, WalkInCustomers); dr != 10000 {
		t.Fatal("unpaid walk-in goes to Walk-in Customers")
	}
}

func TestPurchaseIsMirroredWithInputTax(t *testing.T) {
	p := baseInvoice("purchase")
	p.PartyID = "sup-1"
	p.IGSTPaise, p.CGSTPaise, p.SGSTPaise = 18000, 0, 0
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, Purchases); dr != 100000 {
		t.Fatal("purchases debit")
	}
	if dr, _ := sum(v.Entries, InputIGST); dr != 18000 {
		t.Fatal("input igst debit")
	}
	if _, cr := sum(v.Entries, "party:sup-1"); cr != 118000 {
		t.Fatal("supplier credit")
	}
}

func TestReturns(t *testing.T) {
	cn := baseInvoice("sale_return")
	cn.PartyID = "p-1"
	v, err := Invoice(cn)
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != VCreditNote {
		t.Fatal("type")
	}
	if dr, _ := sum(v.Entries, SalesReturns); dr != 100000 {
		t.Fatal("sales returns debit")
	}
	if dr, _ := sum(v.Entries, OutputCGST); dr != 9000 {
		t.Fatal("output tax reversed")
	}
	if _, cr := sum(v.Entries, "party:p-1"); cr != 118000 {
		t.Fatal("party credited")
	}

	dn := baseInvoice("purchase_return")
	dn.PartyID = "sup-1"
	v, err = Invoice(dn)
	if err != nil {
		t.Fatal(err)
	}
	if _, cr := sum(v.Entries, PurchaseReturns); cr != 100000 {
		t.Fatal("purchase returns credit")
	}
	if dr, _ := sum(v.Entries, "party:sup-1"); dr != 118000 {
		t.Fatal("supplier debited")
	}
}

func TestInconsistentInvoiceRejected(t *testing.T) {
	p := baseInvoice("sale")
	p.GrandTotalPaise = 118001
	if _, err := Invoice(p); err == nil {
		t.Fatal("unbalanced payload must be rejected")
	}
}

func TestPaymentAndRefund(t *testing.T) {
	pl := events.PaymentPayload{PaymentID: "pay-1", AmountPaise: 5000, Method: "upi", GatewayRef: "pay_X"}
	v, err := Payment(pl, "p-1", "st", "2026-10-17", false)
	if err != nil || v.Type != VReceipt {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, Bank); dr != 5000 {
		t.Fatal("bank debit")
	}
	r, err := Payment(pl, "p-1", "st", "2026-10-17", true)
	if err != nil || r.Type != VPayment || r.Source != SrcRefund {
		t.Fatal(err)
	}
	if _, cr := sum(r.Entries, Bank); cr != 5000 {
		t.Fatal("refund credits bank")
	}
}

func TestProduction(t *testing.T) {
	p := events.ProductionCompletedPayload{
		ProductionRunID: "run-1",
		Consumed:        []events.ProductionLine{{Qty: 2, UnitCostPaise: 1000}, {Qty: 0.5, UnitCostPaise: 2000}},
		TotalCostPaise:  3500, // 3000 material + 500 overhead
	}
	v, err := Production(p, "2026-10-17")
	if err != nil {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, StockFinished); dr != 3500 {
		t.Fatal("fg")
	}
	if _, cr := sum(v.Entries, StockRaw); cr != 3000 {
		t.Fatal("rm")
	}
	if _, cr := sum(v.Entries, WorkInProgress); cr != 500 {
		t.Fatal("wip absorbs overhead")
	}
}

func TestValidateManual(t *testing.T) {
	bad := Voucher{Entries: []Entry{{AccountCode: Cash, Debit: 100}, {AccountCode: Bank, Credit: 90}}}
	if bad.Validate() == nil {
		t.Fatal("unbalanced must fail")
	}
	both := Voucher{Entries: []Entry{{AccountCode: Cash, Debit: 100, Credit: 100}, {AccountCode: Bank, Credit: 0}}}
	if both.Validate() == nil {
		t.Fatal("both sides must fail")
	}
	ok := Voucher{Entries: []Entry{{AccountID: "a", Debit: 100}, {PartyID: "p", Credit: 100}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOpening(t *testing.T) {
	v, err := Opening(Entry{PartyID: "p"}, 5000, "dr", "2026-04-01", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, cr := sum(v.Entries, OpeningDiff); cr != 5000 {
		t.Fatal("opening diff credit")
	}
	if _, err := Opening(Entry{PartyID: "p"}, 5000, "xx", "2026-04-01", "x"); err == nil {
		t.Fatal("bad type")
	}
}

func TestChartParentsFirst(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range DefaultChart {
		if a.Parent != "" && !seen[a.Parent] {
			t.Fatalf("%s listed before parent %s", a.Code, a.Parent)
		}
		if seen[a.Code] {
			t.Fatalf("duplicate %s", a.Code)
		}
		seen[a.Code] = true
	}
}

func TestAgingFIFO(t *testing.T) {
	asOf := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	d := func(daysAgo int) time.Time { return asOf.AddDate(0, 0, -daysAgo) }
	b := Age([]Movement{
		{Date: d(100), Amount: 1000}, // fully settled by FIFO
		{Date: d(45), Amount: 2000},  // 500 settled, 1500 open
		{Date: d(10), Amount: 700},
		{Date: d(5), Amount: -1500},
	}, asOf)
	if b.Over90 != 0 || b.D31to60 != 1500 || b.D0to30 != 700 || b.Total != 2200 || b.Advance != 0 {
		t.Fatalf("buckets %+v", b)
	}
	adv := Age([]Movement{{Date: d(1), Amount: 100}, {Date: d(1), Amount: -300}}, asOf)
	if adv.Total != 0 || adv.Advance != 200 {
		t.Fatalf("advance %+v", adv)
	}
}
