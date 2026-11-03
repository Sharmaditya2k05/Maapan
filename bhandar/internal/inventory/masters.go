package inventory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maapan/bhandar/internal/barcode"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// ---------- weighing-scale formats ----------

// scaleFormat returns the tenant's layout for a two-digit prefix, or the default.
func (s *Service) scaleFormat(ctx context.Context, q db.Querier, tenantID, prefix string) (barcode.ScaleFormat, error) {
	f := barcode.ScaleFormat{Prefix: prefix}
	err := q.QueryRow(ctx, `SELECT kind, plu_len, check_digit, value_decimals FROM scale_formats WHERE tenant_id=$1 AND prefix=$2`,
		tenantID, prefix).Scan(&f.Kind, &f.PLULen, &f.CheckDigit, &f.ValueDecimals)
	if db.IsNotFound(err) {
		return barcode.DefaultScaleFormat(prefix), nil
	}
	return f, err
}

// ScaleFormats lists the effective layout of every prefix 20–29 (configured
// or default) with configured=true for the tenant's own rows.
func (s *Service) ScaleFormats(ctx context.Context, tenantID string) ([]ScaleFormatRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT prefix, kind, plu_len, check_digit, value_decimals FROM scale_formats WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]barcode.ScaleFormat{}
	for rows.Next() {
		var f barcode.ScaleFormat
		if err := rows.Scan(&f.Prefix, &f.Kind, &f.PLULen, &f.CheckDigit, &f.ValueDecimals); err != nil {
			return nil, err
		}
		got[f.Prefix] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ScaleFormatRow, 0, 10)
	for d := '0'; d <= '9'; d++ {
		p := "2" + string(d)
		if f, ok := got[p]; ok {
			out = append(out, ScaleFormatRow{ScaleFormat: f, Configured: true})
		} else {
			out = append(out, ScaleFormatRow{ScaleFormat: barcode.DefaultScaleFormat(p)})
		}
	}
	return out, nil
}

// ScaleFormatRow is one prefix's layout.
type ScaleFormatRow struct {
	barcode.ScaleFormat
	Configured bool `json:"configured"`
}

// ScaleFormatsInput is the body of PUT /v1/scale-formats: the configured
// prefixes (others fall back to the default layout).
type ScaleFormatsInput struct {
	Formats []barcode.ScaleFormat `json:"formats"`
}

// Validate checks every format.
func (in *ScaleFormatsInput) Validate() error {
	seen := map[string]bool{}
	for i := range in.Formats {
		f := &in.Formats[i]
		if f.Kind == "none" {
			f.PLULen, f.CheckDigit, f.ValueDecimals = 5, false, 2
		}
		if err := f.Validate(); err != nil {
			return httpx.Validation("format %d: %s", i+1, err.Error())
		}
		if seen[f.Prefix] {
			return httpx.Validation("prefix %s repeated", f.Prefix)
		}
		seen[f.Prefix] = true
	}
	return nil
}

