package jobs

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/forecast"
)

// ReorderLine is one suggestion in GET /v1/forecast/reorder.
type ReorderLine struct {
	ItemID          string   `json:"item_id"`
	Name            string   `json:"name"`
	Unit            string   `json:"unit,omitempty"`
	OnHand          float64  `json:"on_hand"`
	ReorderLevel    float64  `json:"reorder_level"`
	DailyDemand     float64  `json:"forecast_daily_demand"`
	DemandLeadTime  float64  `json:"forecast_demand_over_lead_time"`
	DaysOfCover     *float64 `json:"days_of_cover,omitempty"`
	StockoutRisk    bool     `json:"stockout_before_delivery"`
	ForecastQty     float64  `json:"forecast_qty"`
	RuleQty         float64  `json:"reorder_rule_qty"`
	SuggestedQty    float64  `json:"suggested_qty"`
	Reason          string   `json:"reason"` // forecast | below_reorder_level | forecast+below_reorder_level
	PreferredSupply string   `json:"preferred_supplier_id,omitempty"`
	UnitCostPaise   int64    `json:"unit_cost_paise,omitempty"` // last purchase price from the item master
	EstCostPaise    int64    `json:"est_cost_paise,omitempty"`
}

// SupplierGroup totals suggestions per preferred supplier (one purchase order each).
type SupplierGroup struct {
	SupplierID   string   `json:"supplier_id"` // "" = no preferred supplier set
	Items        int      `json:"items"`
	EstCostPaise int64    `json:"est_cost_paise"`
	ItemIDs      []string `json:"item_ids"`
}

// ReorderReport is the response.
type ReorderReport struct {
	StoreID      string          `json:"store_id"`
	LeadDays     int             `json:"lead_days"`
	CoverDays    int             `json:"cover_days"`
	Source       string          `json:"stock_source"` // bhandar_live | stock_alerts
	Items        []ReorderLine   `json:"items"`
	BySupplier   []SupplierGroup `json:"by_supplier"`
	EstCostPaise int64           `json:"est_cost_paise"`
	Warnings     []string        `json:"warnings"`
}

// Reorder combines bhandar's live stock and re-order rules with buddhi's demand
// forecast: an item is suggested when forecast demand over the lead time
// exceeds stock, or when stock is at/below its re-order level; the larger
// quantity wins. Items are priced and grouped by preferred supplier from the
// item master. Without live stock it falls back to bhandar's recent
// stock.low alerts.
func (f *Forecaster) Reorder(ctx context.Context, tenant, store string, leadDays int) (*ReorderReport, error) {
	if leadDays <= 0 {
		leadDays = f.LeadDays
	}
	rep := &ReorderReport{StoreID: store, LeadDays: leadDays, CoverDays: f.CoverDays, Source: "bhandar_live",
		Items: []ReorderLine{}, BySupplier: []SupplierGroup{}, Warnings: []string{}}

	models, err := f.models(ctx, tenant, store)
	if err != nil {
		return nil, err
	}
	now := f.Now().In(IST)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	stock, serr := f.Clients.StockSummary(ctx, store)
	if serr != nil {
		rep.Source = "stock_alerts"
		rep.Warnings = append(rep.Warnings, "Live stock unavailable from bhandar; showing its last low-stock alerts only.")
		if err := f.fromAlerts(ctx, tenant, store, rep); err != nil {
			return nil, err
		}
	} else {
		for _, it := range stock {
			var m *forecast.Model
			if fm, ok := models[it.ItemID]; ok {
				m = &fm.model
			}
			c := forecast.Suggest(m, from, forecast.Stock{OnHand: it.OnHand, ReorderLevel: it.ReorderLevel, ReorderQty: it.ReorderQty},
				leadDays, f.CoverDays)
			if !c.Needed {
				continue
			}
			l := ReorderLine{ItemID: it.ItemID, Name: it.Name, Unit: it.Unit, OnHand: it.OnHand, ReorderLevel: it.ReorderLevel,
				DemandLeadTime: c.DemandLead, DaysOfCover: c.DaysOfCover, StockoutRisk: c.StockoutRisk,
				ForecastQty: c.ForecastQty, RuleQty: c.RuleQty, SuggestedQty: c.SuggestedQty, Reason: strings.Join(c.Reasons, "+")}
			if m != nil {
				l.DailyDemand = round3(m.Level)
			}
			rep.Items = append(rep.Items, l)
		}
	}

	f.price(ctx, rep)
	sort.SliceStable(rep.Items, func(i, j int) bool {
		a, b := rep.Items[i], rep.Items[j]
		if a.StockoutRisk != b.StockoutRisk {
			return a.StockoutRisk
		}
		return a.SuggestedQty > b.SuggestedQty
	})
	rep.BySupplier = groupBySupplier(rep.Items)
	for _, l := range rep.Items {
		rep.EstCostPaise += l.EstCostPaise
	}
	return rep, nil
}

