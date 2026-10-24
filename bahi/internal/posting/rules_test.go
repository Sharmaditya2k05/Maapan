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

func TestReceiptRouting(t *testing.T) {
	cases := []struct{ gateway, method, want string }{
		{"razorpay", "upi", GatewayClearing}, {"cashfree", "card", GatewayClearing}, {"Razorpay", "cash", GatewayClearing},
		{"razorpay", "netbanking", GatewayClearing}, {"payu", "upi", GatewayClearing},
		{"manual", "cash", Cash}, {"", "cash", Cash}, {"", "Cash", Cash},
		{"manual", "cheque", ChequesInHand}, {"", "cheque", ChequesInHand},
		{"manual", "bank", Bank}, {"", "neft", Bank}, {"", "rtgs", Bank}, {"manual", "imps", Bank}, {"", "netbanking", Bank},
		{"manual", "upi", Bank}, {"", "card", Bank}, {"manual", "wallet", Bank}, {"", "", Bank},
	}
	for _, c := range cases {
		if got := ReceiptAccount(c.gateway, c.method); got != c.want {
			t.Errorf("ReceiptAccount(%q,%q) = %s, want %s", c.gateway, c.method, got, c.want)
		}
	}
	pl := events.PaymentPayload{PaymentID: "pay-1", AmountPaise: 5000, Method: "upi", Gateway: "razorpay", GatewayRef: "pay_X"}
	v, err := Payment(pl, "p-1", "st", "2026-10-17", false)
	if err != nil {
		t.Fatal(err)
	}
	if dr, _ := sum(v.Entries, GatewayClearing); dr != 5000 {
		t.Fatalf("gateway capture must debit Payment Gateway Clearing: %+v", v.Entries)
	}
	if dr, _ := sum(v.Entries, Bank); dr != 0 {
		t.Fatal("gateway capture must not touch Bank until the settlement")
	}
	if _, cr := sum(v.Entries, "party:p-1"); cr != 5000 {
		t.Fatal("party credited")
	}
	pl.Gateway, pl.Method = "manual", "cash"
	if v, _ = Payment(pl, "", "st", "2026-10-17", false); v.Entries[0].AccountCode != Cash {
		t.Fatalf("manual cash receipt goes to Cash: %+v", v.Entries)
	}
	if _, cr := sum(v.Entries, WalkInCustomers); cr != 5000 {
		t.Fatal("no party means walk-in")
	}
	pl.Gateway, pl.Method = "", "cheque"
	if v, _ = Payment(pl, "", "st", "2026-10-17", false); v.Entries[0].AccountCode != ChequesInHand {
		t.Fatalf("cheque receipt goes to Cheques in Hand: %+v", v.Entries)
	}
}

func TestRefundKeyedOnRefundID(t *testing.T) {
	pl := events.PaymentPayload{PaymentID: "pay-1", RefundID: "rf-1", AmountPaise: 2000, Method: "upi", Gateway: "razorpay", GatewayRef: "pay_X"}
	r1, err := Payment(pl, "p-1", "st", "2026-10-17", true)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Type != VPayment || r1.Source != SrcRefund || r1.SourceID != "rf-1" || r1.SourceRef != "pay_X" {
		t.Fatalf("refund key: %+v", r1)
	}
	if _, cr := sum(r1.Entries, GatewayClearing); cr != 2000 {
		t.Fatalf("refund credits the clearing account the capture debited: %+v", r1.Entries)
	}
	if dr, _ := sum(r1.Entries, "party:p-1"); dr != 2000 {
		t.Fatal("party debited")
	}
	pl.RefundID = "rf-2"
	r2, _ := Payment(pl, "p-1", "st", "2026-10-17", true)
	if r2.SourceID == r1.SourceID {
		t.Fatal("a second partial refund must get its own source id")
	}
	pl.RefundID = ""
	r3, _ := Payment(pl, "p-1", "st", "2026-10-17", true)
	if r3.SourceID != "pay-1" {
		t.Fatal("without refund_id the refund falls back to the payment id")
	}
	if c, _ := Payment(pl, "p-1", "st", "2026-10-17", false); c.SourceID != "pay-1" || c.Source != SrcPayment {
		t.Fatal("captures stay keyed on the payment id")
	}
}

func debit(es []Entry, key string) int64 { d, _ := sum(es, key); return d }