// SetScaleFormats replaces the tenant's configured layouts.
func (s *Service) SetScaleFormats(ctx context.Context, tenantID string, in ScaleFormatsInput) ([]ScaleFormatRow, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM scale_formats WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		for _, f := range in.Formats {
			if _, err := tx.Exec(ctx, `INSERT INTO scale_formats (tenant_id, prefix, kind, plu_len, check_digit, value_decimals)
				VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, f.Prefix, f.Kind, f.PLULen, f.CheckDigit, f.ValueDecimals); err != nil {
				return err
			}
		}
		// The POS catalog carries PLUs; touching an item would be wrong, so the
		// PWA re-reads /v1/scale-formats on its own schedule.
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ScaleFormats(ctx, tenantID)
}

// ---------- bins / racks ----------

// Bin is a rack / shelf location in a store.
type Bin struct {
	StoreID string `json:"store_id"`
	Code    string `json:"code"`
	Name    string `json:"name,omitempty"`
	Active  bool   `json:"active"`
	Items   int    `json:"items"`
}

// BinInput is the body of POST /v1/bins.
type BinInput struct {
	StoreID string `json:"store_id,omitempty"`
	Code    string `json:"code"`
	Name    string `json:"name,omitempty"`
	Active  *bool  `json:"active,omitempty"`
}

// Validate normalises a bin.
func (in *BinInput) Validate() error {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" || len(in.Code) > 32 {
		return httpx.Validation("bin code is required (max 32 characters)")
	}
	return nil
}

// UpsertBin creates or renames a bin.
func (s *Service) UpsertBin(ctx context.Context, tenantID, storeID string, in BinInput) (*Bin, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	active := in.Active == nil || *in.Active
	if _, err := s.DB.Exec(ctx, `INSERT INTO bins (tenant_id, store_id, code, name, active) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, store_id, code) DO UPDATE SET name=EXCLUDED.name, active=EXCLUDED.active`,
		tenantID, storeID, in.Code, nilIfEmpty(in.Name), active); err != nil {
		return nil, err
	}
	return &Bin{StoreID: storeID, Code: in.Code, Name: in.Name, Active: active}, nil
}

// Bins lists a store's bins with the number of items assigned.
func (s *Service) Bins(ctx context.Context, tenantID, storeID string) ([]Bin, error) {
	rows, err := s.DB.Query(ctx, `SELECT b.store_id, b.code, b.name, b.active,
		(SELECT count(*) FROM item_bins ib WHERE ib.tenant_id=b.tenant_id AND ib.store_id=b.store_id AND ib.bin_code=b.code)
		FROM bins b WHERE b.tenant_id=$1 AND b.store_id=$2 ORDER BY b.code`, tenantID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bin{}
	for rows.Next() {
		var b Bin
		var name *string
		if err := rows.Scan(&b.StoreID, &b.Code, &name, &b.Active, &b.Items); err != nil {
			return nil, err
		}
		b.Name = deref(name)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ItemBin is an item's location in one store.
type ItemBin struct {
	StoreID string `json:"store_id"`
	BinCode string `json:"bin_code"`
}

// SetItemBin assigns (or with an empty code clears) an item's bin in a store.
func (s *Service) SetItemBin(ctx context.Context, tenantID, storeID, itemID, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if _, err := getItem(ctx, s.DB, tenantID, itemID); err != nil {
		return err
	}
	if code == "" {
		_, err := s.DB.Exec(ctx, `DELETE FROM item_bins WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3`, tenantID, storeID, itemID)
		return err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO item_bins (tenant_id, store_id, item_id, bin_code) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, store_id, item_id) DO UPDATE SET bin_code=EXCLUDED.bin_code`, tenantID, storeID, itemID, code)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		return httpx.Validation("bin %s does not exist in this store (create it with POST /v1/bins)", code)
	}
	return err
}

