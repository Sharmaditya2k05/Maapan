package kpi

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// DayKPI is one day's headline numbers.
type DayKPI struct {
	Date            string  `json:"date"`
	SalesPaise      int64   `json:"sales_paise"`
	TaxablePaise    int64   `json:"taxable_paise"`
	TaxPaise        int64   `json:"tax_paise"`
	Bills           int64   `json:"bills"`
	Cancelled       int64   `json:"cancelled"`
	AvgBillPaise    int64   `json:"avg_bill_paise"`
	MarginPaise     int64   `json:"gross_margin_paise"`
	MarginBasePaise int64   `json:"margin_base_paise"` // taxable value with known cost
	MarginPct       float64 `json:"gross_margin_pct"`
}

func (k *DayKPI) finish() {
	if k.Bills > 0 {
		k.AvgBillPaise = k.SalesPaise / k.Bills
	}
	if k.MarginBasePaise > 0 {
		k.MarginPct = math.Round(10000*float64(k.MarginPaise)/float64(k.MarginBasePaise)) / 100
	}
}

// TopItem is a best seller of the day.
type TopItem struct {
	ItemKey    string  `json:"item_key"`
	Name       string  `json:"name"`
	SalesPaise int64   `json:"sales_paise"`
	Qty        float64 `json:"qty,omitempty"`
}

// Dashboard is GET /v1/dashboard.
type Dashboard struct {
	StoreID     string           `json:"store_id"` // "all" = whole business
	Date        string           `json:"date"`
	Today       DayKPI           `json:"today"`
	Yesterday   DayKPI           `json:"yesterday"`
	ChangePct   *float64         `json:"sales_change_pct,omitempty"`
	Last7Days   []DayKPI         `json:"last_7_days"`
	TopItems    []TopItem        `json:"top_items"`
	PaymentMix  map[string]int64 `json:"payment_mix_paise"`
	Source      string           `json:"source"` // redis | postgres
	GeneratedAt time.Time        `json:"generated_at"`
}

// Dashboard builds the dashboard for store ("" or "all" = tenant-wide) on date (YYYY-MM-DD).
func (s *Service) Dashboard(ctx context.Context, tenant, store, date string) (*Dashboard, error) {
	if store == "" {
		store = AllStores
	}
	day, err := time.ParseInLocation(time.DateOnly, date, IST)
	if err != nil {
		day = s.Now().In(IST)
		date = day.Format(time.DateOnly)
	}
	yday := day.AddDate(0, 0, -1).Format(time.DateOnly)
	out := &Dashboard{StoreID: store, Date: date, Source: "postgres", GeneratedAt: s.Now().UTC()}

	usedRedis := false
	if s.Redis != nil {
		t, okT := s.redisDay(ctx, tenant, store, date)
		y, okY := s.redisDay(ctx, tenant, store, yday)
		if okT {
			out.Today, usedRedis = t, true
			if okY {
				out.Yesterday = y
			} else if out.Yesterday, err = s.pgDay(ctx, tenant, store, yday); err != nil {
				return nil, err
			}
		}
	}
	if !usedRedis {
		if out.Today, err = s.pgDay(ctx, tenant, store, date); err != nil {
			return nil, err
		}
		if out.Yesterday, err = s.pgDay(ctx, tenant, store, yday); err != nil {
			return nil, err
		}
	} else {
		out.Source = "redis"
	}
	if out.Yesterday.SalesPaise > 0 {
		c := math.Round(10000*float64(out.Today.SalesPaise-out.Yesterday.SalesPaise)/float64(out.Yesterday.SalesPaise)) / 100
		out.ChangePct = &c
	}

	if out.Last7Days, err = s.pgSeries(ctx, tenant, store, day.AddDate(0, 0, -6), day); err != nil {
		return nil, err
	}
	if out.TopItems, err = s.topItems(ctx, tenant, store, date); err != nil {
		return nil, err
	}
	if out.PaymentMix, err = s.paymentMix(ctx, tenant, store, date); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) redisDay(ctx context.Context, tenant, store, date string) (DayKPI, bool) {
	m, err := s.Redis.HGetAll(ctx, kpiKey(tenant, store, date)).Result()
	if err != nil || len(m) == 0 {
		return DayKPI{}, false
	}
	n := func(k string) int64 { v, _ := strconv.ParseInt(m[k], 10, 64); return v }
	k := DayKPI{Date: date, SalesPaise: n("sales_paise"), TaxablePaise: n("taxable_paise"), TaxPaise: n("tax_paise"),
		Bills: n("bills"), Cancelled: n("cancelled"), MarginPaise: n("margin_paise"), MarginBasePaise: n("margin_base_paise")}
	k.finish()
	return k, true
}

// storeFilter returns SQL matching store_id (text compare; "all" matches everything).
func storeFilter(store string) (string, []any) {
	if store == AllStores {
		return "", nil
	}
	return " AND store_id::text = $4", []any{store}
}

func (s *Service) pgDay(ctx context.Context, tenant, store, date string) (DayKPI, error) {
	series, err := s.pgSeriesRange(ctx, tenant, store, date, date)
	if err != nil || len(series) == 0 {
		return DayKPI{Date: date}, err
	}
	return series[0], nil
}