func TestTenderWithPaymentRequestStaysOnParty(t *testing.T) {
	p := baseInvoice("sale")
	p.PartyID = "p-1"
	p.Tenders = []events.Tender{{Mode: "upi", AmountPaise: 118000, PaymentRequestID: "pr-1"}}
	v, err := Invoice(p)
	if err != nil {
		t.Fatal(err)
	}
	if debit(v.Entries, Bank) != 0 {
		t.Fatalf("a lenden-backed tender must not debit Bank on the sale (the capture event books it): %+v", v.Entries)
	}
	if debit(v.Entries, "party:p-1") != 118000 || v.Total() != 118000 {
		t.Fatalf("the whole amount stays on the party until payment.captured: %+v", v.Entries)
	}

	p.PartyID = ""
	if v, _ = Invoice(p); debit(v.Entries, WalkInCustomers) != 118000 {
		t.Fatalf("walk-in sale: amount stays on Walk-in Customers: %+v", v.Entries)
	}

	p.PartyID = "p-1"
	p.Tenders = []events.Tender{{Mode: "cash", AmountPaise: 18000}, {Mode: "upi", AmountPaise: 100000, PaymentRequestID: "pr-1"}}
	v, _ = Invoice(p)
	if debit(v.Entries, Cash) != 18000 {
		t.Fatal("cash tender is still booked")
	}
	if debit(v.Entries, "party:p-1") != 100000 {
		t.Fatalf("only the QR part waits on the party: %+v", v.Entries)
	}
	p.Tenders = []events.Tender{{Mode: "card", AmountPaise: 118000}}
	if v, _ = Invoice(p); debit(v.Entries, Bank) != 118000 {
		t.Fatal("a card tender without a payment request is booked to Bank as before")
	}
}

func stockPayload(reason string, value int64, qty float64) events.StockMovedPayload {
	return events.StockMovedPayload{StoreID: "st-1", Source: "aadesh.invoice", SourceID: "inv-1", Reason: reason, ValuePaise: value,
		OccurredAt: "2026-10-17T05:00:00Z",
		Movements: []events.StockMovement{{MovementID: "m1", LineNo: 1, ItemID: "i1", ItemName: "Parle-G", Unit: "PCS", Qty: qty,
			UnitCostPaise: 820, ValuePaise: value, Reason: reason}}}
}

func TestStockMovedReclass(t *testing.T) {
	cases := []struct {
		name   string
		p      events.StockMovedPayload
		dr, cr string
		amt    int64
	}{
		{"purchase", stockPayload("purchase", 78720, 96), Inventory, Purchases, 78720},
		{"purchase_return", stockPayload("purchase_return", -8200, -10), PurchaseReturns, Inventory, 8200},
		{"sale", stockPayload("sale", -8200, -10), COGS, Inventory, 8200},
		{"sale_return", stockPayload("sale_return", 8200, 10), Inventory, COGS, 8200},
		{"adjustment loss", stockPayload("adjustment", -1640, -2), StockLoss, Inventory, 1640},
		{"adjustment gain", stockPayload("adjustment", 820, 1), Inventory, StockGain, 820},
		{"expiry_writeoff", stockPayload("expiry_writeoff", -7500, -5), StockLoss, Inventory, 7500},
		{"opening", stockPayload("opening", 400000, 500), Inventory, OpeningDiff, 400000},
	}
	for _, c := range cases {
		v, ok, err := StockMoved(c.p, "2026-10-17")
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", c.name, ok, err)
		}
		if v.Type != VJournal || v.Source != SrcStockMoved || v.SourceID != "aadesh.invoice/inv-1" || v.SourceRef != "inv-1" {
			t.Fatalf("%s: voucher key %+v", c.name, v)
		}
		if dr, _ := sum(v.Entries, c.dr); dr != c.amt {
			t.Fatalf("%s: debit %s = %d, want %d (%+v)", c.name, c.dr, dr, c.amt, v.Entries)
		}
		if _, cr := sum(v.Entries, c.cr); cr != c.amt {
			t.Fatalf("%s: credit %s = %d, want %d (%+v)", c.name, c.cr, cr, c.amt, v.Entries)
		}
		if v.Total() != c.amt || len(v.Entries) != 2 {
			t.Fatalf("%s: total %d entries %d", c.name, v.Total(), len(v.Entries))
		}
	}

	// A cancelled sale arrives as a reversal: the positive movement puts the cost back.
	rev := stockPayload("sale", 8200, 10)
	rev.Source, rev.Reversal = "aadesh.cancel", true
	v, ok, err := StockMoved(rev, "2026-10-17")
	if err != nil || !ok || v.SourceID != "aadesh.cancel/inv-1/rev" {
		t.Fatalf("reversal key: %+v %v %v", v, ok, err)
	}
	if debit(v.Entries, Inventory) != 8200 {
		t.Fatal("reversal debits inventory")
	}
	if _, cr := sum(v.Entries, COGS); cr != 8200 {
		t.Fatal("reversal credits COGS")
	}

	// Mixed adjustment: shrinkage on one line, a count surplus on another.
	mixed := stockPayload("adjustment", -820, -1)
	mixed.Movements = append(mixed.Movements, events.StockMovement{MovementID: "m2", ItemID: "i2", Qty: 3, UnitCostPaise: 100, ValuePaise: 300, Reason: "adjustment"})
	v, ok, err = StockMoved(mixed, "2026-10-17")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if debit(v.Entries, StockLoss) != 820 {
		t.Fatal("loss line")
	}
	if _, cr := sum(v.Entries, StockGain); cr != 300 {
		t.Fatal("gain line")
	}
	if dr, cr := sum(v.Entries, Inventory); dr != 300 || cr != 820 {
		t.Fatalf("inventory sides %d/%d", dr, cr)
	}

	// Transfers and production movements post nothing; zero value posts nothing.
	for _, r := range []string{"transfer_in", "transfer_out", "production_consume", "production_output"} {
		if _, ok, err := StockMoved(stockPayload(r, 1000, 1), "2026-10-17"); ok || err != nil {
			t.Fatalf("%s must be skipped silently: ok=%v err=%v", r, ok, err)
		}
	}
	if _, ok, err := StockMoved(stockPayload("sale", 0, -1), "2026-10-17"); ok || err != nil {
		t.Fatal("zero-value movement must be skipped")
	}
	if _, _, err := StockMoved(stockPayload("teleport", 100, 1), "2026-10-17"); err == nil {
		t.Fatal("unknown reason must be rejected")
	}
}

