package inventory

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// VariantInput is one variant to create under a parent item. Fields left
// empty / zero are copied from the parent.
type VariantInput struct {
	Attrs              Attrs  `json:"attrs"`
	Name               string `json:"name,omitempty"` // default: "<parent> - <attr values>"
	SKU                string `json:"sku,omitempty"`
	Barcode            string `json:"barcode,omitempty"`
	SalePricePaise     int64  `json:"sale_price_paise,omitempty"`
	MRPPaise           int64  `json:"mrp_paise,omitempty"`
	PurchasePricePaise int64  `json:"purchase_price_paise,omitempty"`
}

// VariantsInput is the body of POST /v1/items/{id}/variants.
type VariantsInput struct {
	Variants []VariantInput `json:"variants"`
}

// Validate normalises attributes and rejects repeats.
func (in *VariantsInput) Validate() error {
	if len(in.Variants) == 0 || len(in.Variants) > 200 {
		return httpx.Validation("send 1 to 200 variants")
	}
	seen := map[string]bool{}
	for i := range in.Variants {
		v := &in.Variants[i]
		norm := Attrs{}
		for k, val := range v.Attrs {
			k, val = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(val)
			if k == "" || val == "" {
				return httpx.Validation("variant %d: attributes need a name and a value", i+1)
			}
			if k == "color" {
				k = "colour"
			}
			norm[k] = val
		}
		if len(norm) == 0 {
			return httpx.Validation("variant %d: attrs are required (e.g. {\"size\":\"M\",\"colour\":\"Red\"})", i+1)
		}
		v.Attrs = norm
		key := VariantKey(norm)
		if seen[key] {
			return httpx.Validation("variant %d repeats %s", i+1, key)
		}
		seen[key] = true
	}
	return nil
}

// VariantKey is the canonical form of attributes: "colour=red|size=m".
func VariantKey(a Attrs) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strings.ToLower(a[k])
	}
	return strings.Join(parts, "|")
}

// variantLabel is the display suffix: values in attribute order "Red / M"
// with size last (the way shelves read).
func variantLabel(a Attrs) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == "size") != (keys[j] == "size") {
			return keys[j] == "size"
		}
		return keys[i] < keys[j]
	})
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = a[k]
	}
	return strings.Join(vals, " / ")
}

