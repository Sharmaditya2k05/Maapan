package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
)

type unitAlt = struct {
	Unit   string  `json:"unit"`
	Factor float64 `json:"factor"`
}

func itemLine(itemID, name string, qty float64, price int64, rate int) LineInput {
	l := LineInput{ItemID: itemID, Name: name}
	l.Qty, l.UnitPricePaise, l.TaxRateBP, l.PriceIncludesTax = qty, price, rate, true
	return l
}

func countOutbox(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func openQty(o *Order, lineNo int) float64 {
	for _, l := range o.Lines {
		if l.LineNo == lineNo {
			return l.OpenQty
		}
	}
	return -1
}

func TestSalesDocumentFlow(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	biscuit, salt := uuid.NewString(), uuid.NewString()
	f.items[biscuit] = clients.Item{ID: biscuit, Name: "Parle-G 100g", HSN: "1905", Unit: "PCS", MRPPaise: 1000, TaxRateBP: 1800,
		TaxCategory: "taxable", Status: "active", AltUnits: []unitAlt{{Unit: "BOX", Factor: 48}}}
	f.items[salt] = clients.Item{ID: salt, Name: "Tata Salt 1kg", HSN: "2501", Unit: "PCS", MRPPaise: 2800, TaxRateBP: 0, TaxCategory: "taxable", Status: "active"}
	customer := uuid.NewString()
	f.parties[customer] = clients.Party{ID: customer, Name: "Sharma Stores", Type: "customer", StateCode: "27", GSTIN: "27AAPFU0939F1ZV"}
	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}

	box := itemLine(biscuit, "Parle-G 100g", 10, 40000, 1800)
	box.Unit = "BOX"
	qt, created, err := s.CreateOrder(ctx, admin, OrderInput{Kind: KindQuotation, PartyID: customer, ValidUntil: "2026-11-30",
		Lines: []LineInput{box, itemLine(salt, "Tata Salt 1kg", 20, 2800, 0)}, Terms: "Valid 30 days", ClientUUID: "qt-1"})
	if err != nil || !created || qt.Number != "QT/26-27/00001" || qt.Status != OrderOpen || qt.Lines[0].UnitFactor != 48 || qt.Lines[0].BaseQty != 480 {
		t.Fatalf("quotation: %+v %v", qt, err)
	}
	if qt.GrandTotalPaise != 10*40000+20*2800 {
		t.Fatalf("quotation total %d", qt.GrandTotalPaise)
	}
	if again, created, _ := s.CreateOrder(ctx, admin, OrderInput{Kind: KindQuotation, PartyID: customer, Lines: []LineInput{box}, ClientUUID: "qt-1"}); created || again.ID != qt.ID {
		t.Fatal("client_uuid replay must return the same quotation")
	}
	outboxBefore := countOutbox(t, pool)

	// Quotation → sales order (everything open).
	res, _, err := s.ConvertOrder(ctx, admin, qt.ID, ConvertInput{To: KindSalesOrder, ExpectedDate: "2026-10-30"})
	if err != nil || res.Order == nil || res.Order.Number != "SO/26-27/00001" || len(res.Order.Lines) != 2 || res.Order.Lines[0].SourceLineID == nil {
		t.Fatalf("SO: %+v %v", res, err)
	}
	so := res.Order
	if q, _ := s.GetOrder(ctx, tenant, qt.ID); q.Status != OrderCompleted || openQty(q, 1) != 0 || len(q.ConvertedTo) != 2 {
		t.Fatalf("quotation after SO: %+v", q)
	}
	if _, _, err := s.ConvertOrder(ctx, admin, qt.ID, ConvertInput{To: KindProforma}); status(err) != 409 {
		t.Fatalf("a completed quotation has nothing open: %v", err)
	}

	// SO → delivery challan, part of line 1 only.
	res, _, err = s.ConvertOrder(ctx, admin, so.ID, ConvertInput{To: KindDeliveryChallan, Lines: []ConvertLine{{LineNo: 1, Qty: 4}}})
	if err != nil || res.Order.Number != "DC/26-27/00001" || deref(res.Order.ChallanReason) != "supply" || res.Order.Lines[0].Qty != 4 {
		t.Fatalf("challan: %+v %v", res, err)
	}
	dc := res.Order
	if got, _ := s.GetOrder(ctx, tenant, so.ID); got.Status != OrderPartial || openQty(got, 1) != 6 || openQty(got, 2) != 20 {
		t.Fatalf("SO after challan: %+v", got)
	}
	if _, _, err := s.ConvertOrder(ctx, admin, so.ID, ConvertInput{To: KindDeliveryChallan, Lines: []ConvertLine{{LineNo: 1, Qty: 7}}}); status(err) != 422 {
		t.Fatalf("more than open must be 422: %v", err)
	}
	if _, _, err := s.ConvertOrder(ctx, admin, dc.ID, ConvertInput{To: KindSalesOrder}); status(err) != 422 {
		t.Fatalf("challan → sales order is not a valid flow: %v", err)
	}
	// Orders, quotations and challans post nothing: no events, so no stock or books.
	if n := countOutbox(t, pool); n != outboxBefore {
		t.Fatalf("pre-invoice documents must not publish events (%d → %d)", outboxBefore, n)
	}

	// Challan → tax invoice: the only step that posts.
	res, created, err = s.ConvertOrder(ctx, admin, dc.ID, ConvertInput{To: "invoice", ClientUUID: "dc-inv-1", Tenders: []Tender{{Mode: "credit", AmountPaise: 160000}}})
	if err != nil || !created || res.Invoice == nil || res.Invoice.Number != "INV/26-27/00001" || deref(res.Invoice.SourceOrderID) != dc.ID {
		t.Fatalf("invoice from challan: %+v %v", res, err)
	}
	inv := res.Invoice
	if inv.Lines[0].Unit != "BOX" || inv.Lines[0].UnitFactor != 48 || inv.Lines[0].BaseQty != 192 || inv.Lines[0].SourceLineID == nil || inv.GrandTotalPaise != 160000 {
		t.Fatalf("invoice lines: %+v", inv.Lines[0])
	}
	if n := countOutbox(t, pool); n != outboxBefore+1 {
		t.Fatalf("the invoice publishes exactly one event (%d → %d)", outboxBefore, n)
	}
	if got, _ := s.GetOrder(ctx, tenant, dc.ID); got.Status != OrderCompleted {
		t.Fatalf("challan after invoice: %s", got.Status)
	}
	if res2, created, err := s.ConvertOrder(ctx, admin, dc.ID, ConvertInput{To: "invoice", ClientUUID: "dc-inv-1"}); err != nil || created || res2.Invoice.ID != inv.ID {
		t.Fatalf("replaying the same conversion must return the first invoice: %v", err)
	}
	if _, _, err := s.ConvertOrder(ctx, admin, so.ID, ConvertInput{To: "invoice", ClientUUID: "dc-inv-1"}); status(err) != 409 {
		t.Fatalf("a client_uuid used for another conversion must be 409: %v", err)
	}
	links, _ := s.InvoiceLinks(ctx, inv.ID)
	if len(links) != 1 || links[0].FromNumber != "DC/26-27/00001" || links[0].Qty != 4 {
		t.Fatalf("invoice links: %+v", links)
	}

	// SO → invoice directly for half the salt; then the SO cannot be cancelled while children live.
	res, _, err = s.ConvertOrder(ctx, admin, so.ID, ConvertInput{To: "invoice", Lines: []ConvertLine{{LineNo: 2, Qty: 10}}})
	if err != nil || res.Invoice.GrandTotalPaise != 28000 {
		t.Fatalf("partial invoice from SO: %+v %v", res, err)
	}
	inv2 := res.Invoice
	if _, err := s.CancelOrder(ctx, admin, so.ID, "customer backed out"); status(err) != 409 {
		t.Fatalf("an order with live documents cannot be cancelled: %v", err)
	}

	// Cancelling the invoice gives the challan its quantity back.
	if _, err := s.Cancel(ctx, admin, inv.ID, "wrong rate"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetOrder(ctx, tenant, dc.ID); got.Status != OrderOpen || openQty(got, 1) != 4 || got.ConvertedTo[0].ReversedAt == nil {
		t.Fatalf("challan after invoice cancel: %+v", got)
	}
	// ... and cancelling the challan gives the sales order its quantity back.
	if _, err := s.CancelOrder(ctx, admin, dc.ID, "goods not sent"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOrder(ctx, tenant, so.ID)
	if got.Status != OrderPartial || openQty(got, 1) != 10 || openQty(got, 2) != 10 {
		t.Fatalf("SO after challan cancel: %+v", got.Lines)
	}
	// Short-close: the rest is not expected; no further conversions.
	closed, err := s.CloseOrder(ctx, admin, so.ID, "rest not needed")
	if err != nil || closed.Status != OrderClosed || openQty(closed, 1) != 0 {
		t.Fatalf("close: %+v %v", closed, err)
	}
	if _, _, err := s.ConvertOrder(ctx, admin, so.ID, ConvertInput{To: "invoice"}); status(err) != 409 {
		t.Fatalf("a closed order cannot be converted: %v", err)
	}
	if _, err := s.Cancel(ctx, admin, inv2.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetOrder(ctx, tenant, so.ID); got.Status != OrderClosed {
		t.Fatalf("a closed order stays closed when a child is cancelled: %s", got.Status)
	}

	// Proforma for a walk-in party, then invoice; listing pending documents.
	pf, _, err := s.CreateOrder(ctx, admin, OrderInput{Kind: KindProforma, Party: &PartyInput{Name: "Ravi", Phone: "9000000001"},
		Lines: []LineInput{itemLine(salt, "Tata Salt 1kg", 5, 2800, 0)}})
	if err != nil || pf.Number != "PI/26-27/00001" {
		t.Fatalf("proforma: %+v %v", pf, err)
	}
	pending, total, err := s.ListOrders(ctx, tenant, OrderFilter{OpenOnly: true})
	if err != nil || total != 1 || pending[0].ID != pf.ID {
		t.Fatalf("pending list: %d %v", total, err)
	}
	res, _, err = s.ConvertOrder(ctx, admin, pf.ID, ConvertInput{})
	if err != nil || res.Kind != DocSale || res.Invoice.PartyName != "Ravi" {
		t.Fatalf("proforma → invoice: %+v %v", res, err)
	}
	if _, err := s.CancelOrder(ctx, admin, qt.ID, "x"); status(err) != 409 {
		t.Fatalf("quotation with a live SO cannot be cancelled: %v", err)
	}
}

func TestPurchaseDocumentFlow(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	syrup := uuid.NewString()
	f.items[syrup] = clients.Item{ID: syrup, Name: "Cough Syrup 100ml", HSN: "3004", Unit: "PCS", MRPPaise: 12000, TaxRateBP: 500,
		TaxCategory: "taxable", Status: "active", TrackBatches: true, AltUnits: []unitAlt{{Unit: "CTN", Factor: 24}}}
	supplier := uuid.NewString()
	f.parties[supplier] = clients.Party{ID: supplier, Name: "Cipla Distributor", Type: "supplier", StateCode: "27", GSTIN: "27AAPFU0939F1ZV"}
	buyer := &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}

	if _, _, err := s.CreateOrder(ctx, buyer, OrderInput{Kind: KindPurchaseOrder, Lines: []LineInput{itemLine(syrup, "Cough Syrup 100ml", 1, 1000, 500)}}); status(err) != 422 {
		t.Fatalf("PO without supplier must be 422: %v", err)
	}
	pl := itemLine(syrup, "Cough Syrup 100ml", 10, 150000, 500)
	pl.Unit, pl.PriceIncludesTax = "CTN", false
	po, _, err := s.CreateOrder(ctx, buyer, OrderInput{Kind: KindPurchaseOrder, PartyID: supplier, ExpectedDate: "2026-11-05", Lines: []LineInput{pl}})
	if err != nil || po.Number != "PO/26-27/00001" || po.Lines[0].BaseQty != 240 {
		t.Fatalf("PO: %+v %v", po, err)
	}
	before := countOutbox(t, pool)

	// GRN for 6 CTN: a batch-tracked item needs a batch on receipt.
	if _, _, err := s.ConvertOrder(ctx, buyer, po.ID, ConvertInput{To: KindGRN, Lines: []ConvertLine{{LineNo: 1, Qty: 6}}}); status(err) != 422 {
		t.Fatalf("GRN of a batch item without batch must be 422: %v", err)
	}
	// The GRN records the batch actually received.
	res, _, err := s.ConvertOrder(ctx, buyer, po.ID, ConvertInput{To: KindGRN, SupplierRef: "DN-7781", Lines: []ConvertLine{{LineNo: 1, Qty: 6, BatchNo: "SY2611"}}})
	if err != nil || res.Order.Number != "GRN/26-27/00001" || deref(res.Order.SupplierRef) != "DN-7781" || deref(res.Order.Lines[0].BatchNo) != "SY2611" {
		t.Fatalf("GRN: %+v %v", res, err)
	}
	grn := res.Order
	if got, _ := s.GetOrder(ctx, tenant, po.ID); got.Status != OrderPartial || openQty(got, 1) != 4 {
		t.Fatalf("PO after GRN: %+v", got)
	}
	if n := countOutbox(t, pool); n != before {
		t.Fatal("a GRN must not publish (stock moves once, on the purchase bill)")
	}

	// GRN → purchase bill: supplier bill fields, ITC, one event.
	res, _, err = s.ConvertOrder(ctx, buyer, grn.ID, ConvertInput{To: "bill", SupplierInvoiceNo: "CP/9921", SupplierInvoiceDate: "2026-10-21", DocDate: "2026-10-22"})
	if err != nil || res.Kind != DocPurchase || res.Invoice.Number != "PUR/26-27/00001" || res.Invoice.Lines[0].ITCEligibility != ITCEligible ||
		res.Invoice.Lines[0].BaseQty != 144 || deref(res.Invoice.Lines[0].BatchNo) != "SY2611" {
		t.Fatalf("bill from GRN: %+v %v", res, err)
	}
	if n := countOutbox(t, pool); n != before+1 {
		t.Fatalf("the bill publishes one event: %d → %d", before, n)
	}
	var ev events.InvoicePayload
	var raw []byte
	_ = pool.QueryRow(ctx, `SELECT payload FROM outbox ORDER BY created_at DESC LIMIT 1`).Scan(&raw)
	var env events.Envelope
	_ = json.Unmarshal(raw, &env)
	_ = env.Decode(&ev)
	if ev.DocType != DocPurchase || len(ev.Lines) != 1 || ev.Lines[0].Unit != "CTN" || ev.Lines[0].Qty != 6 {
		t.Fatalf("bill event: %+v", ev)
	}
	// The rest of the PO straight to a bill (no GRN), then the PO is complete.
	res, _, err = s.ConvertOrder(ctx, buyer, po.ID, ConvertInput{To: "invoice", SupplierInvoiceNo: "CP/9950", SupplierInvoiceDate: "2026-10-22",
		Lines: []ConvertLine{{LineNo: 1, BatchNo: "SY2612"}}})
	if err != nil || res.Kind != DocPurchase {
		t.Fatalf("bill from PO: %+v %v", res, err)
	}
	if got, _ := s.GetOrder(ctx, tenant, po.ID); got.Status != OrderCompleted || len(got.ConvertedTo) != 2 {
		t.Fatalf("PO after bills: %+v", got)
	}
}

func TestSerialBatchAndUnitLines(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	phone, med, tea := uuid.NewString(), uuid.NewString(), uuid.NewString()
	f.items[phone] = clients.Item{ID: phone, Name: "Redmi 13", HSN: "8517", Unit: "PCS", MRPPaise: 1500000, TaxRateBP: 1800,
		TaxCategory: "taxable", Status: "active", TrackSerials: true}
	f.items[med] = clients.Item{ID: med, Name: "Paracetamol 500", HSN: "3004", Unit: "STRIP", MRPPaise: 3000, TaxRateBP: 500,
		TaxCategory: "taxable", Status: "active", TrackBatches: true}
	f.items[tea] = clients.Item{ID: tea, Name: "Tea 250g", HSN: "0902", Unit: "PCS", MRPPaise: 15000, TaxRateBP: 500,
		TaxCategory: "taxable", Status: "active", AltUnits: []unitAlt{{Unit: "BOX", Factor: 12}}}
	f.batches[med] = []clients.Batch{
		{BatchNo: "P-OLD", ExpiryDate: "2026-09-30", Qty: 50},
		{BatchNo: "P-B", ExpiryDate: "2027-06-30", Qty: 100},
		{BatchNo: "P-A", ExpiryDate: "2027-01-31", Qty: 20},
	}
	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}

	// Multi-unit: a BOX of 12 at ₹1,700 is ₹141.67 a piece (under MRP); an unknown unit is refused.
	b := itemLine(tea, "Tea 250g", 2, 170000, 500)
	b.Unit = "box"
	inv, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{b}})
	if err != nil || inv.Lines[0].Unit != "BOX" || inv.Lines[0].UnitFactor != 12 || inv.Lines[0].BaseQty != 24 || inv.Lines[0].BaseUnit != "PCS" {
		t.Fatalf("box line: %+v %v", inv, err)
	}
	if got, _ := s.Get(ctx, tenant, inv.ID); got.Lines[0].UnitFactor != 12 || got.Lines[0].BaseQty != 24 {
		t.Fatalf("stored box line: %+v", got.Lines[0])
	}
	b.Unit = "CASE"
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{b}}); status(err) != 422 {
		t.Fatalf("unknown unit must be 422: %v", err)
	}

	// Serials: required, one per unit, never sold twice, back on a credit note, sold again.
	sale := func(serials ...string) (*Invoice, error) {
		l := itemLine(phone, "Redmi 13", float64(len(serials)), 1400000, 1800)
		l.Serials = serials
		inv, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{l}})
		return inv, err
	}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{itemLine(phone, "Redmi 13", 1, 1400000, 1800)}}); status(err) != 422 {
		t.Fatalf("serial item without serials must be 422: %v", err)
	}
	two := itemLine(phone, "Redmi 13", 2, 1400000, 1800)
	two.Serials = []string{"IMEI1"}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{two}}); status(err) != 422 {
		t.Fatalf("2 phones with 1 serial must be 422: %v", err)
	}
	s1, err := sale("imei1", "IMEI2")
	if err != nil || len(s1.Lines[0].Serials) != 2 || s1.Lines[0].Serials[0] != "IMEI1" {
		t.Fatalf("serial sale: %+v %v", s1, err)
	}
	if _, err := sale("IMEI2"); status(err) != 409 {
		t.Fatalf("selling a sold serial must be 409: %v", err)
	}
	// bhandar gets the serials on the event (lines[].serial_nos).
	var raw []byte
	_ = pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE payload->'data'->>'invoice_id' = $1`, s1.ID).Scan(&raw)
	if !strings.Contains(string(raw), `"serial_nos": ["IMEI1", "IMEI2"]`) {
		t.Fatalf("event must carry serial_nos: %s", raw)
	}
	ret := LineInput{ItemID: phone, Name: "Redmi 13", Serials: []string{"IMEI3"}}
	ret.Qty = 1
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSaleReturn, RefInvoiceID: s1.ID, Lines: []LineInput{ret}}); status(err) != 422 {
		t.Fatalf("returning a serial not on the bill must be 422: %v", err)
	}
	ret.Serials = []string{"IMEI2"}
	cn, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSaleReturn, RefInvoiceID: s1.ID, Lines: []LineInput{ret}})
	if err != nil {
		t.Fatalf("credit note with serial: %v", err)
	}
	if _, err := sale("IMEI2"); err != nil {
		t.Fatalf("a returned serial can be sold again: %v", err)
	}
	// A serial in stock cannot be bought again; cancelling the sale frees IMEI1.
	supplier := uuid.NewString()
	f.parties[supplier] = clients.Party{ID: supplier, Name: "Xiaomi Dist", Type: "supplier", StateCode: "27"}
	buy := itemLine(phone, "Redmi 13", 1, 1100000, 1800)
	buy.PriceIncludesTax, buy.Serials = false, []string{"IMEI9"}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocPurchase, PartyID: supplier, Lines: []LineInput{buy}}); err != nil {
		t.Fatalf("buy IMEI9: %v", err)
	}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocPurchase, PartyID: supplier, Lines: []LineInput{buy}}); status(err) != 409 {
		t.Fatalf("buying a serial already in stock must be 409: %v", err)
	}
	if _, err := s.Cancel(ctx, admin, cn.ID, "x"); err != nil {
		t.Fatal(err)
	}
	// bhandar's view wins when it has one.
	f.serials["IMEI9"] = clients.SerialStatus{SerialNo: "IMEI9", Status: "sold"}
	if _, err := sale("IMEI9"); status(err) != 409 {
		t.Fatalf("bhandar says sold → 409: %v", err)
	}
	f.noSerialRoute = true
	if _, err := sale("IMEI9"); err != nil {
		t.Fatalf("an older bhandar leaves the local ledger in charge: %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM invoice_serials WHERE tenant_id=$1`, tenant).Scan(&n)
	if n < 5 {
		t.Fatalf("serial ledger rows: %d", n)
	}

	// Batches: FEFO when none is named, expired and unknown batches refused.
	ml := itemLine(med, "Paracetamol 500", 10, 2500, 500)
	got, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}})
	if err != nil || deref(got.Lines[0].BatchNo) != "P-A" {
		t.Fatalf("FEFO must pick P-A (earliest unexpired with stock): %+v %v", got, err)
	}
	ml.Qty = 30
	if got, _, err = s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}}); err != nil || deref(got.Lines[0].BatchNo) != "P-B" {
		t.Fatalf("FEFO skips a batch that is too small: %+v %v", got, err)
	}
	ml.Qty, ml.BatchNo = 1, "P-OLD"
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}}); status(err) != 422 {
		t.Fatalf("expired batch must be 422: %v", err)
	}
	ml.BatchNo = "NOPE"
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}}); status(err) != 422 {
		t.Fatalf("unknown batch must be 422: %v", err)
	}
	f.noBatchRoute = true
	ml.BatchNo = ""
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}}); status(err) != 422 {
		t.Fatalf("without bhandar's batch route a batch must still be named: %v", err)
	}
	ml.BatchNo = "P-B"
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ml}}); err != nil {
		t.Fatalf("named batch is accepted unchecked by an older bhandar: %v", err)
	}
}

