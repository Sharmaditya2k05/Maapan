package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maapan/bhandar/internal/barcode"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// ItemInput is the body of POST /v1/items and PUT /v1/items/{id}.
type ItemInput struct {
	Name                string    `json:"name"`
	SKU                 string    `json:"sku,omitempty"`
	Barcode             string    `json:"barcode,omitempty"`
	PLU                 string    `json:"plu,omitempty"` // weighing-scale item code
	HSN                 string    `json:"hsn,omitempty"`
	Unit                string    `json:"unit,omitempty"`
	Category            string    `json:"category,omitempty"`
	Brand               string    `json:"brand,omitempty"`
	SalePricePaise      int64     `json:"sale_price_paise"`
	MRPPaise            int64     `json:"mrp_paise"`
	PurchasePricePaise  int64     `json:"purchase_price_paise"`
	TaxRateBP           int       `json:"tax_rate_bp"`
	CessRateBP          int       `json:"cess_rate_bp,omitempty"`
	CessPerUnitPaise    int64     `json:"cess_per_unit_paise,omitempty"`
	TaxCategory         string    `json:"tax_category,omitempty"`
	PriceIncludesTax    *bool     `json:"price_includes_tax,omitempty"` // default true (MRP pricing)
	TrackBatches        bool      `json:"track_batches,omitempty"`
	ReorderLevel        float64   `json:"reorder_level,omitempty"`
	ReorderQty          float64   `json:"reorder_qty,omitempty"`
	PreferredSupplierID string    `json:"preferred_supplier_id,omitempty"`
	ExternalRef         string    `json:"external_ref,omitempty"`
	AltUnits            []AltUnit `json:"alt_units,omitempty"`
	GenerateBarcode     *bool     `json:"generate_barcode,omitempty"` // default true when barcode is empty
}

var hsnPattern = regexp.MustCompile(`^[0-9]{4}([0-9]{2}([0-9]{2})?)?$`)

// Validate normalises and checks the input. now dates the GST rate check.
func (in *ItemInput) Validate() error { return in.validate(time.Time{}) }

func (in *ItemInput) validate(now time.Time) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		return httpx.Validation("name is required (max 200 characters)")
	}
	in.SKU = strings.ToUpper(strings.TrimSpace(in.SKU))
	in.PLU = strings.TrimSpace(in.PLU)
	in.Barcode = barcode.NormalizeCode(in.Barcode)
	if in.Barcode != "" {
		if len(in.Barcode) > 48 {
			return httpx.Validation("barcode too long")
		}
		if isDigits(in.Barcode) && len(in.Barcode) == 13 && !barcode.ValidEAN13(in.Barcode) {
			return httpx.Validation("barcode %s is not a valid EAN-13 (check digit)", in.Barcode)
		}
	}
	in.HSN = strings.TrimSpace(in.HSN)
	if in.HSN != "" && !hsnPattern.MatchString(in.HSN) {
		return httpx.Validation("hsn must be 4, 6 or 8 digits")
	}
	in.Unit = strings.ToUpper(strings.TrimSpace(in.Unit))
	if in.Unit == "" {
		in.Unit = "PCS"
	}
	if len(in.Unit) > 12 {
		return httpx.Validation("unit too long")
	}
	if in.SalePricePaise < 0 || in.MRPPaise < 0 || in.PurchasePricePaise < 0 {
		return httpx.Validation("prices cannot be negative")
	}
	if in.MRPPaise > 0 && in.SalePricePaise > in.MRPPaise {
		return httpx.Validation("sale price cannot exceed MRP")
	}
	if in.TaxCategory == "" {
		in.TaxCategory = gst.CatTaxable
	}
	switch in.TaxCategory {
	case gst.CatTaxable:
		if in.TaxRateBP < 0 || in.TaxRateBP > 10000 {
			return httpx.Validation("invalid tax rate")
		}
		if !gst.ValidRate(in.TaxRateBP, now) {
			return httpx.Validation("tax rate %d bp is not a GST slab in force (allowed: %v)", in.TaxRateBP, gst.RatesOn(now))
		}
	case gst.CatExempt, gst.CatNilRated, gst.CatNonGST:
		in.TaxRateBP, in.CessRateBP, in.CessPerUnitPaise = 0, 0, 0
	default:
		return httpx.Validation("tax_category must be taxable, exempt, nil_rated or non_gst")
	}
	if in.CessRateBP < 0 || in.CessPerUnitPaise < 0 {
		return httpx.Validation("cess cannot be negative")
	}
	if in.ReorderLevel < 0 || in.ReorderQty < 0 {
		return httpx.Validation("re-order values cannot be negative")
	}
	seen := map[string]bool{}
	for i := range in.AltUnits {
		u := &in.AltUnits[i]
		u.Unit = strings.ToUpper(strings.TrimSpace(u.Unit))
		switch {
		case u.Unit == "" || u.Factor <= 0:
			return httpx.Validation("alt_units need a unit and a positive factor")
		case u.Unit == in.Unit:
			return httpx.Validation("alt unit %s is the base unit", u.Unit)
		case seen[u.Unit]:
			return httpx.Validation("alt unit %s repeated", u.Unit)
		}
		seen[u.Unit] = true
	}
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

