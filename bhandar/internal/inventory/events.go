package inventory

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/logx"
)

// Router returns the event handlers bhandar subscribes to.
func (s *Service) Router() events.Router {
	return events.Router{
		events.InvoiceCreated:      s.onInvoice,
		events.NoteCreated:         s.onInvoice,
		events.InvoiceCancelled:    s.onInvoiceCancelled,
		events.ProductionCompleted: s.onProductionCompleted,
		events.TenantCreated:       s.onTenantCreated,
	}
}

// handle runs fn inside a transaction after marking the event processed;
// a redelivered event is acknowledged without doing anything.
func (s *Service) handle(ctx context.Context, e events.Envelope, fn func(tx pgx.Tx) (int, error)) error {
	posted := 0
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil {
			return err
		}
		if !fresh {
			logx.From(ctx).Info("event already processed")
			return nil
		}
		posted, err = fn(tx)
		return err
	})
	if err == nil && posted > 0 {
		s.kick()
	}
	return err
}

// invoiceEvent is events.InvoicePayload with per-line serial numbers
// (serial_nos), which aadesh sends for serial-tracked items. The field is read
// here until mool's events.InvoiceLine carries it.
type invoiceEvent struct {
	events.InvoicePayload
	Lines []invoiceLine `json:"lines"`
}

type invoiceLine struct {
	events.InvoiceLine
	SerialNos []string `json:"serial_nos,omitempty"`
}

// onInvoice moves stock for sales, purchases and returns. Lines without an
// item_id (free-text lines) do not touch stock. Sales go out at the current
// average cost; purchases come in at taxable value ÷ quantity (GST is input
// credit, not cost); returns mirror them.
func (s *Service) onInvoice(ctx context.Context, e events.Envelope) error {
	var p invoiceEvent
	if err := e.Decode(&p); err != nil {
		return err
	}
	var reason string
	sign := 1.0
	switch p.DocType {
	case "sale":
		reason, sign = ReasonSale, -1
	case "purchase":
		reason = ReasonPurchase
	case "sale_return":
		reason = ReasonSaleReturn
	case "purchase_return":
		reason, sign = ReasonPurchaseReturn, -1
	default:
		logx.From(ctx).Warn("invoice event with unknown doc_type ignored", "doc_type", p.DocType)
		return nil
	}
	storeID := p.StoreID
	if storeID == "" {
		storeID = e.StoreID
	}
	src := SourceInvoice
	if p.DocType == "sale_return" || p.DocType == "purchase_return" {
		src = SourceNote
	}
	req := postRequest{TenantID: e.TenantID, StoreID: storeID, Source: src, SourceID: p.InvoiceID, Reason: reason,
		CreatedBy: p.CreatedBy, OccurredAt: e.OccurredAt}
	var serials []serialMove
	for _, l := range p.Lines {
		if l.ItemID == "" || l.Qty <= 0 {
			continue
		}
		if len(l.SerialNos) > 0 {
			serials = append(serials, serialMove{LineNo: l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, SerialNos: l.SerialNos})
		}
		ml := MoveLine{LineNo: l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: sign * l.Qty, Unit: l.Unit, Note: p.Number}
		if sign > 0 && p.DocType != "sale_return" {
			ml.ValuePaise = l.TaxablePaise // cost per base unit = taxable ÷ base qty, computed in post
		}
		req.Lines = append(req.Lines, ml)
	}
	if len(req.Lines) == 0 {
		return s.handle(ctx, e, func(pgx.Tx) (int, error) { return 0, nil })
	}
	return s.handle(ctx, e, func(tx pgx.Tx) (int, error) {
		if p.DocType == "sale_return" {
			// Returned goods come back at what they cost, not at the selling
			// price: the original sale's cost, else today's average.
			for i := range req.Lines {
				c, err := saleReturnCost(ctx, tx, e.TenantID, storeID, p.RefInvoiceID, req.Lines[i].ItemID)
				if err != nil {
					return 0, err
				}
				req.Lines[i].UnitCostPaise = c
			}
		}
		movs, err := s.post(ctx, tx, req)
		if err != nil {
			return 0, err
		}
		if _, err := applySerials(ctx, tx, e.TenantID, storeID, src, p.InvoiceID, p.DocType, p.PartyID, serials); err != nil {
			return 0, err
		}
		if p.DocType == "purchase" {
			date := p.SupplierInvoiceDate
			if date == "" {
				date = p.InvoiceDate
			}
			for _, m := range movs {
				if err := recordPurchase(ctx, tx, e.TenantID, p.PartyID, m.ItemID, p.InvoiceID, p.Number, date, m.Qty, m.ValuePaise); err != nil {
					return 0, err
				}
			}
		}
		return len(movs), nil
	})
}

