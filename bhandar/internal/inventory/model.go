// Package inventory is bhandar's core: the item master, the multi-store stock
// ledger with weighted-average costing, batches and expiry, transfers,
// re-order rules and stock analytics.
//
// This file holds the types and the pure rules (no I/O) so they can be unit
// tested without a database.
package inventory

import (
	"errors"
	"math"
	"time"
)

// NilUUID marks non-batch stock in stock_balances.batch_id.
const NilUUID = "00000000-0000-0000-0000-000000000000"

// Movement reasons (stock_movements.reason). Direction is the sign of qty.
const (
	ReasonSale              = "sale"
	ReasonPurchase          = "purchase"
	ReasonSaleReturn        = "sale_return"
	ReasonPurchaseReturn    = "purchase_return"
	ReasonTransferIn        = "transfer_in"
	ReasonTransferOut       = "transfer_out"
	ReasonAdjustment        = "adjustment"
	ReasonOpening           = "opening"
	ReasonProductionConsume = "production_consume"
	ReasonProductionOutput  = "production_output"
	ReasonExpiryWriteoff    = "expiry_writeoff"
)

// Movement sources (stock_movements.source).
const (
	SourceInvoice    = "aadesh.invoice"
	SourceNote       = "aadesh.note"
	SourceCancel     = "aadesh.invoice.cancel"
	SourceProduction = "nirmaan.production"
	SourceAdjustment = "adjustment"
	SourceTransfer   = "transfer"
	SourceOpening    = "opening"
)

// Transfer states and actions.
const (
	TransferDraft      = "draft"
	TransferDispatched = "dispatched"
	TransferReceived   = "received"
	TransferCancelled  = "cancelled"

	ActionDispatch = "dispatch"
	ActionReceive  = "receive"
	ActionCancel   = "cancel"
)

// DefaultUnits are seeded for every new tenant.
var DefaultUnits = []Unit{
	{Code: "PCS", Name: "Pieces"}, {Code: "KG", Name: "Kilogram"}, {Code: "GM", Name: "Gram"},
	{Code: "LTR", Name: "Litre"}, {Code: "ML", Name: "Millilitre"}, {Code: "BOX", Name: "Box"},
	{Code: "DOZ", Name: "Dozen"}, {Code: "MTR", Name: "Metre"},
}

// Unit is a unit of measure.
type Unit struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// AltUnit is an alternate unit of an item: 1 <unit> = factor × base unit.
type AltUnit struct {
	Unit   string  `json:"unit"`
	Factor float64 `json:"factor"`
}

// Item is the stored item master row returned by the API.
type Item struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	SKU                 string    `json:"sku,omitempty"`
	Barcode             string    `json:"barcode,omitempty"`
	PLU                 string    `json:"plu,omitempty"` // weighing-scale item code
	HSN                 string    `json:"hsn,omitempty"`
	Unit                string    `json:"unit"`
	Category            string    `json:"category,omitempty"`
	Brand               string    `json:"brand,omitempty"`
	SalePricePaise      int64     `json:"sale_price_paise"`
	MRPPaise            int64     `json:"mrp_paise"`
	PurchasePricePaise  int64     `json:"purchase_price_paise"`
	TaxRateBP           int       `json:"tax_rate_bp"`
	CessRateBP          int       `json:"cess_rate_bp"`
	CessPerUnitPaise    int64     `json:"cess_per_unit_paise,omitempty"`
	TaxCategory         string    `json:"tax_category"`
	PriceIncludesTax    bool      `json:"price_includes_tax"`
	TrackBatches        bool      `json:"track_batches"`
	ReorderLevel        float64   `json:"reorder_level"`
	ReorderQty          float64   `json:"reorder_qty"`
	PreferredSupplierID string    `json:"preferred_supplier_id,omitempty"`
	Status              string    `json:"status"`
	ExternalRef         string    `json:"external_ref,omitempty"`
	AltUnits            []AltUnit `json:"alt_units"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Batch is a lot of a batch-tracked item.
type Batch struct {
	ID         string  `json:"id"`
	ItemID     string  `json:"item_id"`
	BatchNo    string  `json:"batch_no"`
	MfgDate    string  `json:"mfg_date,omitempty"`
	ExpiryDate string  `json:"expiry_date,omitempty"`
	MRPPaise   *int64  `json:"mrp_paise,omitempty"`
	Qty        float64 `json:"qty"` // across the stores asked for
}

// Balance is the stock position of one store/item/batch.
type Balance struct {
	StoreID      string  `json:"store_id"`
	ItemID       string  `json:"item_id"`
	ItemName     string  `json:"item_name"`
	Unit         string  `json:"unit"`
	BatchID      string  `json:"batch_id,omitempty"`
	BatchNo      string  `json:"batch_no,omitempty"`
	ExpiryDate   string  `json:"expiry_date,omitempty"`
	Qty          float64 `json:"qty"`
	AvgCostPaise int64   `json:"avg_cost_paise"`
	ValuePaise   int64   `json:"value_paise"`
	ReorderLevel float64 `json:"reorder_level,omitempty"`
	LastInAt     *string `json:"last_in_at,omitempty"`
	LastOutAt    *string `json:"last_out_at,omitempty"`
}

// Movement is one stock ledger row.
type Movement struct {
	ID            string    `json:"id"`
	StoreID       string    `json:"store_id"`
	ItemID        string    `json:"item_id"`
	ItemName      string    `json:"item_name,omitempty"`
	Unit          string    `json:"unit,omitempty"`
	BatchID       string    `json:"batch_id,omitempty"`
	BatchNo       string    `json:"batch_no,omitempty"`
	Qty           float64   `json:"qty"` // signed
	UnitCostPaise int64     `json:"unit_cost_paise"`
	ValuePaise    int64     `json:"value_paise"`
	Reason        string    `json:"reason"`
	Source        string    `json:"source"`
	SourceID      string    `json:"source_id"`
	LineNo        int       `json:"line_no"`
	Note          string    `json:"note,omitempty"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	RunningQty    *float64  `json:"running_qty,omitempty"`
}

