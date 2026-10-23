package inventory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// MoveLine is one requested stock movement before costing.
type MoveLine struct {
	LineNo        int
	ItemID        string
	BatchNo       string
	MfgDate       string // YYYY-MM-DD, optional: recorded on the batch
	ExpiryDate    string // YYYY-MM-DD, optional: recorded on the batch
	BatchMRPPaise *int64
	Qty           float64 // signed: positive into the store, negative out; expressed in Unit
	Unit          string  // "" = the item's base unit
	UnitCostPaise int64   // cost per base unit for inbound lines
	ValuePaise    int64   // alternative to UnitCostPaise: total cost of the line (e.g. invoice taxable value)
	Note          string
}

// postRequest is one source document's worth of movements.
type postRequest struct {
	TenantID   string
	StoreID    string
	Source     string
	SourceID   string
	Reason     string
	CreatedBy  string
	Lines      []MoveLine
	Reversal   bool
	OccurredAt time.Time
}

type itemLite struct {
	ID, Name, Unit      string
	TrackBatches        bool
	ReorderLevel        float64
	ReorderQty          float64
	PreferredSupplierID string
	PurchasePricePaise  int64
	Alts                []AltUnit
}

func loadItemLite(ctx context.Context, q db.Querier, tenantID, id string) (*itemLite, error) {
	var it itemLite
	var sup *string
	err := q.QueryRow(ctx, `SELECT id, name, unit, track_batches, reorder_level::float8, reorder_qty::float8, preferred_supplier_id,
		purchase_price_paise FROM items WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&it.ID, &it.Name, &it.Unit, &it.TrackBatches, &it.ReorderLevel, &it.ReorderQty, &sup, &it.PurchasePricePaise)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("item %s not found", id)
		}
		return nil, err
	}
	it.PreferredSupplierID = deref(sup)
	rows, err := q.Query(ctx, `SELECT unit, factor::float8 FROM item_units WHERE tenant_id=$1 AND item_id=$2`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AltUnit
		if err := rows.Scan(&a.Unit, &a.Factor); err != nil {
			return nil, err
		}
		it.Alts = append(it.Alts, a)
	}
	return &it, rows.Err()
}

// upsertBatch creates or enriches a batch and returns its id.
func upsertBatch(ctx context.Context, tx pgx.Tx, tenantID, itemID string, l MoveLine) (string, error) {
	var mfg, exp *time.Time
	for _, p := range []struct {
		s   string
		dst **time.Time
	}{{l.MfgDate, &mfg}, {l.ExpiryDate, &exp}} {
		if p.s == "" {
			continue
		}
		t, err := time.Parse(time.DateOnly, p.s)
		if err != nil {
			return "", httpx.Validation("batch dates must be YYYY-MM-DD")
		}
		*p.dst = &t
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO batches (tenant_id, item_id, batch_no, mfg_date, expiry_date, mrp_paise)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, item_id, batch_no) DO UPDATE SET
			mfg_date = COALESCE(EXCLUDED.mfg_date, batches.mfg_date),
			expiry_date = COALESCE(EXCLUDED.expiry_date, batches.expiry_date),
			mrp_paise = COALESCE(EXCLUDED.mrp_paise, batches.mrp_paise)
		RETURNING id`, tenantID, itemID, strings.TrimSpace(l.BatchNo), mfg, exp, l.BatchMRPPaise).Scan(&id)
	return id, err
}