const itemCols = `id, name, sku, barcode, plu, hsn, unit, category, brand, sale_price_paise, mrp_paise,
	purchase_price_paise, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category, price_includes_tax,
	track_batches, reorder_level::float8, reorder_qty::float8, preferred_supplier_id, status, external_ref,
	created_at, updated_at`

func scanItem(row pgx.Row) (*Item, error) {
	var it Item
	var sku, bc, plu, hsn, cat, brand, sup, ext *string
	err := row.Scan(&it.ID, &it.Name, &sku, &bc, &plu, &hsn, &it.Unit, &cat, &brand, &it.SalePricePaise, &it.MRPPaise,
		&it.PurchasePricePaise, &it.TaxRateBP, &it.CessRateBP, &it.CessPerUnitPaise, &it.TaxCategory, &it.PriceIncludesTax,
		&it.TrackBatches, &it.ReorderLevel, &it.ReorderQty, &sup, &it.Status, &ext, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, err
	}
	it.SKU, it.Barcode, it.PLU, it.HSN, it.Category, it.Brand = deref(sku), deref(bc), deref(plu), deref(hsn), deref(cat), deref(brand)
	it.PreferredSupplierID, it.ExternalRef = deref(sup), deref(ext)
	it.AltUnits = []AltUnit{}
	return &it, nil
}

// conflict turns a unique violation into a 409 naming the field.
func conflict(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		switch {
		case strings.Contains(pe.ConstraintName, "barcode"):
			return httpx.Conflict("another item already has this barcode")
		case strings.Contains(pe.ConstraintName, "sku"):
			return httpx.Conflict("another item already has this SKU")
		case strings.Contains(pe.ConstraintName, "extref"):
			return httpx.Conflict("another item already has this external_ref")
		case strings.Contains(pe.ConstraintName, "plu"):
			return httpx.Conflict("another item already has this PLU")
		}
		return httpx.Conflict("duplicate value")
	}
	return err
}

// nextBarcode allocates the next internal EAN-13 for the tenant.
func nextBarcode(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var n int64
	err := tx.QueryRow(ctx, `INSERT INTO barcode_seq (tenant_id, next_no) VALUES ($1, 2)
		ON CONFLICT (tenant_id) DO UPDATE SET next_no = barcode_seq.next_no + 1
		RETURNING next_no - 1`, tenantID).Scan(&n)
	if err != nil {
		return "", err
	}
	return barcode.InternalEAN13(n)
}

