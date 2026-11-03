package kpi

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// DayKPI is one day's headline numbers. Sales are net of sale returns.
type DayKPI struct {
	Date            string  `json:"date"`
	SalesPaise      int64   `json:"sales_paise"`
	TaxablePaise    int64   `json:"taxable_paise"`
	TaxPaise        int64   `json:"tax_paise"`
	Bills           int64   `json:"bills"`
	Cancelled       int64   `json:"cancelled"`
	Returns         int64   `json:"returns"`       // sale returns (credit notes)
	ReturnsPaise    int64   `json:"returns_paise"` // their grand total, already taken off sales
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

// Card is an extra KPI card on the dashboard (e.g. GST 2B insight). The API
// layer adds cards; this package only carries them.
type Card struct {
	Kind     string         `json:"kind"`
	Title    string         `json:"title"`
	Value    string         `json:"value"`
	Severity string         `json:"severity"` // ok | info | warning | critical
	Text     string         `json:"text"`
	Link     string         `json:"link,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
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
	Cards       []Card           `json:"cards"`
	Source      string           `json:"source"` // redis | postgres
	GeneratedAt time.Time        `json:"generated_at"`
}

// snapshot is one consistent Postgres read of a (store, day).
type snapshot struct {
	seq      int64
	day      DayKPI
	allItems []TopItem // every item with sales that day (for a Redis rebuild)
	pay      map[string]int64
}

func (s *snapshot) top10() []TopItem {
	out := []TopItem{}
	for _, it := range s.allItems {
		if it.SalesPaise > 0 {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SalesPaise > out[j].SalesPaise })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// Dashboard builds the dashboard for store ("" or "all" = tenant-wide) on date
// (YYYY-MM-DD). Today's numbers come from Redis only when the Redis mirror is at
// the same version as Postgres; otherwise from Postgres (and Redis is rebuilt).
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
	out := &Dashboard{StoreID: store, Date: date, Source: "postgres", Cards: []Card{}, GeneratedAt: s.Now().UTC()}

	served := false
	if s.Redis != nil {
		pgSeq, err := s.version(ctx, tenant, store, date)
		if err != nil {
			return nil, err
		}
		rv, rerr := s.readRedis(ctx, tenant, store, date)
		if rerr != nil {
			s.Log.Warn("redis dashboard read failed", "error", rerr.Error())
		}
		if UseRedis(rv.have, rv.seq, pgSeq) {
			out.Today, out.TopItems, out.PaymentMix, out.Source = rv.day, rv.top, rv.pay, "redis"
			served = true
		} else {
			snap, err := s.snapshot(ctx, tenant, store, date)
			if err != nil {
				return nil, err
			}
			out.Today, out.TopItems, out.PaymentMix = snap.day, snap.top10(), snap.pay
			served = true
			if rerr == nil && snap.seq > 0 {
				s.rebuild(ctx, tenant, store, date, snap)
			}
		}
	}
	if !served {
		snap, err := s.snapshot(ctx, tenant, store, date)
		if err != nil {
			return nil, err
		}
		out.Today, out.TopItems, out.PaymentMix = snap.day, snap.top10(), snap.pay
	}
	if out.Yesterday, err = s.pgDay(ctx, tenant, store, yday); err != nil {
		return nil, err
	}
	if out.Yesterday.SalesPaise > 0 {
		c := math.Round(10000*float64(out.Today.SalesPaise-out.Yesterday.SalesPaise)/float64(out.Yesterday.SalesPaise)) / 100
		out.ChangePct = &c
	}
	if out.Last7Days, err = s.pgSeries(ctx, tenant, store, day.AddDate(0, 0, -6), day); err != nil {
		return nil, err
	}
	return out, nil
}

// UseRedis decides whether the Redis mirror may serve a day: only when it has
// a version and that version is the one Postgres committed last.
func UseRedis(have bool, redisSeq, pgSeq int64) bool {
	return have && pgSeq > 0 && redisSeq == pgSeq
}

func (s *Service) version(ctx context.Context, tenant, store, date string) (int64, error) {
	var seq int64
	err := s.DB.QueryRow(ctx, `SELECT COALESCE((SELECT seq FROM kpi_versions WHERE tenant_id=$1 AND store_key=$2 AND day=$3),0)`,
		tenant, store, date).Scan(&seq)
	return seq, err
}

// snapshot reads version, KPIs, item sales and payment mix in one
// repeatable-read transaction so a Redis rebuild matches its version exactly.
func (s *Service) snapshot(ctx context.Context, tenant, store, date string) (*snapshot, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	snap := &snapshot{day: DayKPI{Date: date}, pay: map[string]int64{}, allItems: []TopItem{}}
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT seq FROM kpi_versions WHERE tenant_id=$1 AND store_key=$2 AND day=$3),0)`,
		tenant, store, date).Scan(&snap.seq); err != nil {
		return nil, err
	}
	days, err := seriesRange(ctx, tx, tenant, store, date, date)
	if err != nil {
		return nil, err
	}
	if len(days) > 0 {
		snap.day = days[0]
	}
	f, extra := storeFilter(store)
	args := append([]any{tenant, date, date}, extra...)
	rows, err := tx.Query(ctx, `SELECT item_key, max(name), sum(sales_paise)::bigint, sum(qty)::float8
		FROM item_sales_daily WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+`
		GROUP BY item_key ORDER BY 3 DESC`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.ItemKey, &t.Name, &t.SalesPaise, &t.Qty); err != nil {
			rows.Close()
			return nil, err
		}
		snap.allItems = append(snap.allItems, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prow, err := tx.Query(ctx, `SELECT mode, sum(amount_paise)::bigint FROM payments_daily
		WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+` GROUP BY mode`, args...)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var mode string
		var amt int64
		if err := prow.Scan(&mode, &amt); err != nil {
			return nil, err
		}
		if amt != 0 {
			snap.pay[mode] = amt
		}
	}
	return snap, prow.Err()
}