// post writes the movements of one document inside tx: it locks each
// store/item/batch position, applies weighted-average costing, inserts the
// ledger rows idempotently on (source, source_id, line_no), and enqueues
// bhandar.stock.moved plus any bhandar.stock.low alerts. Lines that were
// already posted are skipped, so redelivered events are harmless.
func (s *Service) post(ctx context.Context, tx pgx.Tx, req postRequest) ([]Movement, error) {
	if req.StoreID == "" {
		return nil, httpx.Validation("store_id is required")
	}
	lines := append([]MoveLine(nil), req.Lines...)
	// Lock positions in a stable order so concurrent documents cannot deadlock.
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].ItemID != lines[j].ItemID {
			return lines[i].ItemID < lines[j].ItemID
		}
		return lines[i].BatchNo < lines[j].BatchNo
	})
	items := map[string]*itemLite{}
	var out []Movement
	touched := map[string]*itemLite{}
	for _, l := range lines {
		if l.ItemID == "" || l.Qty == 0 {
			continue
		}
		it := items[l.ItemID]
		if it == nil {
			var err error
			if it, err = loadItemLite(ctx, tx, req.TenantID, l.ItemID); err != nil {
				return nil, err
			}
			items[l.ItemID] = it
		}
		qty, known := ToBaseUnit(l.Qty, l.Unit, it.Unit, it.Alts)
		if !known {
			s.Log.Warn("unknown unit on stock line, treated as base unit", "item_id", it.ID, "unit", l.Unit, "source", req.Source, "source_id", req.SourceID)
		}
		qty = Q3(qty)
		if qty == 0 {
			continue
		}
		unitCost := l.UnitCostPaise
		if unitCost == 0 && l.ValuePaise != 0 {
			unitCost = int64(math.Round(float64(l.ValuePaise) / math.Abs(qty)))
		}
		if unitCost == 0 && qty < 0 {
			unitCost = it.PurchasePricePaise // only used when the position has no cost yet
		}
		batchID := NilUUID
		if strings.TrimSpace(l.BatchNo) != "" {
			id, err := upsertBatch(ctx, tx, req.TenantID, it.ID, l)
			if err != nil {
				return nil, err
			}
			batchID = id
		}
		// Lock the position.
		if _, err := tx.Exec(ctx, `INSERT INTO stock_balances (tenant_id, store_id, item_id, batch_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			req.TenantID, req.StoreID, it.ID, batchID); err != nil {
			return nil, err
		}
		var curQty float64
		var curAvg int64
		if err := tx.QueryRow(ctx, `SELECT qty::float8, avg_cost_paise FROM stock_balances WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3 AND batch_id=$4 FOR UPDATE`,
			req.TenantID, req.StoreID, it.ID, batchID).Scan(&curQty, &curAvg); err != nil {
			return nil, err
		}
		newQty, newAvg, applied := Apply(curQty, curAvg, qty, unitCost)
		var dbBatch *string
		if batchID != NilUUID {
			dbBatch = &batchID
		}
		var movID string
		err := tx.QueryRow(ctx, `INSERT INTO stock_movements (tenant_id, store_id, item_id, batch_id, qty, unit_cost_paise, reason,
			source, source_id, line_no, note, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (tenant_id, source, source_id, line_no) DO NOTHING RETURNING id`,
			req.TenantID, req.StoreID, it.ID, dbBatch, qty, applied, req.Reason, req.Source, req.SourceID, l.LineNo,
			nilIfEmpty(l.Note), orSystem(req.CreatedBy)).Scan(&movID)
		if db.IsNotFound(err) {
			continue // already posted by an earlier delivery of the same document
		}
		if err != nil {
			return nil, err
		}
		inAt, outAt := "last_in_at", "last_out_at"
		col := outAt
		if qty > 0 {
			col = inAt
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE stock_balances SET qty=$5, avg_cost_paise=$6, %s=now(), updated_at=now()
			WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3 AND batch_id=$4`, col),
			req.TenantID, req.StoreID, it.ID, batchID, newQty, newAvg); err != nil {
			return nil, err
		}
		out = append(out, Movement{
			ID: movID, StoreID: req.StoreID, ItemID: it.ID, ItemName: it.Name, Unit: it.Unit, BatchID: strings.TrimPrefix(batchID, NilUUID),
			BatchNo: strings.TrimSpace(l.BatchNo), Qty: qty, UnitCostPaise: applied, ValuePaise: ValuePaise(qty, applied),
			Reason: req.Reason, Source: req.Source, SourceID: req.SourceID, LineNo: l.LineNo, Note: l.Note, CreatedBy: orSystem(req.CreatedBy),
		})
		touched[it.ID] = it
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := s.emitStockMoved(ctx, tx, req, out); err != nil {
		return nil, err
	}
	if err := s.checkReorder(ctx, tx, req.TenantID, req.StoreID, touched); err != nil {
		return nil, err
	}
	return out, nil
}

