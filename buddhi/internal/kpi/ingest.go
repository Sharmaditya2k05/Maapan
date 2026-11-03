// Package kpi keeps the live dashboard numbers. Events are rolled up into
// Postgres (source of truth, idempotent via processed_events) and mirrored into
// Redis counters (hot path for the dashboard). Anomaly rules that need per-bill
// context also run here, inside the same transaction.
package kpi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/maapan/buddhi/internal/anomaly"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/logx"
	"github.com/maapan/mool/money"
)

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// AllStores is the pseudo store id for tenant-wide aggregates in Redis.
const AllStores = "all"

const redisTTL = 45 * 24 * time.Hour

// Service ingests events and serves the dashboard.
type Service struct {
	DB    *pgxpool.Pool
	Redis *redis.Client // nil → Postgres only
	Log   *slog.Logger
	Now   func() time.Time
}

// invLine extends the published line with fields aadesh may add later
// (discount_paise, price_includes_tax); they make discount checks exact.
type invLine struct {
	events.InvoiceLine
	DiscountPaise    *int64 `json:"discount_paise,omitempty"`
	PriceIncludesTax *bool  `json:"price_includes_tax,omitempty"`
}

type invoiceEvent struct {
	events.InvoicePayload
	Lines []invLine `json:"lines"`
}

func itemKey(l invLine) string {
	if l.ItemID != "" {
		return l.ItemID
	}
	return "name:" + strings.ToLower(strings.TrimSpace(l.Name))
}

// delta is what one invoice adds to (or, cancelled, removes from) the counters.
type delta struct {
	store, day            string
	sales, taxable, tax   int64
	bills, cancelled      int64
	margin, marginBase    int64
	returns, returnsPaise int64
	items                 map[string]itemDelta
	payments              map[string]int64

	// Versions of the (store, day) and (all, day) rollups after this delta,
	// set by bumpVersions inside the same transaction. The Redis mirror applies
	// the delta only on top of version-1, so a missed mirror is never hidden.
	seqStore, seqAll int64
}

type itemDelta struct {
	name  string
	qty   float64
	sales int64
}

// OnInvoiceCreated handles aadesh.invoice.created.
func (s *Service) OnInvoiceCreated(ctx context.Context, e events.Envelope) error {
	var inv invoiceEvent
	if err := json.Unmarshal(e.Data, &inv); err != nil {
		logx.From(ctx).Error("bad invoice payload", "error", err.Error())
		return nil
	}
	var d *delta
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		switch inv.DocType {
		case "purchase":
			if err := s.recordCosts(ctx, tx, e.TenantID, inv); err != nil {
				return err
			}
			return s.checkGSTIN(ctx, tx, e.TenantID, inv)
		case "sale":
			d, err = s.addSale(ctx, tx, e.TenantID, inv)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mirror(ctx, e.TenantID, d)
	return nil
}

// OnNoteCreated handles aadesh.note.created. A sale return (credit note)
// reduces net sales, GST, margin, item sales and — for refunds paid at the
// counter — the payment mix on the note's date, and is counted as a return.
// Purchase returns (debit notes) do not touch sales KPIs.
func (s *Service) OnNoteCreated(ctx context.Context, e events.Envelope) error {
	var inv invoiceEvent
	if err := json.Unmarshal(e.Data, &inv); err != nil {
		logx.From(ctx).Error("bad note payload", "error", err.Error())
		return nil
	}
	if inv.DocType != "sale_return" || inv.StoreID == "" {
		return nil
	}
	var d *delta
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		d, err = s.addReturn(ctx, tx, e.TenantID, inv)
		return err
	})
	if err != nil {
		return err
	}
	s.mirror(ctx, e.TenantID, d)
	return nil
}