// CreateItem inserts an item, generating an internal barcode when none is given.
func (s *Service) CreateItem(ctx context.Context, tenantID string, in ItemInput) (*Item, error) {
	if err := in.validate(s.Now()); err != nil {
		return nil, err
	}
	var out *Item
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if in.Barcode == "" && (in.GenerateBarcode == nil || *in.GenerateBarcode) {
			bc, err := nextBarcode(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			in.Barcode = bc
		}
		incl := in.PriceIncludesTax == nil || *in.PriceIncludesTax
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO items (tenant_id, name, sku, barcode, plu, hsn, unit, category, brand,
			sale_price_paise, mrp_paise, purchase_price_paise, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category,
			price_includes_tax, track_batches, reorder_level, reorder_qty, preferred_supplier_id, external_ref)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) RETURNING id`,
			tenantID, in.Name, nilIfEmpty(in.SKU), nilIfEmpty(in.Barcode), nilIfEmpty(in.PLU), nilIfEmpty(in.HSN), in.Unit,
			nilIfEmpty(in.Category), nilIfEmpty(in.Brand), in.SalePricePaise, in.MRPPaise, in.PurchasePricePaise,
			in.TaxRateBP, in.CessRateBP, in.CessPerUnitPaise, in.TaxCategory, incl, in.TrackBatches, in.ReorderLevel,
			in.ReorderQty, nilIfEmpty(in.PreferredSupplierID), nilIfEmpty(in.ExternalRef)).Scan(&id)
		if err != nil {
			return conflict(err)
		}
		if err := replaceAltUnits(ctx, tx, tenantID, id, in.AltUnits); err != nil {
			return err
		}
		out, err = getItem(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

func replaceAltUnits(ctx context.Context, tx pgx.Tx, tenantID, itemID string, alts []AltUnit) error {
	if _, err := tx.Exec(ctx, `DELETE FROM item_units WHERE tenant_id=$1 AND item_id=$2`, tenantID, itemID); err != nil {
		return err
	}
	for _, a := range alts {
		if _, err := tx.Exec(ctx, `INSERT INTO item_units (tenant_id, item_id, unit, factor) VALUES ($1,$2,$3,$4)`,
			tenantID, itemID, a.Unit, a.Factor); err != nil {
			return err
		}
	}
	return nil
}

// UpdateItem replaces an item (PUT semantics). The barcode is kept when the
// body leaves it empty, so a saved item never loses its label code.
func (s *Service) UpdateItem(ctx context.Context, tenantID, id string, in ItemInput) (*Item, error) {
	if err := in.validate(s.Now()); err != nil {
		return nil, err
	}
	var out *Item
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		cur, err := getItem(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if in.Barcode == "" {
			in.Barcode = cur.Barcode
		}
		incl := in.PriceIncludesTax == nil || *in.PriceIncludesTax
		tag, err := tx.Exec(ctx, `UPDATE items SET name=$3, sku=$4, barcode=$5, plu=$6, hsn=$7, unit=$8, category=$9, brand=$10,
			sale_price_paise=$11, mrp_paise=$12, purchase_price_paise=$13, tax_rate_bp=$14, cess_rate_bp=$15,
			cess_per_unit_paise=$16, tax_category=$17, price_includes_tax=$18, track_batches=$19, reorder_level=$20,
			reorder_qty=$21, preferred_supplier_id=$22, external_ref=COALESCE($23, external_ref), updated_at=now()
			WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, in.Name, nilIfEmpty(in.SKU), nilIfEmpty(in.Barcode), nilIfEmpty(in.PLU), nilIfEmpty(in.HSN), in.Unit,
			nilIfEmpty(in.Category), nilIfEmpty(in.Brand), in.SalePricePaise, in.MRPPaise, in.PurchasePricePaise,
			in.TaxRateBP, in.CessRateBP, in.CessPerUnitPaise, in.TaxCategory, incl, in.TrackBatches, in.ReorderLevel,
			in.ReorderQty, nilIfEmpty(in.PreferredSupplierID), nilIfEmpty(in.ExternalRef))
		if err != nil {
			return conflict(err)
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFound("item not found")
		}
		if err := replaceAltUnits(ctx, tx, tenantID, id, in.AltUnits); err != nil {
			return err
		}
		out, err = getItem(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

// SetStatus activates or deactivates an item (soft delete).
func (s *Service) SetStatus(ctx context.Context, tenantID, id, status string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE items SET status=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("item not found")
	}
	return nil
}

// ReorderInput is the body of PUT /v1/items/{id}/reorder.
type ReorderInput struct {
	ReorderLevel        float64 `json:"reorder_level"`
	ReorderQty          float64 `json:"reorder_qty"`
	PreferredSupplierID string  `json:"preferred_supplier_id,omitempty"`
}

// Validate checks the re-order rule.
func (in *ReorderInput) Validate() error {
	if in.ReorderLevel < 0 || in.ReorderQty < 0 {
		return httpx.Validation("re-order values cannot be negative")
	}
	return nil
}

// SetReorder updates an item's re-order rule.
func (s *Service) SetReorder(ctx context.Context, tenantID, id string, in ReorderInput) (*Item, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE items SET reorder_level=$3, reorder_qty=$4, preferred_supplier_id=$5, updated_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, id, in.ReorderLevel, in.ReorderQty, nilIfEmpty(in.PreferredSupplierID))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.NotFound("item not found")
	}
	return getItem(ctx, s.DB, tenantID, id)
}

