// Package clients pushes validated records to bahi and bhandar bulk endpoints.
package clients

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// BulkError is one rejected record (index into the sent batch).
type BulkError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// BulkItemResult is the optional per-record outcome a bulk endpoint may return
// (needed to roll back only what an import created). Today's bahi and
// bhandar return counts only.
type BulkItemResult struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Created bool   `json:"created"`
}

// BulkResult is the common response of the bulk endpoints.
type BulkResult struct {
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Errors  []BulkError      `json:"errors"`
	Results []BulkItemResult `json:"results,omitempty"`
}

// Targets holds the downstream services.
type Targets struct {
	Bahi    *svc.Client
	Bhandar *svc.Client
	// Extras enables fields bahi / bhandar do not accept yet (they reject
	// unknown JSON fields). Flip once they ship: BAHI_EXTRAS / BHANDAR_EXTRAS.
	BahiExtras    bool
	BhandarExtras bool
}

// Bulk endpoint paths. Bills, invoice history and rollback are the calls
// aayaat needs from bahi / bhandar that do not exist yet (see README).
const (
	PathParties         = "/internal/v1/bulk/parties"
	PathOpeningBalances = "/internal/v1/bulk/opening-balances"
	PathOpenBills       = "/internal/v1/bulk/open-bills"
	PathInvoiceHistory  = "/internal/v1/bulk/invoice-history"
	PathItems           = "/internal/v1/bulk/items"
	PathOpeningStock    = "/internal/v1/bulk/opening-stock"
	PathRollback        = "/internal/v1/bulk/rollback"
)

// fieldSet lists the JSON fields an endpoint accepts today, plus the extras
// sent only when enabled.
type fieldSet struct {
	base   []string
	extras []string
}

var fields = map[string]fieldSet{
	PathParties: {
		base: []string{"name", "type", "phone", "email", "gstin", "state_code", "address", "credit_limit_paise",
			"credit_days", "opening_balance_paise", "opening_type", "opening_as_of", "external_ref"},
		extras: []string{"pan", "shipping_address"},
	},
	PathOpeningBalances: {
		base:   []string{"account_code", "party_external_ref", "amount_paise", "type"},
		extras: []string{"external_ref"},
	},
	PathItems: {
		base: []string{"name", "sku", "barcode", "hsn", "unit", "category", "brand", "sale_price_paise", "mrp_paise",
			"purchase_price_paise", "tax_rate_bp", "cess_rate_bp", "track_batches", "reorder_level", "reorder_qty", "external_ref"},
		extras: []string{"alt_units"},
	},
	PathOpeningStock: {
		base:   []string{"item_external_ref", "item_id", "qty", "unit_cost_paise", "batch_no", "expiry_date"},
		extras: []string{"mfg_date", "mrp_paise"},
	},
}

// Shape keeps only the fields path accepts. Records whose endpoint is not in
// the table (designed, not yet built) are sent as they are.
func Shape(path string, rec json.RawMessage, extras bool) (json.RawMessage, error) {
	fs, ok := fields[path]
	if !ok {
		return rec, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec, &m); err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, f := range fs.base {
		keep[f] = true
	}
	if extras {
		for _, f := range fs.extras {
			keep[f] = true
		}
	}
	for k := range m {
		if !keep[k] {
			delete(m, k)
		}
	}
	return json.Marshal(m)
}

// ExtrasFor reports whether extras are enabled for path's service.
func (t Targets) ExtrasFor(path string) bool {
	switch path {
	case PathItems, PathOpeningStock:
		return t.BhandarExtras
	}
	return t.BahiExtras
}

// Bulk posts one batch, retrying transient (5xx / network) failures 3 times.
func Bulk(ctx context.Context, c *svc.Client, path string, body any) (BulkResult, error) {
	var res BulkResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		res = BulkResult{}
		err = c.Post(ctx, path, body, &res)
		if err == nil {
			return res, nil
		}
		if e := httpx.AsError(err); (e.Status > 0 && e.Status < 500) || IsMissingEndpoint(err) {
			return res, err // a 4xx will not get better on retry
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return res, err
}

// IsMissingEndpoint reports a 404 / 405 from a downstream that has not built
// the endpoint yet (as opposed to a record-level failure).
func IsMissingEndpoint(err error) bool {
	if err == nil {
		return false
	}
	e := httpx.AsError(err)
	// A plain-text 404 from Go's mux arrives as "POST <path> returned 404".
	return e.Status == 404 || e.Status == 405 || e.Status == 501 ||
		strings.Contains(e.Message, "returned 404") || strings.Contains(e.Message, "returned 405")
}