// OnInvoiceCancelled handles aadesh.invoice.cancelled for sales and sale returns.
func (s *Service) OnInvoiceCancelled(ctx context.Context, e events.Envelope) error {
	var inv invoiceEvent
	if err := json.Unmarshal(e.Data, &inv); err != nil {
		return nil
	}
	if inv.DocType != "sale" && inv.DocType != "sale_return" {
		return nil
	}
	var d *delta
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		var docType, store, day string
		var dd delta
		err = tx.QueryRow(ctx, `UPDATE invoice_facts SET cancelled=true
			WHERE tenant_id=$1 AND invoice_id=$2 AND NOT cancelled
			RETURNING doc_type, store_id::text, day::text, grand_total_paise, taxable_paise, tax_paise, margin_paise, margin_base_paise`,
			e.TenantID, inv.InvoiceID).Scan(&docType, &store, &day, &dd.sales, &dd.taxable, &dd.tax, &dd.margin, &dd.marginBase)
		if db.IsNotFound(err) {
			return nil // never counted (e.g. created before buddhi was deployed)
		}
		if err != nil {
			return err
		}
		dd.store, dd.day = store, day
		var rev delta
		if docType == "sale_return" {
			// Facts of a return are stored negative; cancelling it adds them back.
			rev = dd.negate()
			rev.returns, rev.returnsPaise = -1, dd.sales // dd.sales = -grand
			rev.items, rev.payments = lineDeltas(inv), tenderDeltas(inv)
		} else {
			dd.bills, dd.cancelled = 1, -1 // negated below
			dd.items, dd.payments = lineDeltas(inv), tenderDeltas(inv)
			rev = dd.negate()
		}
		if err := applyDelta(ctx, tx, e.TenantID, &rev); err != nil {
			return err
		}
		d = &rev
		return nil
	})
	if err != nil {
		return err
	}
	s.mirror(ctx, e.TenantID, d)
	return nil
}

// OnPaymentCaptured adds gateway payments to the payment mix.
func (s *Service) OnPaymentCaptured(ctx context.Context, e events.Envelope) error {
	var p events.PaymentPayload
	if err := e.Decode(&p); err != nil {
		return nil
	}
	day := s.Now().In(IST)
	if t, err := time.Parse(time.RFC3339, p.CapturedAt); err == nil {
		day = t.In(IST)
	}
	store := e.StoreID
	if store == "" {
		store = "unassigned"
	}
	mode := "online_" + strings.ToLower(strings.TrimSpace(p.Method))
	if p.Method == "" {
		mode = "online"
	}
	d := &delta{store: store, day: day.Format(time.DateOnly), payments: map[string]int64{mode: p.AmountPaise}}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			d = nil
			return err
		}
		if err := upsertPayments(ctx, tx, e.TenantID, d); err != nil {
			return err
		}
		return bumpVersions(ctx, tx, e.TenantID, d)
	})
	if err != nil {
		return err
	}
	s.mirror(ctx, e.TenantID, d)
	return nil
}