func TestRoundOffPerTenant(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: uuid.NewString()}
	free := func(price int64) LineInput {
		l := LineInput{Name: "Service"}
		l.Qty, l.UnitPricePaise, l.TaxRateBP = 1, price, 0
		return l
	}
	inv, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{free(10050)}})
	if err != nil || inv.GrandTotalPaise != 10100 || inv.RoundOffPaise != 50 {
		t.Fatalf("default nearest rupee: %+v %v", inv, err)
	}
	if _, err := s.SetBillingSettings(ctx, tenant, BillingSettings{RoundOffMode: RoundDown, RoundOffStepPaise: 100, BankIFSC: "hdfc0001234"}); err != nil {
		t.Fatal(err)
	}
	bs, _ := s.BillingSettings(ctx, tenant)
	if bs.RoundOffMode != RoundDown || bs.BankIFSC != "HDFC0001234" || bs.ThermalWidthMM != 80 {
		t.Fatalf("settings: %+v", bs)
	}
	if inv, _, _ = s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{free(10099)}}); inv.GrandTotalPaise != 10000 || inv.RoundOffPaise != -99 {
		t.Fatalf("round down: %+v", inv)
	}
	off := false
	if inv, _, _ = s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{free(10099)}, RoundOff: &off}); inv.GrandTotalPaise != 10099 || inv.RoundOffPaise != 0 {
		t.Fatalf("round_off=false bills to the paisa: %+v", inv)
	}
	_, _ = s.SetBillingSettings(ctx, tenant, BillingSettings{RoundOffMode: RoundUp, RoundOffStepPaise: 1000})
	if inv, _, _ = s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{free(10001)}}); inv.GrandTotalPaise != 11000 {
		t.Fatalf("round up to ₹10: %+v", inv)
	}
	o, _, err := s.CreateOrder(ctx, admin, OrderInput{Kind: KindQuotation, Lines: []LineInput{free(10001)}})
	if err != nil || o.GrandTotalPaise != 11000 || o.RoundOffPaise != 999 {
		t.Fatalf("orders use the tenant rule too: %+v %v", o, err)
	}
	if _, err := s.SetBillingSettings(ctx, tenant, BillingSettings{RoundOffMode: "sideways"}); status(err) != 422 {
		t.Fatalf("bad mode must be 422: %v", err)
	}
}

