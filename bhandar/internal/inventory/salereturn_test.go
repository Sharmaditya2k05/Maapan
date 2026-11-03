package inventory

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/maapan/mool/events"
)

// A sale return comes back at the cost the sale took it out at, never at the
// selling price, so the average cost and the stock value don't inflate.
func TestSaleReturnComesBackAtCost(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, store := uuid.NewString(), uuid.NewString()
	router := s.Router()
	it, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Widget", Unit: "pcs", SalePricePaise: 10000, MRPPaise: 12000, TaxRateBP: 1800})
	if err != nil {
		t.Fatal(err)
	}
	post := func(p events.InvoicePayload) {
		t.Helper()
		if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, store, p)); err != nil {
			t.Fatal(err)
		}
	}
	// Buy 5 @ ₹80, sell 5 @ ₹100 (taxable), return 2 against the sale.
	post(events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "purchase", Number: "PUR/1",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 5, TaxablePaise: 40000}}})
	saleID := uuid.NewString()
	post(events.InvoicePayload{InvoiceID: saleID, StoreID: store, DocType: "sale", Number: "INV/1",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 5, TaxablePaise: 50000}}})
	// Another purchase at ₹90 so today's average (₹90) differs from the sale's cost (₹80).
	post(events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "purchase", Number: "PUR/2",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 2, TaxablePaise: 18000}}})
	if err := router[events.NoteCreated](ctx, envelope(t, events.NoteCreated, tenantID, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), RefInvoiceID: saleID, StoreID: store, DocType: "sale_return", Number: "CRN/1",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 2, TaxablePaise: 20000}}})); err != nil {
		t.Fatal(err)
	}
	b := balance(t, s, tenantID, store, it.ID)
	// 2 @ ₹90 on hand + 2 returned @ ₹80 = 4 @ ₹85 (₹340), not 2 @ ₹100 from the selling price.
	if b.Qty != 4 || b.AvgCostPaise != 8500 || b.ValuePaise != 34000 {
		t.Fatalf("after return: %+v (want 4 @ 8500 = 34000)", b)
	}

	// A return with no reference falls back to today's average.
	if err := router[events.NoteCreated](ctx, envelope(t, events.NoteCreated, tenantID, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale_return", Number: "CRN/2",
		Lines: []events.InvoiceLine{{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 1, TaxablePaise: 10000}}})); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s, tenantID, store, it.ID); b.Qty != 5 || b.AvgCostPaise != 8500 {
		t.Fatalf("unreferenced return: %+v (want 5 @ 8500)", b)
	}
}