// OnStockLow remembers bhandar's low-stock signal for re-order suggestions.
func (s *Service) OnStockLow(ctx context.Context, e events.Envelope) error {
	var p events.StockLowPayload
	if err := e.Decode(&p); err != nil || p.ItemID == "" || p.StoreID == "" {
		return nil
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO stock_alerts (tenant_id, store_id, item_id, item_name, on_hand, reorder_level, reorder_qty, supplier_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
		ON CONFLICT (tenant_id, store_id, item_id) DO UPDATE SET item_name=$4, on_hand=$5, reorder_level=$6,
			reorder_qty=$7, supplier_id=NULLIF($8,''), alerted_at=now()`,
		e.TenantID, p.StoreID, p.ItemID, p.ItemName, p.OnHand, p.ReorderLevel, p.ReorderQty, p.SupplierID)
	return err
}

func (d delta) negate() delta {
	n := d
	n.sales, n.taxable, n.tax, n.margin, n.marginBase = -d.sales, -d.taxable, -d.tax, -d.margin, -d.marginBase
	n.bills, n.cancelled = -d.bills, -d.cancelled
	n.returns, n.returnsPaise = -d.returns, -d.returnsPaise
	n.items = map[string]itemDelta{}
	for k, v := range d.items {
		n.items[k] = itemDelta{name: v.name, qty: -v.qty, sales: -v.sales}
	}
	n.payments = map[string]int64{}
	for k, v := range d.payments {
		n.payments[k] = -v
	}
	return n
}

func lineDeltas(inv invoiceEvent) map[string]itemDelta {
	out := map[string]itemDelta{}
	for _, l := range inv.Lines {
		k := itemKey(l)
		cur := out[k]
		cur.name = l.Name
		cur.qty += l.Qty
		cur.sales += l.TotalPaise
		out[k] = cur
	}
	return out
}

func tenderDeltas(inv invoiceEvent) map[string]int64 {
	out := map[string]int64{}
	remaining := inv.GrandTotalPaise
	for _, t := range inv.Tenders {
		if t.Mode == "credit" || remaining <= 0 {
			continue
		}
		amt := min(t.AmountPaise, remaining) // cash above the bill is change, not revenue
		out[t.Mode] += amt
		remaining -= amt
	}
	return out
}

func (s *Service) addSale(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) (*delta, error) {
	tax := inv.CGSTPaise + inv.SGSTPaise + inv.IGSTPaise + inv.CessPaise
	day := inv.InvoiceDate
	if day == "" {
		day = s.Now().In(IST).Format(time.DateOnly)
	}

	margin, base, err := marginOf(ctx, tx, tenantID, inv)
	if err != nil {
		return nil, err
	}
	alines := make([]anomaly.Line, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		alines = append(alines, anomaly.Line{Qty: l.Qty, UnitPricePaise: l.UnitPricePaise, TaxablePaise: l.TaxablePaise,
			TotalPaise: l.TotalPaise, DiscountPaise: l.DiscountPaise, PriceIncludesTax: l.PriceIncludesTax})
	}
	discount, gross := anomaly.Discount(alines)

	tag, err := tx.Exec(ctx, `INSERT INTO invoice_facts (tenant_id, invoice_id, store_id, doc_type, number, day, created_by,
		party_id, party_gstin, grand_total_paise, taxable_paise, tax_paise, discount_paise, gross_paise, margin_paise, margin_base_paise)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14,$15,$16) ON CONFLICT DO NOTHING`,
		tenantID, inv.InvoiceID, inv.StoreID, inv.DocType, inv.Number, day, inv.CreatedBy, inv.PartyID, inv.PartyGSTIN,
		inv.GrandTotalPaise, inv.TaxablePaise, tax, discount, gross, margin, base)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil // same invoice delivered under a new event id
	}
	d := delta{store: inv.StoreID, day: day, sales: inv.GrandTotalPaise, taxable: inv.TaxablePaise, tax: tax,
		bills: 1, margin: margin, marginBase: base, items: lineDeltas(inv), payments: tenderDeltas(inv)}
	if err := applyDelta(ctx, tx, tenantID, &d); err != nil {
		return nil, err
	}

	if err := s.checkDiscount(ctx, tx, tenantID, inv, discount, gross); err != nil {
		return nil, err
	}
	if err := s.checkGSTIN(ctx, tx, tenantID, inv); err != nil {
		return nil, err
	}
	if err := s.checkHSNRates(ctx, tx, tenantID, inv); err != nil {
		return nil, err
	}
	return &d, nil
}

// marginOf is the gross margin over lines whose purchase cost we know, and the
// taxable value of those lines.
func marginOf(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) (margin, base int64, err error) {
	var ids []string
	for _, l := range inv.Lines {
		if l.ItemID != "" {
			ids = append(ids, l.ItemID)
		}
	}
	if len(ids) == 0 {
		return 0, 0, nil
	}
	costs := map[string]int64{}
	rows, err := tx.Query(ctx, `SELECT item_id::text, unit_cost_paise FROM item_costs WHERE tenant_id=$1 AND item_id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var id string
		var c int64
		if err := rows.Scan(&id, &c); err != nil {
			rows.Close()
			return 0, 0, err
		}
		costs[id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, l := range inv.Lines {
		if c, ok := costs[l.ItemID]; ok {
			margin += l.TaxablePaise - int64(math.Round(l.Qty*float64(c)))
			base += l.TaxablePaise
		}
	}
	return margin, base, nil
}

// addReturn books a sale return (credit note). Its facts are stored with
// negative amounts so a later cancellation can reverse exactly what was taken off.
func (s *Service) addReturn(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) (*delta, error) {
	tax := inv.CGSTPaise + inv.SGSTPaise + inv.IGSTPaise + inv.CessPaise
	day := inv.InvoiceDate
	if day == "" {
		day = s.Now().In(IST).Format(time.DateOnly)
	}
	margin, base, err := marginOf(ctx, tx, tenantID, inv)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO invoice_facts (tenant_id, invoice_id, store_id, doc_type, number, day, created_by,
		party_id, party_gstin, grand_total_paise, taxable_paise, tax_paise, margin_paise, margin_base_paise)
		VALUES ($1,$2,$3,'sale_return',$4,$5,$6,NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`,
		tenantID, inv.InvoiceID, inv.StoreID, inv.Number, day, inv.CreatedBy, inv.PartyID, inv.PartyGSTIN,
		-inv.GrandTotalPaise, -inv.TaxablePaise, -tax, -margin, -base)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	up := delta{store: inv.StoreID, day: day, sales: inv.GrandTotalPaise, taxable: inv.TaxablePaise, tax: tax,
		margin: margin, marginBase: base, items: lineDeltas(inv), payments: tenderDeltas(inv)}
	d := up.negate()
	d.returns, d.returnsPaise = 1, inv.GrandTotalPaise
	if err := applyDelta(ctx, tx, tenantID, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func applyDelta(ctx context.Context, tx pgx.Tx, tenantID string, d *delta) error {
	_, err := tx.Exec(ctx, `INSERT INTO sales_daily (tenant_id, store_id, day, sales_paise, taxable_paise, tax_paise, bills, cancelled,
			margin_paise, margin_base_paise, returns, returns_paise)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (tenant_id, store_id, day) DO UPDATE SET
			sales_paise = sales_daily.sales_paise + $4, taxable_paise = sales_daily.taxable_paise + $5,
			tax_paise = sales_daily.tax_paise + $6, bills = sales_daily.bills + $7, cancelled = sales_daily.cancelled + $8,
			margin_paise = sales_daily.margin_paise + $9, margin_base_paise = sales_daily.margin_base_paise + $10,
			returns = sales_daily.returns + $11, returns_paise = sales_daily.returns_paise + $12`,
		tenantID, d.store, d.day, d.sales, d.taxable, d.tax, d.bills, d.cancelled, d.margin, d.marginBase, d.returns, d.returnsPaise)
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for k, it := range d.items {
		var itemID any
		if !strings.HasPrefix(k, "name:") {
			itemID = k
		}
		b.Queue(`INSERT INTO item_sales_daily (tenant_id, store_id, item_key, day, item_id, name, qty, sales_paise)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, store_id, item_key, day) DO UPDATE SET name=$6,
				qty = item_sales_daily.qty + $7, sales_paise = item_sales_daily.sales_paise + $8`,
			tenantID, d.store, k, d.day, itemID, it.name, it.qty, it.sales)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return err
	}
	if err := upsertPayments(ctx, tx, tenantID, d); err != nil {
		return err
	}
	return bumpVersions(ctx, tx, tenantID, d)
}

// bumpVersions increments the rollup versions of d's store and of the
// tenant-wide aggregate for d's day, recording the new values on d.
func bumpVersions(ctx context.Context, tx pgx.Tx, tenantID string, d *delta) error {
	bump := func(store string) (int64, error) {
		var seq int64
		err := tx.QueryRow(ctx, `INSERT INTO kpi_versions (tenant_id, store_key, day, seq) VALUES ($1,$2,$3,1)
			ON CONFLICT (tenant_id, store_key, day) DO UPDATE SET seq = kpi_versions.seq + 1 RETURNING seq`,
			tenantID, store, d.day).Scan(&seq)
		return seq, err
	}
	var err error
	if d.seqStore, err = bump(d.store); err != nil {
		return err
	}
	d.seqAll, err = bump(AllStores)
	return err
}

func upsertPayments(ctx context.Context, tx pgx.Tx, tenantID string, d *delta) error {
	b := &pgx.Batch{}
	for mode, amt := range d.payments {
		b.Queue(`INSERT INTO payments_daily (tenant_id, store_id, day, mode, amount_paise) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_id, store_id, day, mode) DO UPDATE SET amount_paise = payments_daily.amount_paise + $5`,
			tenantID, d.store, d.day, mode, amt)
	}
	return tx.SendBatch(ctx, b).Close()
}

func (s *Service) recordCosts(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) error {
	b := &pgx.Batch{}
	for _, l := range inv.Lines {
		if l.ItemID == "" || l.Qty <= 0 {
			continue
		}
		cost := int64(math.Round(float64(l.TaxablePaise) / l.Qty))
		b.Queue(`INSERT INTO item_costs (tenant_id, item_id, unit_cost_paise) VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id, item_id) DO UPDATE SET unit_cost_paise=$3, updated_at=now()`, tenantID, l.ItemID, cost)
	}
	return tx.SendBatch(ctx, b).Close()
}

func (s *Service) checkDiscount(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent, discount, gross int64) error {
	if gross <= 0 || inv.CreatedBy == "" {
		return nil
	}
	pct := anomaly.Pct(discount, gross)
	var st anomaly.Stats
	err := tx.QueryRow(ctx, `SELECT n, mean, m2 FROM cashier_discount_stats WHERE tenant_id=$1 AND cashier_id=$2 FOR UPDATE`,
		tenantID, inv.CreatedBy).Scan(&st.N, &st.Mean, &st.M2)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if anomaly.IsDiscountOutlier(st, pct) {
		msg := fmt.Sprintf("Bill %s has a %.1f%% discount (%s) — this cashier usually gives %.1f%% ± %.1f%%.",
			inv.Number, pct, money.Format(discount), st.Mean, st.StdDev())
		if err := insertAnomaly(ctx, tx, tenantID, "unusual_discount", "warning", inv.StoreID, inv.CreatedBy, msg, inv.InvoiceID,
			map[string]any{"invoice_id": inv.InvoiceID, "number": inv.Number, "discount_pct": round2(pct), "discount_paise": discount,
				"cashier_mean_pct": round2(st.Mean), "cashier_sd_pct": round2(st.StdDev())}); err != nil {
			return err
		}
	}
	st = st.Update(pct)
	_, err = tx.Exec(ctx, `INSERT INTO cashier_discount_stats (tenant_id, cashier_id, n, mean, m2) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, cashier_id) DO UPDATE SET n=$3, mean=$4, m2=$5`, tenantID, inv.CreatedBy, st.N, st.Mean, st.M2)
	return err
}

func (s *Service) checkGSTIN(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) error {
	kind := anomaly.GSTINCheck(inv.DocType, inv.PartyID, inv.PartyGSTIN, inv.GrandTotalPaise, gst.ValidateGSTIN)
	if kind == "" {
		return nil
	}
	msg := fmt.Sprintf("%s %s to %s (%s) has no GSTIN.", docLabel(inv.DocType), inv.Number, inv.PartyName, money.Format(inv.GrandTotalPaise))
	sev := "warning"
	if kind == "invalid_gstin" {
		msg = fmt.Sprintf("%s %s has an invalid party GSTIN %q.", docLabel(inv.DocType), inv.Number, inv.PartyGSTIN)
		sev = "critical"
	} else if inv.DocType == "purchase" {
		msg = fmt.Sprintf("Purchase bill %s from %s has no supplier GSTIN — input tax credit cannot be claimed.", inv.Number, inv.PartyName)
	}
	return insertAnomaly(ctx, tx, tenantID, kind, sev, inv.StoreID, inv.Number, msg, inv.InvoiceID,
		map[string]any{"invoice_id": inv.InvoiceID, "number": inv.Number, "party_id": inv.PartyID, "doc_type": inv.DocType})
}

func (s *Service) checkHSNRates(ctx context.Context, tx pgx.Tx, tenantID string, inv invoiceEvent) error {
	seen := map[string]bool{}
	for _, l := range inv.Lines {
		hsn := strings.TrimSpace(l.HSN)
		key := fmt.Sprintf("%s/%d", hsn, l.TaxRateBP)
		if hsn == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(ctx, `INSERT INTO hsn_rates (tenant_id, hsn, tax_rate_bp, uses, last_number) VALUES ($1,$2,$3,1,$4)
			ON CONFLICT (tenant_id, hsn, tax_rate_bp) DO UPDATE SET uses = hsn_rates.uses + 1, last_number=$4, last_seen=now()`,
			tenantID, hsn, l.TaxRateBP, inv.Number); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT tax_rate_bp FROM hsn_rates WHERE tenant_id=$1 AND hsn=$2 ORDER BY tax_rate_bp`, tenantID, hsn)
		if err != nil {
			return err
		}
		var rates []int
		for rows.Next() {
			var r int
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return err
			}
			rates = append(rates, r)
		}
		rows.Close()
		if len(rates) < 2 {
			continue
		}
		pcts := make([]string, len(rates))
		for i, r := range rates {
			pcts[i] = fmt.Sprintf("%g%%", float64(r)/100)
		}
		msg := fmt.Sprintf("HSN %s is billed at different GST rates (%s); latest on bill %s. Check the item master.",
			hsn, strings.Join(pcts, ", "), inv.Number)
		dedupe := hsn + ":" + strings.Join(pcts, ",")
		if err := insertAnomaly(ctx, tx, tenantID, "hsn_rate_mismatch", "warning", inv.StoreID, hsn, msg, dedupe,
			map[string]any{"hsn": hsn, "rates_bp": slices.Clone(rates), "latest_invoice": inv.Number}); err != nil {
			return err
		}
	}
	return nil
}

func insertAnomaly(ctx context.Context, tx pgx.Tx, tenantID, kind, severity, storeID, subject, message, dedupe string, details map[string]any) error {
	b, _ := json.Marshal(details)
	_, err := tx.Exec(ctx, `INSERT INTO anomalies (tenant_id, kind, severity, store_id, subject, message, details, dedupe_key)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8) ON CONFLICT (tenant_id, kind, dedupe_key) DO NOTHING`,
		tenantID, kind, severity, storeID, subject, message, b, dedupe)
	return err
}

func docLabel(docType string) string {
	if docType == "purchase" {
		return "Purchase bill"
	}
	return "Sale invoice"
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