func orSystem(s string) string {
	if s == "" {
		return "system"
	}
	return s
}

func (s *Service) emitStockMoved(ctx context.Context, tx pgx.Tx, req postRequest, movs []Movement) error {
	p := events.StockMovedPayload{
		StoreID: req.StoreID, Source: req.Source, SourceID: req.SourceID, Reason: req.Reason, Reversal: req.Reversal,
		OccurredAt: s.Now().UTC().Format(time.RFC3339), Movements: make([]events.StockMovement, 0, len(movs)),
	}
	if !req.OccurredAt.IsZero() {
		p.OccurredAt = req.OccurredAt.UTC().Format(time.RFC3339)
	}
	for _, m := range movs {
		p.ValuePaise += m.ValuePaise
		p.Movements = append(p.Movements, events.StockMovement{
			MovementID: m.ID, LineNo: m.LineNo, ItemID: m.ItemID, ItemName: m.ItemName, Unit: m.Unit, BatchNo: m.BatchNo,
			Qty: m.Qty, UnitCostPaise: m.UnitCostPaise, ValuePaise: m.ValuePaise, Reason: m.Reason,
		})
	}
	return s.emit(ctx, tx, events.StockMoved, req.TenantID, req.StoreID, p)
}

// checkReorder raises bhandar.stock.low once per item/store/day for the
// touched items that sit at or below their re-order level.
func (s *Service) checkReorder(ctx context.Context, tx pgx.Tx, tenantID, storeID string, touched map[string]*itemLite) error {
	today := s.today()
	for _, it := range touched {
		if it.ReorderLevel <= 0 {
			continue
		}
		var onHand float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(qty),0)::float8 FROM stock_balances WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3`,
			tenantID, storeID, it.ID).Scan(&onHand); err != nil {
			return err
		}
		needed, suggested := ReorderSuggestion(onHand, it.ReorderLevel, it.ReorderQty)
		if !needed {
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO reorder_alerts (tenant_id, store_id, item_id, alert_date) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			tenantID, storeID, it.ID, today)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if err := s.emit(ctx, tx, events.StockLow, tenantID, storeID, events.StockLowPayload{
			ItemID: it.ID, ItemName: it.Name, StoreID: storeID, OnHand: onHand, ReorderLevel: it.ReorderLevel,
			ReorderQty: suggested, SupplierID: it.PreferredSupplierID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Post runs post in its own transaction and kicks the relay.
func (s *Service) Post(ctx context.Context, req postRequest) ([]Movement, error) {
	var out []Movement
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		out, err = s.post(ctx, tx, req)
		return err
	})
	if err == nil && len(out) > 0 {
		s.kick()
	}
	return out, err
}

// AdjustmentLine is one line of POST /v1/stock/adjustments.
type AdjustmentLine struct {
	ItemID        string  `json:"item_id"`
	BatchNo       string  `json:"batch_no,omitempty"`
	ExpiryDate    string  `json:"expiry_date,omitempty"`
	Qty           float64 `json:"qty"` // signed
	Unit          string  `json:"unit,omitempty"`
	UnitCostPaise int64   `json:"unit_cost_paise,omitempty"` // for positive adjustments
}

// AdjustmentInput is the body of POST /v1/stock/adjustments.
type AdjustmentInput struct {
	StoreID string           `json:"store_id,omitempty"`
	Reason  string           `json:"reason"` // adjustment | expiry_writeoff
	Note    string           `json:"note"`
	Lines   []AdjustmentLine `json:"lines"`
}

// Validate checks an adjustment.
func (in *AdjustmentInput) Validate() error {
	if in.Reason == "" {
		in.Reason = ReasonAdjustment
	}
	if in.Reason != ReasonAdjustment && in.Reason != ReasonExpiryWriteoff {
		return httpx.Validation("reason must be adjustment or expiry_writeoff")
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Note == "" {
		return httpx.Validation("a note explaining the adjustment is required")
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		return httpx.Validation("an adjustment needs 1 to 500 lines")
	}
	for i, l := range in.Lines {
		switch {
		case l.ItemID == "":
			return httpx.Validation("line %d: item_id is required", i+1)
		case l.Qty == 0:
			return httpx.Validation("line %d: qty cannot be zero", i+1)
		case in.Reason == ReasonExpiryWriteoff && l.Qty > 0:
			return httpx.Validation("line %d: an expiry write-off must be negative", i+1)
		case l.UnitCostPaise < 0:
			return httpx.Validation("line %d: unit cost cannot be negative", i+1)
		}
	}
	return nil
}

// Adjustment is the response of POST /v1/stock/adjustments.
type Adjustment struct {
	ID        string     `json:"id"`
	StoreID   string     `json:"store_id"`
	Reason    string     `json:"reason"`
	Note      string     `json:"note"`
	Movements []Movement `json:"movements"`
}

// Adjust posts a manual stock adjustment.
func (s *Service) Adjust(ctx context.Context, tenantID, storeID, userID string, in AdjustmentInput) (*Adjustment, error) {
	id := uuid.NewString()
	req := postRequest{TenantID: tenantID, StoreID: storeID, Source: SourceAdjustment, SourceID: id, Reason: in.Reason, CreatedBy: userID}
	for i, l := range in.Lines {
		req.Lines = append(req.Lines, MoveLine{LineNo: i + 1, ItemID: l.ItemID, BatchNo: l.BatchNo, ExpiryDate: l.ExpiryDate,
			Qty: l.Qty, Unit: l.Unit, UnitCostPaise: l.UnitCostPaise, Note: in.Note})
	}
	movs, err := s.Post(ctx, req)
	if err != nil {
		return nil, err
	}
	return &Adjustment{ID: id, StoreID: storeID, Reason: in.Reason, Note: in.Note, Movements: movs}, nil
}

// OpeningLine is one line of POST /internal/v1/bulk/opening-stock (aayaat's StockRecord).
type OpeningLine struct {
	ItemExternalRef string  `json:"item_external_ref,omitempty"`
	ItemID          string  `json:"item_id,omitempty"`
	Qty             float64 `json:"qty"`
	UnitCostPaise   int64   `json:"unit_cost_paise"`
	BatchNo         string  `json:"batch_no,omitempty"`
	ExpiryDate      string  `json:"expiry_date,omitempty"`
}

// OpeningInput is the body of POST /internal/v1/bulk/opening-stock.
type OpeningInput struct {
	StoreID string        `json:"store_id"`
	AsOf    string        `json:"as_of,omitempty"`
	Lines   []OpeningLine `json:"lines"`
}

// OpeningStock posts opening quantities. One opening per store/item/batch is
// ever recorded (source_id carries that key), so re-sending a file is a
// no-op; corrections go through adjustments.
func (s *Service) OpeningStock(ctx context.Context, tenantID string, in OpeningInput) (BulkResult, error) {
	res := BulkResult{Errors: []BulkError{}}
	if in.StoreID == "" {
		return res, httpx.Validation("store_id is required")
	}
	if len(in.Lines) > 1000 {
		return res, httpx.Validation("send up to 1000 lines per call")
	}
	for i, l := range in.Lines {
		if l.Qty <= 0 {
			res.Errors = append(res.Errors, BulkError{Index: i, Message: "qty must be positive"})
			continue
		}
		it, err := resolveItemRef(ctx, s.DB, tenantID, l.ItemID, l.ItemExternalRef)
		if err != nil {
			res.Errors = append(res.Errors, BulkError{Index: i, Message: httpx.AsError(err).Message})
			continue
		}
		cost := l.UnitCostPaise
		if cost == 0 {
			cost = it.PurchasePricePaise
		}
		key := fmt.Sprintf("%s|%s|%s", in.StoreID, it.ID, strings.TrimSpace(l.BatchNo))
		movs, err := s.Post(ctx, postRequest{
			TenantID: tenantID, StoreID: in.StoreID, Source: SourceOpening, SourceID: key, Reason: ReasonOpening, CreatedBy: "import",
			Lines: []MoveLine{{LineNo: 0, ItemID: it.ID, BatchNo: l.BatchNo, ExpiryDate: l.ExpiryDate, Qty: l.Qty, UnitCostPaise: cost, Note: "opening stock " + in.AsOf}},
		})
		if err != nil {
			res.Errors = append(res.Errors, BulkError{Index: i, Message: httpx.AsError(err).Message})
			continue
		}
		if len(movs) > 0 {
			res.Created++
		} else {
			res.Updated++ // already recorded earlier: no-op
		}
	}
	return res, nil
}

// StockFilter narrows GET /v1/stock.
type StockFilter struct {
	StoreID string
	ItemID  string
	ByBatch bool
	httpx.Page
}

// Stock returns positions with value. Without by_batch, batches of an item
// are merged into one row per store/item with a weighted average cost.
func (s *Service) Stock(ctx context.Context, tenantID string, f StockFilter) ([]Balance, int, error) {
	where := []string{"b.tenant_id = $1", "b.qty <> 0"}
	args := []any{tenantID}
	if f.StoreID != "" {
		args = append(args, f.StoreID)
		where = append(where, fmt.Sprintf("b.store_id = $%d", len(args)))
	}
	if f.ItemID != "" {
		args = append(args, f.ItemID)
		where = append(where, fmt.Sprintf("b.item_id = $%d", len(args)))
	}
	cond := strings.Join(where, " AND ")
	var q, countQ string
	if f.ByBatch {
		countQ = `SELECT count(*) FROM stock_balances b WHERE ` + cond
		q = `SELECT b.store_id, b.item_id, i.name, i.unit, b.batch_id, bt.batch_no, bt.expiry_date, b.qty::float8, b.avg_cost_paise,
			round(b.qty * b.avg_cost_paise)::bigint, i.reorder_level::float8, b.last_in_at, b.last_out_at
			FROM stock_balances b JOIN items i ON i.id = b.item_id LEFT JOIN batches bt ON bt.id = b.batch_id
			WHERE ` + cond + ` ORDER BY lower(i.name), bt.expiry_date NULLS LAST, bt.batch_no`
	} else {
		countQ = `SELECT count(*) FROM (SELECT 1 FROM stock_balances b WHERE ` + cond + ` GROUP BY b.store_id, b.item_id) x`
		q = `SELECT b.store_id, b.item_id, i.name, i.unit, NULL::uuid, NULL::text, NULL::date, sum(b.qty)::float8,
			CASE WHEN sum(b.qty) > 0 THEN round(sum(b.qty * b.avg_cost_paise) / sum(b.qty))::bigint ELSE max(b.avg_cost_paise) END,
			sum(round(b.qty * b.avg_cost_paise))::bigint, i.reorder_level::float8, max(b.last_in_at), max(b.last_out_at)
			FROM stock_balances b JOIN items i ON i.id = b.item_id
			WHERE ` + cond + ` GROUP BY b.store_id, b.item_id, i.name, i.unit, i.reorder_level ORDER BY lower(i.name)`
	}
	var total int
	if err := s.DB.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf("%s LIMIT $%d OFFSET $%d", q, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Balance{}
	for rows.Next() {
		var b Balance
		var batchID, batchNo *string
		var exp, inAt, outAt *time.Time
		if err := rows.Scan(&b.StoreID, &b.ItemID, &b.ItemName, &b.Unit, &batchID, &batchNo, &exp, &b.Qty, &b.AvgCostPaise,
			&b.ValuePaise, &b.ReorderLevel, &inAt, &outAt); err != nil {
			return nil, 0, err
		}
		if batchID != nil && *batchID != NilUUID {
			b.BatchID, b.BatchNo, b.ExpiryDate = *batchID, deref(batchNo), dateString(exp)
		}
		b.LastInAt, b.LastOutAt = derefTime(inAt), derefTime(outAt)
		out = append(out, b)
	}
	return out, total, rows.Err()
}

// LedgerFilter narrows GET /v1/items/{id}/ledger.
type LedgerFilter struct {
	StoreID string
	From    string
	To      string
	httpx.Page
}

// ItemLedger lists an item's movements, newest first, with the running
// quantity after each one (per store).
func (s *Service) ItemLedger(ctx context.Context, tenantID, itemID string, f LedgerFilter) ([]Movement, int, error) {
	args := []any{tenantID, itemID}
	inner := `SELECT m.id, m.store_id, m.item_id, m.batch_id, bt.batch_no, m.qty::float8, m.unit_cost_paise, m.reason, m.source,
		m.source_id, m.line_no, m.note, m.created_by, m.created_at,
		sum(m.qty) OVER (PARTITION BY m.store_id ORDER BY m.created_at, m.id)::float8 AS running
		FROM stock_movements m LEFT JOIN batches bt ON bt.id = m.batch_id
		WHERE m.tenant_id = $1 AND m.item_id = $2`
	if f.StoreID != "" {
		args = append(args, f.StoreID)
		inner += fmt.Sprintf(" AND m.store_id = $%d", len(args))
	}
	where := []string{"true"}
	if f.From != "" {
		args = append(args, f.From)
		where = append(where, fmt.Sprintf("x.created_at >= $%d::date", len(args)))
	}
	if f.To != "" {
		args = append(args, f.To)
		where = append(where, fmt.Sprintf("x.created_at < ($%d::date + 1)", len(args)))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM (%s) x WHERE %s`, inner, cond), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT * FROM (%s) x WHERE %s ORDER BY x.created_at DESC, x.id DESC LIMIT $%d OFFSET $%d`,
		inner, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Movement{}
	for rows.Next() {
		var m Movement
		var batchID, batchNo, note *string
		var running float64
		if err := rows.Scan(&m.ID, &m.StoreID, &m.ItemID, &batchID, &batchNo, &m.Qty, &m.UnitCostPaise, &m.Reason, &m.Source,
			&m.SourceID, &m.LineNo, &note, &m.CreatedBy, &m.CreatedAt, &running); err != nil {
			return nil, 0, err
		}
		m.BatchID, m.BatchNo, m.Note = deref(batchID), deref(batchNo), deref(note)
		m.ValuePaise = ValuePaise(m.Qty, m.UnitCostPaise)
		r := Q3(running)
		m.RunningQty = &r
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// ItemBatches lists an item's batches with the quantity held (all stores, or one).
func (s *Service) ItemBatches(ctx context.Context, tenantID, itemID, storeID string) ([]Batch, error) {
	args := []any{tenantID, itemID}
	storeCond := ""
	if storeID != "" {
		args = append(args, storeID)
		storeCond = " AND b.store_id = $3"
	}
	rows, err := s.DB.Query(ctx, `SELECT bt.id, bt.item_id, bt.batch_no, bt.mfg_date, bt.expiry_date, bt.mrp_paise,
		COALESCE((SELECT sum(b.qty) FROM stock_balances b WHERE b.tenant_id = bt.tenant_id AND b.batch_id = bt.id`+storeCond+`), 0)::float8
		FROM batches bt WHERE bt.tenant_id = $1 AND bt.item_id = $2 ORDER BY bt.expiry_date NULLS LAST, bt.batch_no`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		var b Batch
		var mfg, exp *time.Time
		if err := rows.Scan(&b.ID, &b.ItemID, &b.BatchNo, &mfg, &exp, &b.MRPPaise, &b.Qty); err != nil {
			return nil, err
		}
		b.MfgDate, b.ExpiryDate = dateString(mfg), dateString(exp)
		out = append(out, b)
	}
	return out, rows.Err()
}

// movementsOf returns the posted movements of a source document (for reversals).
func movementsOf(ctx context.Context, q db.Querier, tenantID string, sources []string, sourceID string) ([]Movement, error) {
	rows, err := q.Query(ctx, `SELECT m.id, m.store_id, m.item_id, COALESCE(bt.batch_no,''), m.qty::float8, m.unit_cost_paise, m.reason, m.source, m.line_no
		FROM stock_movements m LEFT JOIN batches bt ON bt.id = m.batch_id
		WHERE m.tenant_id = $1 AND m.source = ANY($2::text[]) AND m.source_id = $3 ORDER BY m.line_no`, tenantID, sources, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movement
	for rows.Next() {
		var m Movement
		if err := rows.Scan(&m.ID, &m.StoreID, &m.ItemID, &m.BatchNo, &m.Qty, &m.UnitCostPaise, &m.Reason, &m.Source, &m.LineNo); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