// Transfer moves stock between two stores of a tenant.
type Transfer struct {
	ID           string         `json:"id"`
	FromStoreID  string         `json:"from_store_id"`
	ToStoreID    string         `json:"to_store_id"`
	Status       string         `json:"status"`
	Notes        string         `json:"notes,omitempty"`
	Lines        []TransferLine `json:"lines"`
	CreatedBy    string         `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	DispatchedAt *time.Time     `json:"dispatched_at,omitempty"`
	ReceivedAt   *time.Time     `json:"received_at,omitempty"`
	CancelledAt  *time.Time     `json:"cancelled_at,omitempty"`
}

// TransferLine is one item on a transfer.
type TransferLine struct {
	LineNo        int     `json:"line_no"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name,omitempty"`
	BatchID       string  `json:"batch_id,omitempty"`
	BatchNo       string  `json:"batch_no,omitempty"`
	Qty           float64 `json:"qty"`
	UnitCostPaise int64   `json:"unit_cost_paise"`
}

// Q3 rounds a quantity to 3 decimals (the NUMERIC(14,3) precision of the ledger).
func Q3(x float64) float64 { return math.Round(x*1000) / 1000 }

// ValuePaise is round(qty × unit cost), signed like qty.
func ValuePaise(qty float64, unitCost int64) int64 { return int64(math.Round(qty * float64(unitCost))) }

// WeightedAverage returns the new average cost after qtyIn units arrive at
// costIn on top of qtyOld units held at costOld (moving weighted average).
// Stock that is negative or empty carries no cost, so the arrival sets the cost.
func WeightedAverage(qtyOld float64, costOld int64, qtyIn float64, costIn int64) int64 {
	if qtyIn <= 0 {
		return costOld
	}
	if qtyOld <= 0 {
		return costIn
	}
	total := qtyOld + qtyIn
	return int64(math.Round((qtyOld*float64(costOld) + qtyIn*float64(costIn)) / total))
}

// Apply returns the balance after a signed movement. Inbound movements
// (qty > 0) re-average the cost at unitCost; outbound ones leave the average
// untouched and are costed at it. The movement's effective unit cost is
// returned so the ledger row records what was actually applied.
func Apply(qty float64, avgCost int64, moveQty float64, unitCost int64) (newQty float64, newAvg int64, applied int64) {
	newQty = Q3(qty + moveQty)
	if moveQty > 0 {
		newAvg = WeightedAverage(qty, avgCost, moveQty, unitCost)
		return newQty, newAvg, unitCost
	}
	applied = avgCost
	if applied == 0 {
		applied = unitCost // nothing in stock yet: take the caller's cost (e.g. item purchase price)
	}
	newAvg = avgCost
	if newQty <= 0 {
		// Position closed (or negative): keep the last cost for the next outbound,
		// the next arrival resets it (see WeightedAverage).
		newAvg = applied
	}
	return newQty, newAvg, applied
}

// NextTransferStatus applies action to the current status.
func NextTransferStatus(current, action string) (string, error) {
	switch {
	case current == TransferDraft && action == ActionDispatch:
		return TransferDispatched, nil
	case current == TransferDispatched && action == ActionReceive:
		return TransferReceived, nil
	case (current == TransferDraft || current == TransferDispatched) && action == ActionCancel:
		return TransferCancelled, nil
	}
	return "", errors.New("cannot " + action + " a " + current + " transfer")
}

// ReorderSuggestion reports whether stock is at or below the re-order level
// and how much to order: the item's re-order quantity, but never less than
// what is needed to get back to the level.
func ReorderSuggestion(onHand, level, reorderQty float64) (needed bool, suggested float64) {
	if level <= 0 || onHand > level {
		return false, 0
	}
	suggested = math.Max(reorderQty, level-onHand)
	if suggested <= 0 {
		suggested = level
	}
	return true, Q3(suggested)
}

// AgingBucket classifies stock by the days since it last arrived.
func AgingBucket(days int) string {
	switch {
	case days <= 30:
		return "0-30"
	case days <= 60:
		return "31-60"
	case days <= 90:
		return "61-90"
	}
	return "90+"
}

// DaysUntil returns whole days from today (IST calendar) to date; negative when past.
func DaysUntil(today, date time.Time) int {
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Round(d.Sub(t).Hours() / 24))
}

// ToBaseUnit converts qty expressed in unit to the item's base unit using its
// alternate units. The base unit itself, an empty unit, or an unknown unit
// (logged by callers) return qty unchanged; ok reports whether unit was known.
func ToBaseUnit(qty float64, unit, baseUnit string, alts []AltUnit) (float64, bool) {
	if unit == "" || equalFoldASCII(unit, baseUnit) {
		return qty, true
	}
	for _, a := range alts {
		if equalFoldASCII(a.Unit, unit) && a.Factor > 0 {
			return Q3(qty * a.Factor), true
		}
	}
	return qty, false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