// GetItem loads one item with its alternate units.
func (s *Service) GetItem(ctx context.Context, tenantID, id string) (*Item, error) {
	return getItem(ctx, s.DB, tenantID, id)
}

func getItem(ctx context.Context, q db.Querier, tenantID, id string) (*Item, error) {
	it, err := scanItem(q.QueryRow(ctx, `SELECT `+itemCols+` FROM items WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("item not found")
		}
		return nil, err
	}
	items := []Item{*it}
	if err := attachAltUnits(ctx, q, tenantID, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func attachAltUnits(ctx context.Context, q db.Querier, tenantID string, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	pos := make(map[string]int, len(items))
	for i := range items {
		ids[i] = items[i].ID
		pos[items[i].ID] = i
		items[i].AltUnits = []AltUnit{}
	}
	rows, err := q.Query(ctx, `SELECT item_id, unit, factor::float8 FROM item_units WHERE tenant_id=$1 AND item_id = ANY($2::uuid[]) ORDER BY unit`, tenantID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a AltUnit
		if err := rows.Scan(&id, &a.Unit, &a.Factor); err != nil {
			return err
		}
		items[pos[id]].AltUnits = append(items[pos[id]].AltUnits, a)
	}
	return rows.Err()
}

// ItemFilter narrows GET /v1/items.
type ItemFilter struct {
	Q        string
	Category string
	Status   string // default active; "all" for every status
	httpx.Page
}

// ListItems searches the item master. q matches the name (contains) or an
// exact SKU / barcode / PLU.
func (s *Service) ListItems(ctx context.Context, tenantID string, f ItemFilter) ([]Item, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	switch f.Status {
	case "", "active":
		where = append(where, "status = 'active'")
	case "all":
	default:
		add("status = $%d", f.Status)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Q != "" {
		code := barcode.NormalizeCode(f.Q)
		args = append(args, "%"+f.Q+"%", code)
		n := len(args)
		where = append(where, fmt.Sprintf("(name ILIKE $%d OR sku = $%d OR barcode = $%d OR plu = $%d)", n-1, n, n, n))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM items WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM items WHERE %s ORDER BY lower(name) LIMIT $%d OFFSET $%d`,
		itemCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := attachAltUnits(ctx, s.DB, tenantID, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ScaleRead is what a weighing-scale barcode encoded.
type ScaleRead struct {
	Kind  string `json:"kind"`  // price | weight
	Value int64  `json:"value"` // paise, or grams
}

// CodeLookup is the response of GET /v1/barcodes/{code}.
type CodeLookup struct {
	Item  *Item      `json:"item"`
	Scale *ScaleRead `json:"scale,omitempty"`
}

// LookupByCode resolves a scanned code: an exact barcode or SKU, or a
// weighing-scale code whose PLU names the item.
func (s *Service) LookupByCode(ctx context.Context, tenantID, code string) (*CodeLookup, error) {
	code = barcode.NormalizeCode(code)
	if code == "" {
		return nil, httpx.Validation("code is required")
	}
	it, err := scanItem(s.DB.QueryRow(ctx, `SELECT `+itemCols+` FROM items WHERE tenant_id=$1 AND status='active' AND (barcode=$2 OR sku=$2) LIMIT 1`, tenantID, code))
	if err == nil {
		items := []Item{*it}
		if err := attachAltUnits(ctx, s.DB, tenantID, items); err != nil {
			return nil, err
		}
		return &CodeLookup{Item: &items[0]}, nil
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	if plu, value, kind, ok := barcode.ParseWeighingScale(code); ok {
		it, err := scanItem(s.DB.QueryRow(ctx, `SELECT `+itemCols+` FROM items WHERE tenant_id=$1 AND status='active' AND (plu=$2 OR sku=$2) LIMIT 1`, tenantID, plu))
		if err == nil {
			items := []Item{*it}
			if err := attachAltUnits(ctx, s.DB, tenantID, items); err != nil {
				return nil, err
			}
			return &CodeLookup{Item: &items[0], Scale: &ScaleRead{Kind: kind, Value: value}}, nil
		}
		if !db.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, httpx.NotFound("no item with code %s", code)
}

// CatalogItem is the compact POS catalog row cached by the PWA.
type CatalogItem struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	SKU              string    `json:"sku,omitempty"`
	Barcode          string    `json:"barcode,omitempty"`
	HSN              string    `json:"hsn,omitempty"`
	Unit             string    `json:"unit"`
	SalePricePaise   int64     `json:"sale_price_paise"`
	MRPPaise         int64     `json:"mrp_paise"`
	TaxRateBP        int       `json:"tax_rate_bp"`
	CessRateBP       int       `json:"cess_rate_bp"`
	CessPerUnitPaise int64     `json:"cess_per_unit_paise,omitempty"`
	TaxCategory      string    `json:"tax_category"`
	PriceIncludesTax bool      `json:"price_includes_tax"`
	TrackBatches     bool      `json:"track_batches"`
	PLU              string    `json:"plu,omitempty"` // weighing-scale item code (offline scale scans)
	OnHand           float64   `json:"on_hand"`
	AltUnits         []AltUnit `json:"alt_units,omitempty"`
}

// PosCatalog returns every active item with its on-hand at the store, plus an
// ETag that changes whenever an item or the store's stock changes.
func (s *Service) PosCatalog(ctx context.Context, tenantID, storeID string) ([]CatalogItem, string, error) {
	var count int
	var itemsAt, stockAt *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT count(*), max(updated_at) FROM items WHERE tenant_id=$1`, tenantID).Scan(&count, &itemsAt); err != nil {
		return nil, "", err
	}
	if err := s.DB.QueryRow(ctx, `SELECT max(updated_at) FROM stock_balances WHERE tenant_id=$1 AND store_id=$2`, tenantID, storeID).Scan(&stockAt); err != nil {
		return nil, "", err
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%v|%v", tenantID, storeID, count, itemsAt, stockAt)))
	etag := `"` + hex.EncodeToString(h[:8]) + `"`

	rows, err := s.DB.Query(ctx, `SELECT i.id, i.name, i.sku, i.barcode, i.hsn, i.unit, i.sale_price_paise, i.mrp_paise,
		i.tax_rate_bp, i.cess_rate_bp, i.cess_per_unit_paise, i.tax_category, i.price_includes_tax, i.track_batches, i.plu,
		COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id=i.tenant_id AND b.item_id=i.id AND b.store_id=$2), 0)::float8
		FROM items i WHERE i.tenant_id=$1 AND i.status='active' ORDER BY lower(i.name)`, tenantID, storeID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []CatalogItem{}
	for rows.Next() {
		var c CatalogItem
		var sku, bc, hsn, plu *string
		if err := rows.Scan(&c.ID, &c.Name, &sku, &bc, &hsn, &c.Unit, &c.SalePricePaise, &c.MRPPaise, &c.TaxRateBP,
			&c.CessRateBP, &c.CessPerUnitPaise, &c.TaxCategory, &c.PriceIncludesTax, &c.TrackBatches, &plu, &c.OnHand); err != nil {
			return nil, "", err
		}
		c.SKU, c.Barcode, c.HSN, c.PLU = deref(sku), deref(bc), deref(hsn), deref(plu)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	// Alternate units in one query.
	arows, err := s.DB.Query(ctx, `SELECT item_id, unit, factor::float8 FROM item_units WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, "", err
	}
	defer arows.Close()
	pos := make(map[string]int, len(out))
	for i := range out {
		pos[out[i].ID] = i
	}
	for arows.Next() {
		var id string
		var a AltUnit
		if err := arows.Scan(&id, &a.Unit, &a.Factor); err != nil {
			return nil, "", err
		}
		if i, ok := pos[id]; ok {
			out[i].AltUnits = append(out[i].AltUnits, a)
		}
	}
	return out, etag, arows.Err()
}

// Units lists the tenant's units of measure, seeding the defaults on first use.
func (s *Service) Units(ctx context.Context, tenantID string) ([]Unit, error) {
	rows, err := s.DB.Query(ctx, `SELECT code, name FROM units WHERE tenant_id=$1 ORDER BY code`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Unit{}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.Code, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return seedUnits(ctx, tx, tenantID) }); err != nil {
			return nil, err
		}
		return append(out, DefaultUnits...), nil
	}
	return out, nil
}