// CreateVariants adds variants under a parent item. Each variant is an item
// of its own (own SKU, barcode, stock, prices) with parent_id set; tax, HSN,
// unit, category, brand, batch/serial tracking and alternate units come from
// the parent. A variant cannot itself have variants.
func (s *Service) CreateVariants(ctx context.Context, tenantID, parentID string, in VariantsInput) ([]Item, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	parent, err := s.GetItem(ctx, tenantID, parentID)
	if err != nil {
		return nil, err
	}
	if parent.ParentID != "" {
		return nil, httpx.Validation("%s is itself a variant; add variants to its parent", parent.Name)
	}
	var ids []string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		for i, v := range in.Variants {
			item := ItemInput{
				Name: v.Name, SKU: v.SKU, Barcode: v.Barcode, HSN: parent.HSN, Unit: parent.Unit, Category: parent.Category,
				Brand: parent.Brand, SalePricePaise: or64(v.SalePricePaise, parent.SalePricePaise), MRPPaise: or64(v.MRPPaise, parent.MRPPaise),
				PurchasePricePaise: or64(v.PurchasePricePaise, parent.PurchasePricePaise), TaxRateBP: parent.TaxRateBP,
				CessRateBP: parent.CessRateBP, CessPerUnitPaise: parent.CessPerUnitPaise, TaxCategory: parent.TaxCategory,
				TrackBatches: parent.TrackBatches, TrackSerials: parent.TrackSerials, AltUnits: parent.AltUnits,
				ReorderLevel: parent.ReorderLevel, ReorderQty: parent.ReorderQty, PreferredSupplierID: parent.PreferredSupplierID,
			}
			if item.Name == "" {
				item.Name = parent.Name + " - " + variantLabel(v.Attrs)
			}
			if err := item.validate(s.Now()); err != nil {
				return httpx.Validation("variant %d: %s", i+1, httpx.AsError(err).Message)
			}
			if item.Barcode == "" {
				bc, err := nextBarcode(ctx, tx, tenantID)
				if err != nil {
					return err
				}
				item.Barcode = bc
			}
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO items (tenant_id, name, sku, barcode, hsn, unit, category, brand,
				sale_price_paise, mrp_paise, purchase_price_paise, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category,
				price_includes_tax, track_batches, track_serials, reorder_level, reorder_qty, preferred_supplier_id,
				parent_id, variant_attrs, variant_key, edited_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,now()) RETURNING id`,
				tenantID, item.Name, nilIfEmpty(item.SKU), nilIfEmpty(item.Barcode), nilIfEmpty(item.HSN), item.Unit,
				nilIfEmpty(item.Category), nilIfEmpty(item.Brand), item.SalePricePaise, item.MRPPaise, item.PurchasePricePaise,
				item.TaxRateBP, item.CessRateBP, item.CessPerUnitPaise, item.TaxCategory, parent.PriceIncludesTax, item.TrackBatches,
				item.TrackSerials, item.ReorderLevel, item.ReorderQty, nilIfEmpty(item.PreferredSupplierID), parent.ID, v.Attrs,
				VariantKey(v.Attrs)).Scan(&id)
			if err != nil {
				if e := conflict(err); httpx.AsError(e).Status == 409 {
					msg := httpx.AsError(e).Message
					if strings.Contains(err.Error(), "items_variant_uq") {
						msg = "a variant " + variantLabel(v.Attrs) + " already exists"
					}
					return httpx.Conflict("variant %d: %s", i+1, msg)
				}
				return err
			}
			if err := replaceAltUnits(ctx, tx, tenantID, id, item.AltUnits); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		_, err := tx.Exec(ctx, `UPDATE items SET updated_at=now(), edited_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, parent.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		it, err := s.GetItem(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, nil
}

// Variants lists a parent's variants with their on-hand (all stores or one).
func (s *Service) Variants(ctx context.Context, tenantID, parentID, storeID string) ([]VariantRow, error) {
	if _, err := s.GetItem(ctx, tenantID, parentID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+itemCols+`,
		COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id=items.tenant_id AND b.item_id=items.id
			AND ($3 = '' OR b.store_id::text = $3)), 0)::float8
		FROM items WHERE tenant_id=$1 AND parent_id=$2 ORDER BY variant_key`, tenantID, parentID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VariantRow{}
	for rows.Next() {
		var it Item
		var sku, bc, plu, hsn, cat, brand, sup, ext, parent *string
		var onHand float64
		if err := rows.Scan(&it.ID, &it.Name, &sku, &bc, &plu, &hsn, &it.Unit, &cat, &brand, &it.SalePricePaise, &it.MRPPaise,
			&it.PurchasePricePaise, &it.TaxRateBP, &it.CessRateBP, &it.CessPerUnitPaise, &it.TaxCategory, &it.PriceIncludesTax,
			&it.TrackBatches, &it.ReorderLevel, &it.ReorderQty, &sup, &it.Status, &ext, &it.CreatedAt, &it.UpdatedAt,
			&it.TrackSerials, &parent, &it.VariantAttrs, &onHand); err != nil {
			return nil, err
		}
		it.SKU, it.Barcode, it.PLU, it.HSN, it.Category, it.Brand = deref(sku), deref(bc), deref(plu), deref(hsn), deref(cat), deref(brand)
		it.PreferredSupplierID, it.ExternalRef, it.ParentID = deref(sup), deref(ext), deref(parent)
		it.AltUnits = []AltUnit{}
		out = append(out, VariantRow{Item: it, OnHand: onHand})
	}
	return out, rows.Err()
}

// VariantRow is a variant with its stock.
type VariantRow struct {
	Item
	OnHand float64 `json:"on_hand"`
}

func or64(v, def int64) int64 {
	if v != 0 {
		return v
	}
	return def
}
