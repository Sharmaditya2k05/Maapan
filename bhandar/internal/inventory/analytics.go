package inventory

import (
	"context"
	"time"
)

// DeadStockRow is stock that has not moved out for a while.
type DeadStockRow struct {
	StoreID      string  `json:"store_id"`
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Qty          float64 `json:"qty"`
	AvgCostPaise int64   `json:"avg_cost_paise"`
	ValuePaise   int64   `json:"value_paise"`
	LastInAt     *string `json:"last_in_at,omitempty"`
	LastOutAt    *string `json:"last_out_at,omitempty"`
	IdleDays     int     `json:"idle_days"` // since the last outward movement, or since the last receipt if it never moved out
}

// DeadStock lists positions with no outward movement in the last days.
func (s *Service) DeadStock(ctx context.Context, tenantID, storeID string, days int) ([]DeadStockRow, error) {
	now := s.Now()
	args := []any{tenantID, now.AddDate(0, 0, -days)}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $3"
	}
	rows, err := s.DB.Query(ctx, `SELECT b.store_id, b.item_id, i.name, i.unit, sum(b.qty)::float8,
		CASE WHEN sum(b.qty) > 0 THEN round(sum(b.qty*b.avg_cost_paise)/sum(b.qty))::bigint ELSE 0 END,
		sum(round(b.qty*b.avg_cost_paise))::bigint, max(b.last_in_at), max(b.last_out_at)
		FROM stock_balances b JOIN items i ON i.id = b.item_id
		WHERE b.tenant_id = $1`+storeCond+`
		GROUP BY b.store_id, b.item_id, i.name, i.unit
		HAVING sum(b.qty) > 0 AND (max(b.last_out_at) IS NULL OR max(b.last_out_at) < $2)
		ORDER BY sum(round(b.qty*b.avg_cost_paise)) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeadStockRow{}
	for rows.Next() {
		var r DeadStockRow
		var inAt, outAt *time.Time
		if err := rows.Scan(&r.StoreID, &r.ItemID, &r.Name, &r.Unit, &r.Qty, &r.AvgCostPaise, &r.ValuePaise, &inAt, &outAt); err != nil {
			return nil, err
		}
		r.LastInAt, r.LastOutAt = derefTime(inAt), derefTime(outAt)
		switch {
		case outAt != nil:
			r.IdleDays = int(now.Sub(*outAt).Hours() / 24)
		case inAt != nil:
			r.IdleDays = int(now.Sub(*inAt).Hours() / 24)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AgingRow is one bucket of stock by days since the last receipt.
type AgingRow struct {
	Bucket     string  `json:"bucket"` // 0-30 | 31-60 | 61-90 | 90+
	Items      int     `json:"items"`
	Qty        float64 `json:"qty"`
	ValuePaise int64   `json:"value_paise"`
}

// StockAging buckets positive positions by the age of their last receipt.
func (s *Service) StockAging(ctx context.Context, tenantID, storeID string) ([]AgingRow, error) {
	now := s.Now()
	args := []any{tenantID}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $2"
	}
	rows, err := s.DB.Query(ctx, `SELECT b.item_id, b.store_id, sum(b.qty)::float8, sum(round(b.qty*b.avg_cost_paise))::bigint, max(b.last_in_at)
		FROM stock_balances b WHERE b.tenant_id = $1`+storeCond+` GROUP BY b.item_id, b.store_id HAVING sum(b.qty) > 0`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := map[string]*AgingRow{}
	for _, k := range []string{"0-30", "31-60", "61-90", "90+"} {
		buckets[k] = &AgingRow{Bucket: k}
	}
	for rows.Next() {
		var itemID, store string
		var qty float64
		var value int64
		var inAt *time.Time
		if err := rows.Scan(&itemID, &store, &qty, &value, &inAt); err != nil {
			return nil, err
		}
		days := 365
		if inAt != nil {
			days = int(now.Sub(*inAt).Hours() / 24)
		}
		b := buckets[AgingBucket(days)]
		b.Items++
		b.Qty += qty
		b.ValuePaise += value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []AgingRow{*buckets["0-30"], *buckets["31-60"], *buckets["61-90"], *buckets["90+"]}, nil
}

// ValuationRow is one item's stock value.
type ValuationRow struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Qty          float64 `json:"qty"`
	AvgCostPaise int64   `json:"avg_cost_paise"`
	ValuePaise   int64   `json:"value_paise"`
}

// Valuation is the response of GET /v1/analytics/valuation.
type Valuation struct {
	StoreID         string         `json:"store_id,omitempty"`
	AsOf            string         `json:"as_of"`
	Items           []ValuationRow `json:"items"`
	TotalQty        float64        `json:"total_qty"`
	TotalValuePaise int64          `json:"total_value_paise"`
}

// Valuate reports weighted-average stock value per item (one store or all).
func (s *Service) Valuate(ctx context.Context, tenantID, storeID string) (*Valuation, error) {
	args := []any{tenantID}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $2"
	}
	rows, err := s.DB.Query(ctx, `SELECT b.item_id, i.name, i.unit, sum(b.qty)::float8,
		CASE WHEN sum(b.qty) > 0 THEN round(sum(b.qty*b.avg_cost_paise)/sum(b.qty))::bigint ELSE 0 END,
		sum(round(b.qty*b.avg_cost_paise))::bigint
		FROM stock_balances b JOIN items i ON i.id = b.item_id WHERE b.tenant_id = $1`+storeCond+`
		GROUP BY b.item_id, i.name, i.unit HAVING sum(b.qty) <> 0 ORDER BY lower(i.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := &Valuation{StoreID: storeID, AsOf: s.Now().UTC().Format(time.RFC3339), Items: []ValuationRow{}}
	for rows.Next() {
		var r ValuationRow
		if err := rows.Scan(&r.ItemID, &r.Name, &r.Unit, &r.Qty, &r.AvgCostPaise, &r.ValuePaise); err != nil {
			return nil, err
		}
		v.Items = append(v.Items, r)
		v.TotalQty += r.Qty
		v.TotalValuePaise += r.ValuePaise
	}
	return v, rows.Err()
}

// SummaryItem is the internal stock summary row (buddhi forecasts, nirmaan availability).
type SummaryItem struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	OnHand       float64 `json:"on_hand"`
	ValuePaise   int64   `json:"value_paise"`
	ReorderLevel float64 `json:"reorder_level"`
	ReorderQty   float64 `json:"reorder_qty"`
}

// Summary returns on-hand and value for every active item (at one store, or
// across stores when storeID is empty).
func (s *Service) Summary(ctx context.Context, tenantID, storeID string) ([]SummaryItem, error) {
	args := []any{tenantID}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $2"
	}
	rows, err := s.DB.Query(ctx, `SELECT i.id, i.name, i.unit, i.reorder_level::float8, i.reorder_qty::float8,
		COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id=i.tenant_id AND b.item_id=i.id`+storeCond+`),0)::float8,
		COALESCE((SELECT sum(round(b.qty*b.avg_cost_paise)) FROM stock_balances b WHERE b.tenant_id=i.tenant_id AND b.item_id=i.id`+storeCond+`),0)::bigint
		FROM items i WHERE i.tenant_id=$1 AND i.status='active' ORDER BY lower(i.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SummaryItem{}
	for rows.Next() {
		var r SummaryItem
		if err := rows.Scan(&r.ItemID, &r.Name, &r.Unit, &r.ReorderLevel, &r.ReorderQty, &r.OnHand, &r.ValuePaise); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