func seedUnits(ctx context.Context, tx pgx.Tx, tenantID string) error {
	for _, u := range DefaultUnits {
		if _, err := tx.Exec(ctx, `INSERT INTO units (tenant_id, code, name) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, tenantID, u.Code, u.Name); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a unit.
func (u *Unit) Validate() error {
	u.Code = strings.ToUpper(strings.TrimSpace(u.Code))
	u.Name = strings.TrimSpace(u.Name)
	if u.Code == "" || len(u.Code) > 12 || u.Name == "" {
		return httpx.Validation("unit needs a code (max 12 chars) and a name")
	}
	return nil
}

// CreateUnit adds a unit of measure.
func (s *Service) CreateUnit(ctx context.Context, tenantID string, u Unit) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO units (tenant_id, code, name) VALUES ($1,$2,$3)
		ON CONFLICT (tenant_id, code) DO UPDATE SET name = EXCLUDED.name`, tenantID, u.Code, u.Name)
	return err
}

// LookupInput is the body of POST /internal/v1/items/lookup.
type LookupInput struct {
	IDs      []string `json:"ids"`
	Barcodes []string `json:"barcodes"`
}

// LookupItems returns items by id and/or barcode (active or not).
func (s *Service) LookupItems(ctx context.Context, tenantID string, in LookupInput) ([]Item, error) {
	if len(in.IDs)+len(in.Barcodes) == 0 {
		return []Item{}, nil
	}
	if len(in.IDs)+len(in.Barcodes) > 1000 {
		return nil, httpx.Validation("lookup up to 1000 codes per call")
	}
	codes := make([]string, 0, len(in.Barcodes))
	for _, b := range in.Barcodes {
		if c := barcode.NormalizeCode(b); c != "" {
			codes = append(codes, c)
		}
	}
	ids := in.IDs
	if ids == nil {
		ids = []string{}
	}
	rows, err := s.DB.Query(ctx, `SELECT `+itemCols+` FROM items WHERE tenant_id=$1 AND (id = ANY($2::uuid[]) OR barcode = ANY($3::text[]))`,
		tenantID, ids, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, attachAltUnits(ctx, s.DB, tenantID, out)
}

// BulkItem is one record of POST /internal/v1/bulk/items (aayaat's ItemRecord).
type BulkItem struct {
	Name               string  `json:"name"`
	SKU                string  `json:"sku,omitempty"`
	Barcode            string  `json:"barcode,omitempty"`
	HSN                string  `json:"hsn,omitempty"`
	Unit               string  `json:"unit,omitempty"`
	Category           string  `json:"category,omitempty"`
	Brand              string  `json:"brand,omitempty"`
	SalePricePaise     int64   `json:"sale_price_paise"`
	MRPPaise           int64   `json:"mrp_paise"`
	PurchasePricePaise int64   `json:"purchase_price_paise"`
	TaxRateBP          int     `json:"tax_rate_bp"`
	CessRateBP         int     `json:"cess_rate_bp,omitempty"`
	TrackBatches       bool    `json:"track_batches,omitempty"`
	ReorderLevel       float64 `json:"reorder_level,omitempty"`
	ReorderQty         float64 `json:"reorder_qty,omitempty"`
	ExternalRef        string  `json:"external_ref,omitempty"`
}

// BulkError reports one failed record.
type BulkError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// BulkResult is the shape every bulk endpoint returns.
type BulkResult struct {
	Created int         `json:"created"`
	Updated int         `json:"updated"`
	Errors  []BulkError `json:"errors"`
}

// BulkItems upserts items, matching existing ones by external_ref, then SKU,
// then barcode. Each record is its own transaction so one bad row does not
// fail the batch.
func (s *Service) BulkItems(ctx context.Context, tenantID string, recs []BulkItem) (BulkResult, error) {
	res := BulkResult{Errors: []BulkError{}}
	if len(recs) > 1000 {
		return res, httpx.Validation("send up to 1000 items per call")
	}
	now := s.Now()
	for i, r := range recs {
		in := ItemInput{
			Name: r.Name, SKU: r.SKU, Barcode: r.Barcode, HSN: r.HSN, Unit: r.Unit, Category: r.Category, Brand: r.Brand,
			SalePricePaise: r.SalePricePaise, MRPPaise: r.MRPPaise, PurchasePricePaise: r.PurchasePricePaise,
			TaxRateBP: r.TaxRateBP, CessRateBP: r.CessRateBP, TrackBatches: r.TrackBatches,
			ReorderLevel: r.ReorderLevel, ReorderQty: r.ReorderQty, ExternalRef: r.ExternalRef,
		}
		if r.MRPPaise > 0 && r.SalePricePaise > r.MRPPaise {
			in.SalePricePaise = r.MRPPaise // legacy exports often carry a stale MRP; never reject a migration for it
		}
		if err := in.validate(now); err != nil {
			res.Errors = append(res.Errors, BulkError{Index: i, Message: httpx.AsError(err).Message})
			continue
		}
		created, err := s.upsertItem(ctx, tenantID, in)
		if err != nil {
			res.Errors = append(res.Errors, BulkError{Index: i, Message: httpx.AsError(err).Message})
			continue
		}
		if created {
			res.Created++
		} else {
			res.Updated++
		}
	}
	return res, nil
}

func (s *Service) upsertItem(ctx context.Context, tenantID string, in ItemInput) (created bool, err error) {
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM items WHERE tenant_id=$1 AND (
			($2::text IS NOT NULL AND external_ref=$2) OR ($3::text IS NOT NULL AND sku=$3) OR ($4::text IS NOT NULL AND barcode=$4))
			ORDER BY (external_ref=$2) DESC NULLS LAST, (sku=$3) DESC NULLS LAST LIMIT 1`,
			tenantID, nilIfEmpty(in.ExternalRef), nilIfEmpty(in.SKU), nilIfEmpty(in.Barcode)).Scan(&id)
		switch {
		case db.IsNotFound(err):
			if in.Barcode == "" {
				bc, err := nextBarcode(ctx, tx, tenantID)
				if err != nil {
					return err
				}
				in.Barcode = bc
			}
			_, err = tx.Exec(ctx, `INSERT INTO items (tenant_id, name, sku, barcode, hsn, unit, category, brand, sale_price_paise,
				mrp_paise, purchase_price_paise, tax_rate_bp, cess_rate_bp, tax_category, track_batches, reorder_level, reorder_qty, external_ref)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
				tenantID, in.Name, nilIfEmpty(in.SKU), nilIfEmpty(in.Barcode), nilIfEmpty(in.HSN), in.Unit, nilIfEmpty(in.Category),
				nilIfEmpty(in.Brand), in.SalePricePaise, in.MRPPaise, in.PurchasePricePaise, in.TaxRateBP, in.CessRateBP,
				in.TaxCategory, in.TrackBatches, in.ReorderLevel, in.ReorderQty, nilIfEmpty(in.ExternalRef))
			created = true
			return conflict(err)
		case err != nil:
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE items SET name=$3, sku=COALESCE($4, sku), barcode=COALESCE($5, barcode), hsn=COALESCE($6, hsn),
			unit=$7, category=COALESCE($8, category), brand=COALESCE($9, brand), sale_price_paise=$10, mrp_paise=$11,
			purchase_price_paise=$12, tax_rate_bp=$13, cess_rate_bp=$14, track_batches=track_batches OR $15,
			reorder_level=CASE WHEN $16 > 0 THEN $16 ELSE reorder_level END, reorder_qty=CASE WHEN $17 > 0 THEN $17 ELSE reorder_qty END,
			external_ref=COALESCE($18, external_ref), status='active', updated_at=now()
			WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, in.Name, nilIfEmpty(in.SKU), nilIfEmpty(in.Barcode), nilIfEmpty(in.HSN), in.Unit, nilIfEmpty(in.Category),
			nilIfEmpty(in.Brand), in.SalePricePaise, in.MRPPaise, in.PurchasePricePaise, in.TaxRateBP, in.CessRateBP,
			in.TrackBatches, in.ReorderLevel, in.ReorderQty, nilIfEmpty(in.ExternalRef))
		return conflict(err)
	})
	return created, err
}

// resolveItemRef finds an item by id or external_ref (bulk opening stock).
func resolveItemRef(ctx context.Context, q db.Querier, tenantID, itemID, externalRef string) (*Item, error) {
	switch {
	case itemID != "":
		return getItem(ctx, q, tenantID, itemID)
	case externalRef != "":
		it, err := scanItem(q.QueryRow(ctx, `SELECT `+itemCols+` FROM items WHERE tenant_id=$1 AND external_ref=$2`, tenantID, externalRef))
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("no item with external_ref %s", externalRef)
		}
		return it, err
	}
	return nil, httpx.Validation("item_id or item_external_ref is required")
}