// onInvoiceCancelled reverses exactly what the original document posted.
func (s *Service) onInvoiceCancelled(ctx context.Context, e events.Envelope) error {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	return s.handle(ctx, e, func(tx pgx.Tx) (int, error) {
		orig, err := movementsOf(ctx, tx, e.TenantID, []string{SourceInvoice, SourceNote}, p.InvoiceID)
		if err != nil {
			return 0, err
		}
		if _, err := revertSerials(ctx, tx, e.TenantID, []string{SourceInvoice, SourceNote}, p.InvoiceID); err != nil {
			return 0, err
		}
		if len(orig) == 0 {
			logx.From(ctx).Info("cancellation for a document that never moved stock", "invoice_id", p.InvoiceID)
			return 0, nil
		}
		req := postRequest{TenantID: e.TenantID, StoreID: orig[0].StoreID, Source: SourceCancel, SourceID: p.InvoiceID,
			Reason: orig[0].Reason, CreatedBy: p.CreatedBy, Reversal: true, OccurredAt: e.OccurredAt}
		for _, m := range orig {
			req.Lines = append(req.Lines, MoveLine{LineNo: m.LineNo, ItemID: m.ItemID, BatchNo: m.BatchNo, Qty: -m.Qty,
				UnitCostPaise: m.UnitCostPaise, Note: "cancelled " + p.Number})
		}
		movs, err := s.post(ctx, tx, req)
		return len(movs), err
	})
}

// onProductionCompleted consumes raw material (at average cost, falling back
// to the run's cost when the store holds none) and receives finished goods
// at the production cost nirmaan computed.
func (s *Service) onProductionCompleted(ctx context.Context, e events.Envelope) error {
	var p events.ProductionCompletedPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.StoreID == "" {
		p.StoreID = e.StoreID
	}
	consume := postRequest{TenantID: e.TenantID, StoreID: p.StoreID, Source: SourceProduction, SourceID: p.ProductionRunID,
		Reason: ReasonProductionConsume, OccurredAt: e.OccurredAt}
	for i, l := range p.Consumed {
		if l.ItemID == "" || l.Qty <= 0 {
			continue
		}
		consume.Lines = append(consume.Lines, MoveLine{LineNo: i + 1, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: -l.Qty, Unit: l.Unit, UnitCostPaise: l.UnitCostPaise})
	}
	produce := consume
	produce.Reason, produce.Lines = ReasonProductionOutput, nil
	for i, l := range p.Produced {
		if l.ItemID == "" || l.Qty <= 0 {
			continue
		}
		produce.Lines = append(produce.Lines, MoveLine{LineNo: 1000 + i + 1, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: l.Qty, Unit: l.Unit, UnitCostPaise: l.UnitCostPaise})
	}
	return s.handle(ctx, e, func(tx pgx.Tx) (int, error) {
		n := 0
		for _, req := range []postRequest{consume, produce} {
			if len(req.Lines) == 0 {
				continue
			}
			movs, err := s.post(ctx, tx, req)
			if err != nil {
				return 0, err
			}
			n += len(movs)
		}
		return n, nil
	})
}

// onTenantCreated seeds the default units of measure.
func (s *Service) onTenantCreated(ctx context.Context, e events.Envelope) error {
	var p events.TenantCreatedPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	tenantID := p.TenantID
	if tenantID == "" {
		tenantID = e.TenantID
	}
	if tenantID == "" {
		return errors.New("tenant.created without tenant id")
	}
	return s.handle(ctx, e, func(tx pgx.Tx) (int, error) { return 0, seedUnits(ctx, tx, tenantID) })
}

// saleReturnCost is the unit cost a sale return comes back in at: the cost
// the original sale took the item out at, else the store's current average,
// else the item's purchase price.
func saleReturnCost(ctx context.Context, tx pgx.Tx, tenantID, storeID, refInvoiceID, itemID string) (int64, error) {
	var c int64
	if refInvoiceID != "" {
		err := tx.QueryRow(ctx, `SELECT unit_cost_paise FROM stock_movements
			WHERE tenant_id=$1 AND source=$2 AND source_id=$3 AND item_id=$4 AND qty < 0 AND unit_cost_paise > 0
			ORDER BY line_no LIMIT 1`, tenantID, SourceInvoice, refInvoiceID, itemID).Scan(&c)
		if err == nil && c > 0 {
			return c, nil
		}
		if err != nil && !db.IsNotFound(err) {
			return 0, err
		}
	}
	err := tx.QueryRow(ctx, `SELECT avg_cost_paise FROM stock_balances
		WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3 AND avg_cost_paise > 0
		ORDER BY qty DESC LIMIT 1`, tenantID, storeID, itemID).Scan(&c)
	if err == nil && c > 0 {
		return c, nil
	}
	if err != nil && !db.IsNotFound(err) {
		return 0, err
	}
	err = tx.QueryRow(ctx, `SELECT purchase_price_paise FROM items WHERE tenant_id=$1 AND id=$2`, tenantID, itemID).Scan(&c)
	if err != nil && !db.IsNotFound(err) {
		return 0, err
	}
	return c, nil
}