func TestSettlement(t *testing.T) {
	p := events.SettlementPayload{SettlementID: "setl-1", Gateway: "razorpay", GatewayRef: "setl_X", UTR: "UTR123",
		GrossPaise: 100000, RefundsPaise: 5000, FeePaise: 1900, FeeTaxPaise: 343, NetPaise: 92757, SettledAt: "2026-10-18T04:00:00Z"}
	v, ok, err := Settlement(p, "st", "2026-10-18")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if v.Type != VReceipt || v.Source != SrcSettlement || v.SourceID != "setl-1" || v.SourceRef != "setl_X" {
		t.Fatalf("key %+v", v)
	}
	want := map[string][2]int64{Bank: {92757, 0}, BankCharges: {1900, 0}, InputCGST: {172, 0}, InputSGST: {171, 0}, GatewayClearing: {0, 95000}}
	for code, w := range want {
		if dr, cr := sum(v.Entries, code); dr != w[0] || cr != w[1] {
			t.Errorf("%s: %d/%d want %d/%d", code, dr, cr, w[0], w[1])
		}
	}
	if v.Total() != 95000 {
		t.Fatalf("total %d", v.Total())
	}
	bad := p
	bad.NetPaise++
	if _, _, err := Settlement(bad, "st", "2026-10-18"); err == nil {
		t.Fatal("gross - refunds != net + fee + tax must be rejected")
	}
	bad = p
	bad.SettlementID = ""
	if _, _, err := Settlement(bad, "st", "2026-10-18"); err == nil {
		t.Fatal("settlement id is required")
	}
	if _, ok, err := Settlement(events.SettlementPayload{SettlementID: "z"}, "st", "2026-10-18"); ok || err != nil {
		t.Fatal("zero settlement posts nothing")
	}
	even := p
	even.FeeTaxPaise, even.NetPaise = 342, 92758
	v, _, _ = Settlement(even, "st", "2026-10-18")
	if debit(v.Entries, InputCGST) != 171 || debit(v.Entries, InputSGST) != 171 {
		t.Fatal("even tax splits equally")
	}
}

func TestChartGroupsAndParents(t *testing.T) {
	byCode := map[string]SeedAccount{}
	for _, a := range DefaultChart {
		byCode[a.Code] = a
	}
	for _, a := range DefaultChart {
		if a.Parent != "" && !byCode[a.Parent].IsGroup {
			t.Errorf("%s %s has a postable parent %s", a.Code, a.Name, a.Parent)
		}
	}
	leaves := map[string]string{ // code -> parent
		Cash: GroupAssets, Bank: GroupAssets, ChequesInHand: GroupAssets, GatewayClearing: GroupAssets,
		StockGain: GroupIncome, OtherIncome: GroupIncome,
		COGS: DirectExpenses, StockLoss: DirectExpenses, OtherDirectExpenses: DirectExpenses,
		BankCharges: IndirectExpenses, OtherIndirectExpenses: IndirectExpenses, DiscountAllowed: IndirectExpenses, RoundOff: IndirectExpenses,
	}
	for code, parent := range leaves {
		a, ok := byCode[code]
		if !ok || a.Parent != parent || a.IsGroup {
			t.Errorf("%s: %+v (want leaf under %s)", code, a, parent)
		}
	}
	for _, g := range []string{DirectExpenses, IndirectExpenses, StockInHand, SundryDebtors, SundryCreditors} {
		if !byCode[g].IsGroup {
			t.Errorf("%s must be a group", g)
		}
	}
	// Every account a rule posts to is seeded and postable.
	for _, code := range []string{Cash, Bank, ChequesInHand, GatewayClearing, WalkInCustomers, Inventory, StockRaw, WorkInProgress,
		InputCGST, InputSGST, InputIGST, InputCess, CashSuppliers, OutputCGST, OutputSGST, OutputIGST, OutputCess, OpeningDiff,
		Sales, SalesReturns, StockGain, Purchases, PurchaseReturns, COGS, StockLoss, BankCharges, RoundOff} {
		if a, ok := byCode[code]; !ok || a.IsGroup {
			t.Errorf("rules post to %s but it is missing or a group", code)
		}
	}
}
