// Package clients wraps nirmaan's synchronous calls to bhandar.
package clients

import (
	"context"
	"net/url"

	"github.com/maapan/mool/svc"
	"github.com/maapan/nirmaan/internal/units"
)

// AltUnit is an alternate unit of an item: 1 Unit = Factor × base unit.
type AltUnit struct {
	Unit   string  `json:"unit"`
	Factor float64 `json:"factor"`
}

// Item is the slice of bhandar's item lookup nirmaan needs.
type Item struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Unit               string    `json:"unit"` // base (stock) unit
	HSN                string    `json:"hsn,omitempty"`
	TaxRateBP          int       `json:"tax_rate_bp"`
	PurchasePricePaise int64     `json:"purchase_price_paise"`
	AltUnits           []AltUnit `json:"alt_units"`
}

// Units turns the item into a units.ItemUnits.
func (it Item) Units() units.ItemUnits {
	iu := units.ItemUnits{Base: it.Unit, Alts: map[string]float64{}}
	for _, a := range it.AltUnits {
		iu.Alts[a.Unit] = a.Factor
	}
	return iu
}

// StockPos is an item's position at a store (or across stores).
type StockPos struct {
	OnHand     float64 `json:"on_hand"`
	ValuePaise int64   `json:"value_paise"`
}

// AvgCostPaise is the weighted-average cost per base unit, when there is stock to average.
func (p StockPos) AvgCostPaise() (int64, bool) {
	if p.OnHand <= 0 || p.ValuePaise <= 0 {
		return 0, false
	}
	return units.Rate(p.ValuePaise, p.OnHand), true
}

// Inventory is what nirmaan needs from bhandar (an interface so tests can fake it).
type Inventory interface {
	// Lookup fetches items by id, keyed by id.
	Lookup(ctx context.Context, ids []string) (map[string]Item, error)
	// Stock returns positions keyed by item id at storeID ("" = all stores).
	Stock(ctx context.Context, storeID string) (map[string]StockPos, error)
}

// Bhandar calls the inventory service.
type Bhandar struct{ C *svc.Client }

// Lookup fetches items by id, keyed by id.
func (b *Bhandar) Lookup(ctx context.Context, ids []string) (map[string]Item, error) {
	out := map[string]Item{}
	if len(ids) == 0 {
		return out, nil
	}
	var res struct {
		Items []Item `json:"items"`
	}
	if err := b.C.Post(ctx, "/internal/v1/items/lookup", map[string]any{"ids": dedupe(ids)}, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		out[it.ID] = it
	}
	return out, nil
}

// Stock returns on hand and value per item at a store ("" = every store).
func (b *Bhandar) Stock(ctx context.Context, storeID string) (map[string]StockPos, error) {
	var res struct {
		Items []struct {
			ItemID     string  `json:"item_id"`
			OnHand     float64 `json:"on_hand"`
			ValuePaise int64   `json:"value_paise"`
		} `json:"items"`
	}
	path := "/internal/v1/stock/summary"
	if storeID != "" {
		path += "?store_id=" + url.QueryEscape(storeID)
	}
	if err := b.C.Get(ctx, path, &res); err != nil {
		return nil, err
	}
	out := make(map[string]StockPos, len(res.Items))
	for _, it := range res.Items {
		out[it.ItemID] = StockPos{OnHand: it.OnHand, ValuePaise: it.ValuePaise}
	}
	return out, nil
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Catalog builds a units.Catalog from looked-up items.
func Catalog(items map[string]Item) units.Catalog {
	c := make(units.Catalog, len(items))
	for id, it := range items {
		c[id] = it.Units()
	}
	return c
}

// Price sources, best first.
const (
	SourceAvgStore = "avg_store" // weighted-average cost at the store
	SourceAvgAll   = "avg_all"   // weighted-average cost across stores
	SourcePurchase = "purchase"  // item master purchase price (fallback)
	SourceRollup   = "rollup"    // sub-assembly rolled up from its BOM
	SourceManual   = "manual"    // entered by the user at completion
)

// Price is a cost per base unit and where it came from.
type Price struct {
	Paise  int64  `json:"paise"`
	Source string `json:"source"`
}

// Basis values for ResolvePrices.
const (
	BasisAverage  = "avg"
	BasisPurchase = "purchase"
)

// ResolvePrices prices ids per base unit. Basis "avg" (default) uses bhandar's
// weighted-average cost at storeID, then across all stores, then the purchase
// price; "purchase" uses the purchase price only. Items with no usable price
// are absent from the result. The looked-up items come back too (units, HSN).
func ResolvePrices(ctx context.Context, inv Inventory, ids []string, storeID, basis string) (map[string]Price, map[string]Item, error) {
	prices := map[string]Price{}
	items, err := inv.Lookup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	if basis != BasisPurchase {
		type tier struct {
			pos map[string]StockPos
			src string
		}
		var tiers []tier
		if storeID != "" {
			pos, err := inv.Stock(ctx, storeID)
			if err != nil {
				return nil, nil, err
			}
			tiers = append(tiers, tier{pos, SourceAvgStore})
		}
		all, err := inv.Stock(ctx, "")
		if err != nil {
			return nil, nil, err
		}
		tiers = append(tiers, tier{all, SourceAvgAll})
		for _, id := range ids {
			for _, t := range tiers {
				if c, ok := t.pos[id].AvgCostPaise(); ok {
					prices[id] = Price{Paise: c, Source: t.src}
					break
				}
			}
		}
	}
	for _, id := range ids {
		if _, ok := prices[id]; ok {
			continue
		}
		if it, ok := items[id]; ok && it.PurchasePricePaise > 0 {
			prices[id] = Price{Paise: it.PurchasePricePaise, Source: SourcePurchase}
		}
	}
	return prices, items, nil
}