func (s *Service) pgSeries(ctx context.Context, tenant, store string, from, to time.Time) ([]DayKPI, error) {
	rows, err := s.pgSeriesRange(ctx, tenant, store, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	byDay := map[string]DayKPI{}
	for _, r := range rows {
		byDay[r.Date] = r
	}
	var out []DayKPI
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		ds := d.Format(time.DateOnly)
		k, ok := byDay[ds]
		if !ok {
			k = DayKPI{Date: ds}
		}
		out = append(out, k)
	}
	return out, nil
}

func (s *Service) pgSeriesRange(ctx context.Context, tenant, store, from, to string) ([]DayKPI, error) {
	f, extra := storeFilter(store)
	args := append([]any{tenant, from, to}, extra...)
	rows, err := s.DB.Query(ctx, `SELECT day::text, sum(sales_paise)::bigint, sum(taxable_paise)::bigint, sum(tax_paise)::bigint, sum(bills)::bigint,
		sum(cancelled)::bigint, sum(margin_paise)::bigint, sum(margin_base_paise)::bigint
		FROM sales_daily WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayKPI
	for rows.Next() {
		var k DayKPI
		if err := rows.Scan(&k.Date, &k.SalesPaise, &k.TaxablePaise, &k.TaxPaise, &k.Bills, &k.Cancelled,
			&k.MarginPaise, &k.MarginBasePaise); err != nil {
			return nil, err
		}
		k.finish()
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Service) topItems(ctx context.Context, tenant, store, date string) ([]TopItem, error) {
	if s.Redis != nil {
		zs, err := s.Redis.ZRevRangeWithScores(ctx, topKey(tenant, store, date), 0, 9).Result()
		if err == nil && len(zs) > 0 {
			keys := make([]string, len(zs))
			for i, z := range zs {
				keys[i], _ = z.Member.(string)
			}
			names, _ := s.Redis.HMGet(ctx, nameKey(tenant), keys...).Result()
			out := make([]TopItem, 0, len(zs))
			for i, z := range zs {
				if z.Score <= 0 {
					continue
				}
				name, _ := names[i].(string)
				out = append(out, TopItem{ItemKey: keys[i], Name: name, SalesPaise: int64(z.Score)})
			}
			return out, nil
		} else if err != nil && err != redis.Nil {
			s.Log.Warn("redis top items", "error", err.Error())
		}
	}
	f, extra := storeFilter(store)
	args := append([]any{tenant, date, date}, extra...)
	rows, err := s.DB.Query(ctx, `SELECT item_key, max(name), sum(sales_paise)::bigint, sum(qty)::float8
		FROM item_sales_daily WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+`
		GROUP BY item_key HAVING sum(sales_paise)::bigint > 0 ORDER BY 3 DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopItem{}
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.ItemKey, &t.Name, &t.SalesPaise, &t.Qty); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) paymentMix(ctx context.Context, tenant, store, date string) (map[string]int64, error) {
	out := map[string]int64{}
	if s.Redis != nil {
		m, err := s.Redis.HGetAll(ctx, payKey(tenant, store, date)).Result()
		if err == nil && len(m) > 0 {
			for k, v := range m {
				if n, _ := strconv.ParseInt(v, 10, 64); n != 0 {
					out[k] = n
				}
			}
			return out, nil
		}
	}
	f, extra := storeFilter(store)
	args := append([]any{tenant, date, date}, extra...)
	rows, err := s.DB.Query(ctx, `SELECT mode, sum(amount_paise)::bigint FROM payments_daily
		WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+` GROUP BY mode`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mode string
		var amt int64
		if err := rows.Scan(&mode, &amt); err != nil {
			return nil, err
		}
		if amt != 0 {
			out[mode] = amt
		}
	}
	return out, rows.Err()
}

// Range sums KPIs between two dates (used by ask-your-books and weekly summaries).
func (s *Service) Range(ctx context.Context, tenant, store, from, to string) (DayKPI, []TopItem, error) {
	rows, err := s.pgSeriesRange(ctx, tenant, store, from, to)
	if err != nil {
		return DayKPI{}, nil, err
	}
	total := DayKPI{Date: from + ".." + to}
	for _, r := range rows {
		total.SalesPaise += r.SalesPaise
		total.TaxablePaise += r.TaxablePaise
		total.TaxPaise += r.TaxPaise
		total.Bills += r.Bills
		total.Cancelled += r.Cancelled
		total.MarginPaise += r.MarginPaise
		total.MarginBasePaise += r.MarginBasePaise
	}
	total.finish()
	f, extra := storeFilter(store)
	args := append([]any{tenant, from, to}, extra...)
	trows, err := s.DB.Query(ctx, `SELECT item_key, max(name), sum(sales_paise)::bigint, sum(qty)::float8
		FROM item_sales_daily WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+`
		GROUP BY item_key HAVING sum(sales_paise)::bigint > 0 ORDER BY 3 DESC LIMIT 10`, args...)
	if err != nil {
		return total, nil, err
	}
	defer trows.Close()
	var top []TopItem
	for trows.Next() {
		var t TopItem
		if err := trows.Scan(&t.ItemKey, &t.Name, &t.SalesPaise, &t.Qty); err != nil {
			return total, nil, err
		}
		top = append(top, t)
	}
	return total, top, trows.Err()
}