func TestInvoicePDFRoutesAndSignedLink(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: uuid.NewString()}
	l := LineInput{Name: "दाल 1kg"}
	l.Qty, l.UnitPricePaise, l.TaxRateBP, l.HSN = 2, 12000, 500, "0713"
	inv, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Party: &PartyInput{Name: "राम कुमार"}, Lines: []LineInput{l}})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "svc-secret"
	s.PDFKey, s.PublicBaseURL = PDFLinkKey(secret), "https://api.example.in/api/aadesh"
	mux := http.NewServeMux()
	s.Routes(mux, auth.NewVerifier("jwt-secret-jwt-secret-jwt-secret-32", secret), false)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	internal := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set(auth.HeaderServiceSecret, secret)
		req.Header.Set(auth.HeaderTenantID, tenant)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := internal("/internal/v1/invoices/" + inv.ID + "/pdf")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/pdf" ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "INV-26-27-00001.pdf") {
		t.Fatalf("internal pdf: %d %v", res.StatusCode, res.Header)
	}
	res.Body.Close()

	res = internal("/internal/v1/invoices/" + inv.ID + "/pdf-link?ttl=600")
	var link PDFLink
	_ = json.NewDecoder(res.Body).Decode(&link)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(link.URL, "https://api.example.in/api/aadesh/public/v1/invoices/"+inv.ID+"/pdf?") ||
		link.FileName != "INV-26-27-00001.pdf" || link.ExpiresAt.Sub(s.Now()) != 10*time.Minute {
		t.Fatalf("pdf link: %d %+v", res.StatusCode, link)
	}
	u, _ := url.Parse(link.URL)
	public := func(q url.Values) int {
		res, err := http.Get(srv.URL + "/public/v1/invoices/" + inv.ID + "/pdf?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := public(u.Query()); code != 200 {
		t.Fatalf("signed link must open: %d", code)
	}
	bad := u.Query()
	bad.Set("sig", "AAAA"+bad.Get("sig")[4:])
	if code := public(bad); code != 404 {
		t.Fatalf("tampered link must be 404: %d", code)
	}
	if code := public(url.Values{}); code != 404 {
		t.Fatalf("unsigned must be 404: %d", code)
	}
	now := s.Now
	s.Now = func() time.Time { return now().Add(11 * time.Minute) }
	if code := public(u.Query()); code != 404 {
		t.Fatalf("expired link must be 404: %d", code)
	}
	s.Now = now
	// A user route still needs a token.
	if res, _ := http.Get(srv.URL + "/v1/invoices/" + inv.ID + "/pdf"); res.StatusCode != 401 {
		t.Fatalf("user pdf route without token: %d", res.StatusCode)
	}
}