type fitted struct {
	name  string
	model forecast.Model
}

func (f *Forecaster) models(ctx context.Context, tenant, store string) (map[string]fitted, error) {
	rows, err := f.DB.Query(ctx, `SELECT item_id::text, name, daily_level, dow_factors FROM forecasts
		WHERE tenant_id=$1 AND store_id::text=$2 AND item_id IS NOT NULL AND daily_level > 0`, tenant, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fitted{}
	for rows.Next() {
		var id, name string
		var level float64
		var dow []float64
		if err := rows.Scan(&id, &name, &level, &dow); err != nil {
			return nil, err
		}
		m := forecast.Model{Level: level}
		copy(m.DOW[:], dow)
		out[id] = fitted{name: name, model: m}
	}
	return out, rows.Err()
}

func (f *Forecaster) fromAlerts(ctx context.Context, tenant, store string, rep *ReorderReport) error {
	rows, err := f.DB.Query(ctx, `SELECT item_id::text, item_name, on_hand::float8, reorder_level::float8, reorder_qty::float8, COALESCE(supplier_id,'')
		FROM stock_alerts WHERE tenant_id=$1 AND store_id::text=$2 AND alerted_at > now() - interval '14 days'`, tenant, store)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l ReorderLine
		var qty float64
		if err := rows.Scan(&l.ItemID, &l.Name, &l.OnHand, &l.ReorderLevel, &qty, &l.PreferredSupply); err != nil {
			return err
		}
		l.RuleQty, l.SuggestedQty, l.Reason = qty, qty, "below_reorder_level"
		rep.Items = append(rep.Items, l)
	}
	return rows.Err()
}

// price fills preferred supplier, unit and cost from bhandar's item master.
// Best effort: without it the suggestions stand, unpriced.
func (f *Forecaster) price(ctx context.Context, rep *ReorderReport) {
	if len(rep.Items) == 0 {
		return
	}
	ids := make([]string, 0, len(rep.Items))
	for _, l := range rep.Items {
		ids = append(ids, l.ItemID)
	}
	master, err := f.Clients.LookupItems(ctx, ids, nil)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "Item master unavailable from bhandar; suppliers and costs are not filled in.")
		return
	}
	byID := make(map[string]clients.MasterItem, len(master))
	for _, m := range master {
		byID[m.ID] = m
	}
	for i := range rep.Items {
		l := &rep.Items[i]
		m, ok := byID[l.ItemID]
		if !ok {
			continue
		}
		if l.PreferredSupply == "" {
			l.PreferredSupply = m.PreferredSupplierID
		}
		if l.Unit == "" {
			l.Unit = m.Unit
		}
		if m.PurchasePricePaise > 0 {
			l.UnitCostPaise = m.PurchasePricePaise
			l.EstCostPaise = int64(math.Round(l.SuggestedQty * float64(m.PurchasePricePaise)))
		}
	}
}

func groupBySupplier(items []ReorderLine) []SupplierGroup {
	idx := map[string]int{}
	out := []SupplierGroup{}
	for _, l := range items {
		i, ok := idx[l.PreferredSupply]
		if !ok {
			i = len(out)
			idx[l.PreferredSupply] = i
			out = append(out, SupplierGroup{SupplierID: l.PreferredSupply, ItemIDs: []string{}})
		}
		out[i].Items++
		out[i].EstCostPaise += l.EstCostPaise
		out[i].ItemIDs = append(out[i].ItemIDs, l.ItemID)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EstCostPaise > out[j].EstCostPaise })
	return out
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