// storeFilter returns SQL matching store_id (text compare; "all" matches everything).
func storeFilter(store string) (string, []any) {
	if store == AllStores {
		return "", nil
	}
	return " AND store_id::text = $4", []any{store}
}

func (s *Service) pgDay(ctx context.Context, tenant, store, date string) (DayKPI, error) {
	series, err := seriesRange(ctx, s.DB, tenant, store, date, date)
	if err != nil || len(series) == 0 {
		return DayKPI{Date: date}, err
	}
	return series[0], nil
}

func (s *Service) pgSeries(ctx context.Context, tenant, store string, from, to time.Time) ([]DayKPI, error) {
	rows, err := seriesRange(ctx, s.DB, tenant, store, from.Format(time.DateOnly), to.Format(time.DateOnly))
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

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func seriesRange(ctx context.Context, q querier, tenant, store, from, to string) ([]DayKPI, error) {
	f, extra := storeFilter(store)
	args := append([]any{tenant, from, to}, extra...)
	rows, err := q.Query(ctx, `SELECT day::text, sum(sales_paise)::bigint, sum(taxable_paise)::bigint, sum(tax_paise)::bigint, sum(bills)::bigint,
		sum(cancelled)::bigint, sum(margin_paise)::bigint, sum(margin_base_paise)::bigint, sum(returns)::bigint, sum(returns_paise)::bigint
		FROM sales_daily WHERE tenant_id=$1 AND day BETWEEN $2 AND $3`+f+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayKPI
	for rows.Next() {
		var k DayKPI
		if err := rows.Scan(&k.Date, &k.SalesPaise, &k.TaxablePaise, &k.TaxPaise, &k.Bills, &k.Cancelled,
			&k.MarginPaise, &k.MarginBasePaise, &k.Returns, &k.ReturnsPaise); err != nil {
			return nil, err
		}
		k.finish()
		out = append(out, k)
	}
	return out, rows.Err()
}

// Range sums KPIs between two dates (used by ask-your-books and weekly summaries).
func (s *Service) Range(ctx context.Context, tenant, store, from, to string) (DayKPI, []TopItem, error) {
	rows, err := seriesRange(ctx, s.DB, tenant, store, from, to)
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
		total.Returns += r.Returns
		total.ReturnsPaise += r.ReturnsPaise
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
