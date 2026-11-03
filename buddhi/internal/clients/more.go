package clients

import (
	"context"
	"encoding/json"
	"net/url"
)

// MasterItem is the part of bhandar's item master buddhi uses (POST
// /internal/v1/items/lookup returns full items).
type MasterItem struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	SKU                 string  `json:"sku,omitempty"`
	Barcode             string  `json:"barcode,omitempty"`
	HSN                 string  `json:"hsn,omitempty"`
	Unit                string  `json:"unit"`
	PurchasePricePaise  int64   `json:"purchase_price_paise"`
	TaxRateBP           int     `json:"tax_rate_bp"`
	ReorderLevel        float64 `json:"reorder_level"`
	ReorderQty          float64 `json:"reorder_qty"`
	PreferredSupplierID string  `json:"preferred_supplier_id,omitempty"`
	Status              string  `json:"status"`
}

// MaxLookup is bhandar's per-call lookup limit.
const MaxLookup = 1000

// LookupItems calls bhandar POST /internal/v1/items/lookup {ids, barcodes}.
func (c *Clients) LookupItems(ctx context.Context, ids, barcodes []string) ([]MasterItem, error) {
	if len(ids)+len(barcodes) == 0 {
		return nil, nil
	}
	if len(ids) > MaxLookup {
		ids = ids[:MaxLookup]
	}
	if len(ids)+len(barcodes) > MaxLookup {
		barcodes = barcodes[:MaxLookup-len(ids)]
	}
	if ids == nil {
		ids = []string{}
	}
	if barcodes == nil {
		barcodes = []string{}
	}
	var out struct {
		Items []MasterItem `json:"items"`
	}
	err := c.Bhandar.Post(ctx, "/internal/v1/items/lookup", map[string]any{"ids": ids, "barcodes": barcodes}, &out)
	return out.Items, err
}

// GSTR2B calls kar GET /internal/v1/returns/gstr2b?gstin=&period= — the same
// reconciliation as the user route GET /v1/returns/gstr2b (2B documents
// matched to purchase bills, counts, ITC summary). gstin empty = kar picks the
// tenant's primary registration. Returned raw for gstinsight to decode.
func (c *Clients) GSTR2B(ctx context.Context, gstin, period string) (json.RawMessage, error) {
	q := url.Values{}
	if gstin != "" {
		q.Set("gstin", gstin)
	}
	q.Set("period", period)
	var out json.RawMessage
	err := c.Kar.Get(ctx, "/internal/v1/returns/gstr2b?"+q.Encode(), &out)
	return out, err
}