// ItemBins lists an item's bins across stores.
func (s *Service) ItemBins(ctx context.Context, tenantID, itemID string) ([]ItemBin, error) {
	rows, err := s.DB.Query(ctx, `SELECT store_id, bin_code FROM item_bins WHERE tenant_id=$1 AND item_id=$2 ORDER BY store_id`, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ItemBin{}
	for rows.Next() {
		var b ItemBin
		if err := rows.Scan(&b.StoreID, &b.BinCode); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------- supplier–item mapping ----------

// SupplierItem maps an item to a supplier with the last purchase terms.
type SupplierItem struct {
	SupplierID       string   `json:"supplier_id"`
	ItemID           string   `json:"item_id"`
	ItemName         string   `json:"item_name,omitempty"`
	SupplierSKU      string   `json:"supplier_sku,omitempty"`
	SupplierItemName string   `json:"supplier_item_name,omitempty"`
	LeadTimeDays     *int     `json:"lead_time_days,omitempty"`
	LastPricePaise   *int64   `json:"last_price_paise,omitempty"` // per base unit, GST excluded
	LastPurchaseDate string   `json:"last_purchase_date,omitempty"`
	LastInvoiceID    string   `json:"last_invoice_id,omitempty"`
	LastInvoiceNo    string   `json:"last_invoice_no,omitempty"`
	LastQty          *float64 `json:"last_qty,omitempty"`
	Purchases        int      `json:"purchases"`
	Preferred        bool     `json:"preferred"`
}

// SupplierItemInput is the body of PUT /v1/items/{id}/suppliers/{supplier_id}.
type SupplierItemInput struct {
	SupplierSKU      string `json:"supplier_sku,omitempty"`
	SupplierItemName string `json:"supplier_item_name,omitempty"`
	LeadTimeDays     *int   `json:"lead_time_days,omitempty"`
	Preferred        bool   `json:"preferred,omitempty"` // also sets the item's preferred supplier
}

// Validate checks the mapping.
func (in *SupplierItemInput) Validate() error {
	in.SupplierSKU, in.SupplierItemName = strings.TrimSpace(in.SupplierSKU), strings.TrimSpace(in.SupplierItemName)
	if in.LeadTimeDays != nil && (*in.LeadTimeDays < 0 || *in.LeadTimeDays > 365) {
		return httpx.Validation("lead_time_days must be 0 to 365")
	}
	return nil
}

// SetSupplierItem creates or updates a mapping by hand.
func (s *Service) SetSupplierItem(ctx context.Context, tenantID, itemID, supplierID string, in SupplierItemInput) (*SupplierItem, error) {
	if supplierID == "" {
		return nil, httpx.Validation("supplier id is required")
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := getItem(ctx, tx, tenantID, itemID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_items (tenant_id, supplier_id, item_id, supplier_sku, supplier_item_name, lead_time_days)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id, supplier_id, item_id) DO UPDATE SET supplier_sku=EXCLUDED.supplier_sku,
				supplier_item_name=EXCLUDED.supplier_item_name, lead_time_days=EXCLUDED.lead_time_days, updated_at=now()`,
			tenantID, supplierID, itemID, nilIfEmpty(in.SupplierSKU), nilIfEmpty(in.SupplierItemName), in.LeadTimeDays); err != nil {
			return err
		}
		if in.Preferred {
			_, err := tx.Exec(ctx, `UPDATE items SET preferred_supplier_id=$3, updated_at=now(), edited_at=now() WHERE tenant_id=$1 AND id=$2`,
				tenantID, itemID, supplierID)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rows, err := s.SupplierItems(ctx, tenantID, itemID, supplierID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// SupplierItems lists mappings of an item (supplierID "") or of a supplier (itemID "").
func (s *Service) SupplierItems(ctx context.Context, tenantID, itemID, supplierID string) ([]SupplierItem, error) {
	rows, err := s.DB.Query(ctx, `SELECT si.supplier_id, si.item_id, i.name, si.supplier_sku, si.supplier_item_name, si.lead_time_days,
		si.last_price_paise, si.last_purchase_date, si.last_invoice_id, si.last_invoice_no, si.last_qty::float8, si.purchases,
		COALESCE(i.preferred_supplier_id = si.supplier_id, false)
		FROM supplier_items si JOIN items i ON i.id = si.item_id
		WHERE si.tenant_id=$1 AND ($2 = '' OR si.item_id::text = $2) AND ($3 = '' OR si.supplier_id::text = $3)
		ORDER BY si.last_purchase_date DESC NULLS LAST, lower(i.name)`, tenantID, itemID, supplierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SupplierItem{}
	for rows.Next() {
		var m SupplierItem
		var sku, name, invID, invNo *string
		var date *time.Time
		if err := rows.Scan(&m.SupplierID, &m.ItemID, &m.ItemName, &sku, &name, &m.LeadTimeDays, &m.LastPricePaise, &date,
			&invID, &invNo, &m.LastQty, &m.Purchases, &m.Preferred); err != nil {
			return nil, err
		}
		m.SupplierSKU, m.SupplierItemName, m.LastInvoiceID, m.LastInvoiceNo = deref(sku), deref(name), deref(invID), deref(invNo)
		m.LastPurchaseDate = dateString(date)
		out = append(out, m)
	}
	return out, rows.Err()
}

// recordPurchase updates the supplier–item mapping from a purchase bill line:
// last price per base unit (taxable ÷ base qty), date, bill and quantity. An
// older bill arriving late does not overwrite a newer one.
func recordPurchase(ctx context.Context, tx pgx.Tx, tenantID, supplierID, itemID, invoiceID, invoiceNo, date string, baseQty float64, taxable int64) error {
	if supplierID == "" || itemID == "" || baseQty <= 0 {
		return nil
	}
	price := int64(math.Round(float64(taxable) / baseQty))
	var d *time.Time
	if t, err := time.Parse(time.DateOnly, date); err == nil {
		d = &t
	}
	_, err := tx.Exec(ctx, `INSERT INTO supplier_items (tenant_id, supplier_id, item_id, last_price_paise, last_purchase_date,
			last_invoice_id, last_invoice_no, last_qty, purchases)
		VALUES ($1,$2,$3,$4,COALESCE($5::date, CURRENT_DATE),$6,$7,$8,1)
		ON CONFLICT (tenant_id, supplier_id, item_id) DO UPDATE SET
			purchases = supplier_items.purchases + 1,
			last_price_paise = CASE WHEN supplier_items.last_purchase_date IS NULL OR EXCLUDED.last_purchase_date >= supplier_items.last_purchase_date
				THEN EXCLUDED.last_price_paise ELSE supplier_items.last_price_paise END,
			last_invoice_id = CASE WHEN supplier_items.last_purchase_date IS NULL OR EXCLUDED.last_purchase_date >= supplier_items.last_purchase_date
				THEN EXCLUDED.last_invoice_id ELSE supplier_items.last_invoice_id END,
			last_invoice_no = CASE WHEN supplier_items.last_purchase_date IS NULL OR EXCLUDED.last_purchase_date >= supplier_items.last_purchase_date
				THEN EXCLUDED.last_invoice_no ELSE supplier_items.last_invoice_no END,
			last_qty = CASE WHEN supplier_items.last_purchase_date IS NULL OR EXCLUDED.last_purchase_date >= supplier_items.last_purchase_date
				THEN EXCLUDED.last_qty ELSE supplier_items.last_qty END,
			last_purchase_date = GREATEST(supplier_items.last_purchase_date, EXCLUDED.last_purchase_date),
			updated_at = now()`,
		tenantID, supplierID, itemID, price, d, invoiceID, nilIfEmpty(invoiceNo), baseQty)
	return err
}
