package inventory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// Suggestion is one item that needs re-ordering at a store.
type Suggestion struct {
	ItemID              string  `json:"item_id"`
	Name                string  `json:"name"`
	SKU                 string  `json:"sku,omitempty"`
	HSN                 string  `json:"hsn,omitempty"`
	Unit                string  `json:"unit"`
	OnHand              float64 `json:"on_hand"`
	ReorderLevel        float64 `json:"reorder_level"`
	ReorderQty          float64 `json:"reorder_qty"`
	SuggestedQty        float64 `json:"suggested_qty"`
	PreferredSupplierID string  `json:"preferred_supplier_id,omitempty"`
	PurchasePricePaise  int64   `json:"purchase_price_paise"`
	TaxRateBP           int     `json:"tax_rate_bp"`
	CessRateBP          int     `json:"cess_rate_bp"`
	TaxCategory         string  `json:"tax_category"`
}

// Suggestions lists active items at or below their re-order level at a store.
func (s *Service) Suggestions(ctx context.Context, tenantID, storeID string) ([]Suggestion, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.id, i.name, i.sku, i.hsn, i.unit, i.reorder_level::float8, i.reorder_qty::float8, i.preferred_supplier_id,
		i.purchase_price_paise, i.tax_rate_bp, i.cess_rate_bp, i.tax_category,
		COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id=i.tenant_id AND b.item_id=i.id AND b.store_id=$2),0)::float8 AS on_hand
		FROM items i WHERE i.tenant_id=$1 AND i.status='active' AND i.reorder_level > 0 ORDER BY lower(i.name)`, tenantID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		var g Suggestion
		var sku, hsn, sup *string
		if err := rows.Scan(&g.ItemID, &g.Name, &sku, &hsn, &g.Unit, &g.ReorderLevel, &g.ReorderQty, &sup, &g.PurchasePricePaise,
			&g.TaxRateBP, &g.CessRateBP, &g.TaxCategory, &g.OnHand); err != nil {
			return nil, err
		}
		needed, qty := ReorderSuggestion(g.OnHand, g.ReorderLevel, g.ReorderQty)
		if !needed {
			continue
		}
		g.SKU, g.HSN, g.PreferredSupplierID, g.SuggestedQty = deref(sku), deref(hsn), deref(sup), qty
		out = append(out, g)
	}
	return out, rows.Err()
}

// PurchaseDraftInput is the body of POST /v1/reorder/purchase-draft.
type PurchaseDraftInput struct {
	StoreID    string   `json:"store_id,omitempty"`
	SupplierID string   `json:"supplier_id,omitempty"`
	ItemIDs    []string `json:"item_ids,omitempty"`
}

// PurchaseDraft is a ready-to-send body for aadesh POST /v1/invoices, one per supplier.
type PurchaseDraft struct {
	SupplierID string         `json:"supplier_id"` // "" when the items have no preferred supplier: the UI must pick one
	Items      int            `json:"items"`
	ValuePaise int64          `json:"value_paise"`
	Body       map[string]any `json:"body"`
}

// PurchaseDrafts groups re-order suggestions into purchase bills per supplier.
func (s *Service) PurchaseDrafts(ctx context.Context, tenantID string, in PurchaseDraftInput) ([]PurchaseDraft, error) {
	sugg, err := s.Suggestions(ctx, tenantID, in.StoreID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range in.ItemIDs {
		want[id] = true
	}
	groups := map[string]*PurchaseDraft{}
	var order []string
	for _, g := range sugg {
		if len(want) > 0 && !want[g.ItemID] {
			continue
		}
		if in.SupplierID != "" && g.PreferredSupplierID != in.SupplierID {
			continue
		}
		d := groups[g.PreferredSupplierID]
		if d == nil {
			d = &PurchaseDraft{SupplierID: g.PreferredSupplierID, Body: map[string]any{
				"doc_type": "purchase", "store_id": in.StoreID, "party_id": g.PreferredSupplierID, "lines": []map[string]any{},
				"notes": "Re-order draft " + s.Now().In(ist).Format(time.DateOnly),
			}}
			groups[g.PreferredSupplierID] = d
			order = append(order, g.PreferredSupplierID)
		}
		line := map[string]any{
			"item_id": g.ItemID, "name": g.Name, "hsn": g.HSN, "unit": g.Unit, "qty": g.SuggestedQty,
			"unit_price_paise": g.PurchasePricePaise, "discount_paise": 0, "tax_rate_bp": g.TaxRateBP, "cess_rate_bp": g.CessRateBP,
			"price_includes_tax": false,
		}
		if g.TaxCategory != "taxable" {
			line["tax_category"] = g.TaxCategory
		}
		d.Body["lines"] = append(d.Body["lines"].([]map[string]any), line)
		d.Items++
		d.ValuePaise += ValuePaise(g.SuggestedQty, g.PurchasePricePaise)
	}
	out := make([]PurchaseDraft, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out, nil
}

// ExpiringBatch is one batch position that has expired or is about to.
type ExpiringBatch struct {
	BatchID      string  `json:"batch_id"`
	ItemID       string  `json:"item_id"`
	ItemName     string  `json:"item_name"`
	Unit         string  `json:"unit"`
	StoreID      string  `json:"store_id"`
	BatchNo      string  `json:"batch_no"`
	ExpiryDate   string  `json:"expiry_date"`
	DaysLeft     int     `json:"days_left"` // negative when expired
	Qty          float64 `json:"qty"`
	AvgCostPaise int64   `json:"avg_cost_paise"`
	ValuePaise   int64   `json:"value_paise"`
}

// Expiring lists batch stock expiring within days (including already expired), per store.
func (s *Service) Expiring(ctx context.Context, tenantID, storeID string, days int) ([]ExpiringBatch, error) {
	today := s.today()
	args := []any{tenantID, today.AddDate(0, 0, days)}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $3"
	}
	rows, err := s.DB.Query(ctx, `SELECT bt.id, bt.item_id, i.name, i.unit, b.store_id, bt.batch_no, bt.expiry_date, b.qty::float8, b.avg_cost_paise
		FROM stock_balances b JOIN batches bt ON bt.id = b.batch_id JOIN items i ON i.id = b.item_id
		WHERE b.tenant_id = $1 AND b.qty > 0 AND bt.expiry_date IS NOT NULL AND bt.expiry_date <= $2`+storeCond+`
		ORDER BY bt.expiry_date, lower(i.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExpiringBatch{}
	for rows.Next() {
		var x ExpiringBatch
		var exp time.Time
		if err := rows.Scan(&x.BatchID, &x.ItemID, &x.ItemName, &x.Unit, &x.StoreID, &x.BatchNo, &exp, &x.Qty, &x.AvgCostPaise); err != nil {
			return nil, err
		}
		x.ExpiryDate = exp.Format(time.DateOnly)
		x.DaysLeft = DaysUntil(today, exp)
		x.ValuePaise = ValuePaise(x.Qty, x.AvgCostPaise)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ReturnToVendorInput is the body of POST /v1/batches/{id}/return-to-vendor.
type ReturnToVendorInput struct {
	StoreID    string  `json:"store_id,omitempty"`
	Qty        float64 `json:"qty,omitempty"` // default: everything held at the store
	SupplierID string  `json:"supplier_id,omitempty"`
}

// ReturnToVendor drafts a purchase-return (debit note) body for aadesh for a
// batch, referencing the purchase bill that brought it in when known.
func (s *Service) ReturnToVendor(ctx context.Context, tenantID, batchID, storeID string, in ReturnToVendorInput) (map[string]any, error) {
	var itemID, batchNo string
	var exp *time.Time
	err := s.DB.QueryRow(ctx, `SELECT item_id, batch_no, expiry_date FROM batches WHERE tenant_id=$1 AND id=$2`, tenantID, batchID).Scan(&itemID, &batchNo, &exp)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("batch not found")
	}
	if err != nil {
		return nil, err
	}
	it, err := getItem(ctx, s.DB, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	var onHand float64
	var avg int64
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(qty,0)::float8, COALESCE(avg_cost_paise,0) FROM stock_balances WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3 AND batch_id=$4`,
		tenantID, storeID, itemID, batchID).Scan(&onHand, &avg); err != nil && !db.IsNotFound(err) {
		return nil, err
	}
	qty := in.Qty
	if qty <= 0 {
		qty = onHand
	}
	if qty <= 0 {
		return nil, httpx.Validation("nothing of batch %s is held at this store", batchNo)
	}
	var refInvoice *string
	_ = s.DB.QueryRow(ctx, `SELECT source_id FROM stock_movements WHERE tenant_id=$1 AND item_id=$2 AND batch_id=$3 AND reason='purchase' AND source=$4
		ORDER BY created_at DESC LIMIT 1`, tenantID, itemID, batchID, SourceInvoice).Scan(&refInvoice)
	supplier := in.SupplierID
	if supplier == "" {
		supplier = it.PreferredSupplierID
	}
	body := map[string]any{
		"doc_type": "purchase_return", "store_id": storeID, "party_id": supplier,
		"notes": fmt.Sprintf("Return of batch %s (%s)", batchNo, strings.TrimSpace(dateString(exp))),
		"lines": []map[string]any{{
			"item_id": it.ID, "name": it.Name, "hsn": it.HSN, "unit": it.Unit, "batch_no": batchNo, "qty": Q3(qty),
			"unit_price_paise": avg, "discount_paise": 0, "tax_rate_bp": it.TaxRateBP, "cess_rate_bp": it.CessRateBP, "price_includes_tax": false,
		}},
	}
	if refInvoice != nil {
		body["ref_invoice_id"] = *refInvoice
	}
	return body, nil
}

// ExpiryScan raises bhandar.batch.expiring once per batch/store/day for stock
// expiring within ExpiryAlertDays, across every tenant. Returns alerts raised.
func (s *Service) ExpiryScan(ctx context.Context) (int, error) {
	today := s.today()
	rows, err := s.DB.Query(ctx, `SELECT b.tenant_id, b.store_id, bt.id, bt.item_id, i.name, bt.batch_no, bt.expiry_date, b.qty::float8
		FROM stock_balances b JOIN batches bt ON bt.id = b.batch_id JOIN items i ON i.id = b.item_id
		WHERE b.qty > 0 AND bt.expiry_date IS NOT NULL AND bt.expiry_date <= $1`, today.AddDate(0, 0, s.ExpiryAlertDays))
	if err != nil {
		return 0, err
	}
	type hit struct {
		tenant, store, batchID, itemID, name, batchNo string
		exp                                           time.Time
		qty                                           float64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.tenant, &h.store, &h.batchID, &h.itemID, &h.name, &h.batchNo, &h.exp, &h.qty); err != nil {
			rows.Close()
			return 0, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	raised := 0
	for _, h := range hits {
		err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO expiry_alerts (tenant_id, store_id, batch_id, alert_date) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				h.tenant, h.store, h.batchID, today)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			raised++
			return s.emit(ctx, tx, events.BatchExpiring, h.tenant, h.store, events.BatchExpiringPayload{
				ItemID: h.itemID, ItemName: h.name, StoreID: h.store, BatchNo: h.batchNo,
				ExpiryDate: h.exp.Format(time.DateOnly), Qty: h.qty, DaysLeft: DaysUntil(today, h.exp),
			})
		})
		if err != nil {
			return raised, err
		}
	}
	if raised > 0 {
		s.kick()
	}
	return raised, nil
}

// ReorderScan raises bhandar.stock.low once per item/store/day for every
// active item at or below its re-order level, across every tenant and store
// that holds (or held) the item. Returns alerts raised.
func (s *Service) ReorderScan(ctx context.Context) (int, error) {
	today := s.today()
	rows, err := s.DB.Query(ctx, `SELECT i.tenant_id, b.store_id, i.id, i.name, i.reorder_level::float8, i.reorder_qty::float8, i.preferred_supplier_id,
		sum(b.qty)::float8
		FROM items i JOIN stock_balances b ON b.tenant_id = i.tenant_id AND b.item_id = i.id
		WHERE i.status='active' AND i.reorder_level > 0
		GROUP BY i.tenant_id, b.store_id, i.id, i.name, i.reorder_level, i.reorder_qty, i.preferred_supplier_id
		HAVING sum(b.qty) <= i.reorder_level`)
	if err != nil {
		return 0, err
	}
	type hit struct {
		tenant, store, itemID, name, supplier string
		level, reorderQty, onHand             float64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		var sup *string
		if err := rows.Scan(&h.tenant, &h.store, &h.itemID, &h.name, &h.level, &h.reorderQty, &sup, &h.onHand); err != nil {
			rows.Close()
			return 0, err
		}
		h.supplier = deref(sup)
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	raised := 0
	for _, h := range hits {
		_, suggested := ReorderSuggestion(h.onHand, h.level, h.reorderQty)
		err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO reorder_alerts (tenant_id, store_id, item_id, alert_date) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				h.tenant, h.store, h.itemID, today)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			raised++
			return s.emit(ctx, tx, events.StockLow, h.tenant, h.store, events.StockLowPayload{
				ItemID: h.itemID, ItemName: h.name, StoreID: h.store, OnHand: h.onHand, ReorderLevel: h.level,
				ReorderQty: suggested, SupplierID: h.supplier,
			})
		})
		if err != nil {
			return raised, err
		}
	}
	if raised > 0 {
		s.kick()
	}
	return raised, nil
}
