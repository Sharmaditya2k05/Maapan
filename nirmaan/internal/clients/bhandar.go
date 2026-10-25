// Package clients wraps nirmaan's synchronous calls to bhandar.
package clients

import (
	"context"
	"net/url"

	"github.com/maapan/mool/svc"
)

// Item is the slice of bhandar's item lookup nirmaan needs.
type Item struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Unit               string `json:"unit"`
	PurchasePricePaise int64  `json:"purchase_price_paise"`
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
	if err := b.C.Post(ctx, "/internal/v1/items/lookup", map[string]any{"ids": ids}, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		out[it.ID] = it
	}
	return out, nil
}

// Prices returns purchase prices (paise) keyed by item id.
func (b *Bhandar) Prices(ctx context.Context, ids []string) (map[string]int64, error) {
	items, err := b.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(items))
	for id, it := range items {
		out[id] = it.PurchasePricePaise
	}
	return out, nil
}

// OnHand returns stock on hand at a store keyed by item id.
func (b *Bhandar) OnHand(ctx context.Context, storeID string) (map[string]float64, error) {
	var res struct {
		Items []struct {
			ItemID string  `json:"item_id"`
			OnHand float64 `json:"on_hand"`
		} `json:"items"`
	}
	if err := b.C.Get(ctx, "/internal/v1/stock/summary?store_id="+url.QueryEscape(storeID), &res); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(res.Items))
	for _, it := range res.Items {
		out[it.ItemID] = it.OnHand
	}
	return out, nil
}
