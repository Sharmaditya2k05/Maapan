package inventory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Stock count statuses.
const (
	CountCounting  = "counting"
	CountPosted    = "posted"
	CountCancelled = "cancelled"
)

// StockCount is a stock take / cycle count session at one store. Creating
// it freezes a snapshot of the book quantities in scope; counts are entered
// against that snapshot and posting adjusts stock by (counted − frozen), so
// sales and receipts made while counting are not lost.
type StockCount struct {
	ID          string           `json:"id"`
	StoreID     string           `json:"store_id"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Category    string           `json:"category,omitempty"`
	BinCode     string           `json:"bin_code,omitempty"`
	ItemIDs     []string         `json:"item_ids,omitempty"`
	Note        string           `json:"note,omitempty"`
	FrozenAt    time.Time        `json:"frozen_at"`
	PostedAt    *time.Time       `json:"posted_at,omitempty"`
	CancelledAt *time.Time       `json:"cancelled_at,omitempty"`
	CreatedBy   string           `json:"created_by"`
	PostedBy    string           `json:"posted_by,omitempty"`
	Totals      *CountTotals     `json:"totals,omitempty"`
	Lines       []StockCountLine `json:"lines,omitempty"`
}

// StockCountLine is one item/batch of a count with its variance.
type StockCountLine struct {
	LineNo        int      `json:"line_no"`
	ItemID        string   `json:"item_id"`
	ItemName      string   `json:"item_name"`
	SKU           string   `json:"sku,omitempty"`
	Barcode       string   `json:"barcode,omitempty"`
	Unit          string   `json:"unit"`
	BatchID       string   `json:"batch_id,omitempty"`
	BatchNo       string   `json:"batch_no,omitempty"`
	BinCode       string   `json:"bin_code,omitempty"`
	FrozenQty     float64  `json:"frozen_qty"`
	CountedQty    *float64 `json:"counted_qty"`
	VarianceQty   *float64 `json:"variance_qty"`
	AvgCostPaise  int64    `json:"avg_cost_paise"`
	VariancePaise int64    `json:"variance_value_paise"`
	CountedBy     string   `json:"counted_by,omitempty"`
	AddedOnCount  bool     `json:"added_on_count,omitempty"` // not in the frozen snapshot (found while counting)
}

// CountTotals summarises a count's variance report.
type CountTotals struct {
	Lines         int   `json:"lines"`
	Counted       int   `json:"counted"`
	Uncounted     int   `json:"uncounted"`
	WithVariance  int   `json:"with_variance"`
	ShortagePaise int64 `json:"shortage_value_paise"` // positive number
	ExcessPaise   int64 `json:"excess_value_paise"`
	NetPaise      int64 `json:"net_value_paise"` // excess − shortage
	FrozenValue   int64 `json:"frozen_value_paise"`
}

// StockCountInput is the body of POST /v1/stock-counts.
type StockCountInput struct {
	StoreID  string   `json:"store_id,omitempty"`
	Name     string   `json:"name,omitempty"`
	Category string   `json:"category,omitempty"` // cycle count of one category
	BinCode  string   `json:"bin_code,omitempty"` // … or one rack
	ItemIDs  []string `json:"item_ids,omitempty"` // … or these items (included even at zero stock)
	Note     string   `json:"note,omitempty"`
}

// Validate normalises the scope.
func (in *StockCountInput) Validate() error {
	in.Name, in.Category, in.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Category), strings.TrimSpace(in.Note)
	in.BinCode = strings.ToUpper(strings.TrimSpace(in.BinCode))
	if len(in.ItemIDs) > 5000 {
		return httpx.Validation("count up to 5000 items per session")
	}
	return nil
}

// CreateStockCount opens a session and freezes the snapshot.
func (s *Service) CreateStockCount(ctx context.Context, tenantID, storeID, userID string, in StockCountInput) (*StockCount, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.Name == "" {
		in.Name = "Stock count " + s.Now().In(ist).Format("2006-01-02 15:04")
	}
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM stock_counts WHERE tenant_id=$1 AND store_id=$2 AND status='counting'
			AND COALESCE(category,'')=$3 AND COALESCE(bin_code,'')=$4 AND item_ids IS NULL AND $5`,
			tenantID, storeID, in.Category, in.BinCode, len(in.ItemIDs) == 0).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return httpx.Conflict("a count with the same scope is already open at this store; post or cancel it first")
		}
		var ids []string
		if len(in.ItemIDs) > 0 {
			ids = in.ItemIDs
		}
		if err := tx.QueryRow(ctx, `INSERT INTO stock_counts (tenant_id, store_id, name, category, bin_code, item_ids, note, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, tenantID, storeID, in.Name, nilIfEmpty(in.Category), nilIfEmpty(in.BinCode),
			ids, nilIfEmpty(in.Note), orSystem(userID)).Scan(&id); err != nil {
			return err
		}
		// Snapshot: every non-zero position in scope (lock the balances so the
		// snapshot is consistent), plus explicitly listed items at zero.
		if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (count_id, item_id, batch_id, line_no, frozen_qty, avg_cost_paise)
			SELECT $1, b.item_id, b.batch_id, row_number() OVER (ORDER BY lower(i.name), b.batch_id), b.qty, b.avg_cost_paise
			FROM stock_balances b JOIN items i ON i.id = b.item_id
			LEFT JOIN item_bins ib ON ib.tenant_id=b.tenant_id AND ib.store_id=b.store_id AND ib.item_id=b.item_id
			WHERE b.tenant_id=$2 AND b.store_id=$3 AND b.qty <> 0
			AND ($4 = '' OR i.category = $4) AND ($5 = '' OR ib.bin_code = $5) AND ($6::uuid[] IS NULL OR b.item_id = ANY($6::uuid[]))`,
			id, tenantID, storeID, in.Category, in.BinCode, ids); err != nil {
			return err
		}
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (count_id, item_id, batch_id, line_no, frozen_qty, avg_cost_paise)
				SELECT $1, i.id, $4::uuid, (SELECT COALESCE(max(line_no),0) FROM stock_count_lines WHERE count_id=$1) + row_number() OVER (ORDER BY lower(i.name)),
					0, i.purchase_price_paise
				FROM items i WHERE i.tenant_id=$2 AND i.id = ANY($3::uuid[])
				AND NOT EXISTS (SELECT 1 FROM stock_count_lines l WHERE l.count_id=$1 AND l.item_id=i.id)`,
				id, tenantID, ids, NilUUID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetStockCount(ctx, tenantID, id, "")
}

func (s *Service) loadCount(ctx context.Context, q db.Querier, tenantID, id string, lock bool) (*StockCount, error) {
	var c StockCount
	var cat, bin, note, postedBy *string
	sql := `SELECT id, store_id, name, status, category, bin_code, COALESCE(item_ids::text[], '{}'), note, frozen_at, posted_at,
		cancelled_at, created_by, posted_by FROM stock_counts WHERE tenant_id=$1 AND id=$2`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, tenantID, id).Scan(&c.ID, &c.StoreID, &c.Name, &c.Status, &cat, &bin, &c.ItemIDs, &note, &c.FrozenAt,
		&c.PostedAt, &c.CancelledAt, &c.CreatedBy, &postedBy)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("stock count not found")
	}
	if err != nil {
		return nil, err
	}
	c.Category, c.BinCode, c.Note, c.PostedBy = deref(cat), deref(bin), deref(note), deref(postedBy)
	return &c, nil
}

// GetStockCount returns a count with its variance report. only: "" (all
// lines), "variance" (counted lines that differ), "uncounted".
func (s *Service) GetStockCount(ctx context.Context, tenantID, id, only string) (*StockCount, error) {
	c, err := s.loadCount(ctx, s.DB, tenantID, id, false)
	if err != nil {
		return nil, err
	}
	lines, err := countLines(ctx, s.DB, tenantID, c)
	if err != nil {
		return nil, err
	}
	t := &CountTotals{}
	c.Lines = []StockCountLine{}
	for _, l := range lines {
		t.Lines++
		t.FrozenValue += ValuePaise(l.FrozenQty, l.AvgCostPaise)
		if l.CountedQty == nil {
			t.Uncounted++
		} else {
			t.Counted++
			if *l.VarianceQty != 0 {
				t.WithVariance++
				if l.VariancePaise < 0 {
					t.ShortagePaise -= l.VariancePaise
				} else {
					t.ExcessPaise += l.VariancePaise
				}
			}
		}
		switch {
		case only == "variance" && (l.CountedQty == nil || *l.VarianceQty == 0):
			continue
		case only == "uncounted" && l.CountedQty != nil:
			continue
		}
		c.Lines = append(c.Lines, l)
	}
	t.NetPaise = t.ExcessPaise - t.ShortagePaise
	c.Totals = t
	return c, nil
}

func countLines(ctx context.Context, q db.Querier, tenantID string, c *StockCount) ([]StockCountLine, error) {
	rows, err := q.Query(ctx, `SELECT l.line_no, l.item_id, i.name, i.sku, i.barcode, i.unit, l.batch_id, bt.batch_no, ib.bin_code,
		l.frozen_qty::float8, l.counted_qty::float8, l.avg_cost_paise, l.counted_by, NOT l.in_scope
		FROM stock_count_lines l JOIN items i ON i.id = l.item_id LEFT JOIN batches bt ON bt.id = l.batch_id
		LEFT JOIN item_bins ib ON ib.tenant_id=i.tenant_id AND ib.store_id=$3 AND ib.item_id=l.item_id
		WHERE l.count_id=$1 AND i.tenant_id=$2 ORDER BY ib.bin_code NULLS LAST, l.line_no`, c.ID, tenantID, c.StoreID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StockCountLine
	for rows.Next() {
		var l StockCountLine
		var sku, bc, batchNo, bin, by *string
		var batchID string
		if err := rows.Scan(&l.LineNo, &l.ItemID, &l.ItemName, &sku, &bc, &l.Unit, &batchID, &batchNo, &bin, &l.FrozenQty, &l.CountedQty,
			&l.AvgCostPaise, &by, &l.AddedOnCount); err != nil {
			return nil, err
		}
		l.SKU, l.Barcode, l.BatchNo, l.BinCode, l.CountedBy = deref(sku), deref(bc), deref(batchNo), deref(bin), deref(by)
		if batchID != NilUUID {
			l.BatchID = batchID
		}
		if l.CountedQty != nil {
			v := Q3(*l.CountedQty - l.FrozenQty)
			l.VarianceQty = &v
			l.VariancePaise = ValuePaise(v, l.AvgCostPaise)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// StockCountFilter narrows GET /v1/stock-counts.
type StockCountFilter struct {
	StoreID string
	Status  string
	httpx.Page
}

// ListStockCounts lists sessions, newest first (without lines).
func (s *Service) ListStockCounts(ctx context.Context, tenantID string, f StockCountFilter) ([]StockCount, error) {
	rows, err := s.DB.Query(ctx, `SELECT id FROM stock_counts WHERE tenant_id=$1 AND ($2 = '' OR store_id::text=$2) AND ($3 = '' OR status=$3)
		ORDER BY created_at DESC LIMIT $4 OFFSET $5`, tenantID, f.StoreID, f.Status, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []StockCount{}
	for _, id := range ids {
		c, err := s.GetStockCount(ctx, tenantID, id, "")
		if err != nil {
			return nil, err
		}
		c.Lines = nil
		out = append(out, *c)
	}
	return out, nil
}

// CountEntry is one scan or keyed count.
type CountEntry struct {
	Code    string   `json:"code,omitempty"`    // barcode / SKU / PLU / weighing-scale code
	ItemID  string   `json:"item_id,omitempty"` // instead of code
	BatchNo string   `json:"batch_no,omitempty"`
	Qty     *float64 `json:"qty,omitempty"`  // default 1 (one scan = one unit), or the scale code's quantity
	Unit    string   `json:"unit,omitempty"` // alternate unit (BOX); default the base unit
	Mode    string   `json:"mode,omitempty"` // add (default: scans accumulate) | set (replace the count)
}

// CountEntriesInput is the body of POST /v1/stock-counts/{id}/entries.
type CountEntriesInput struct {
	Entries []CountEntry `json:"entries"`
}

// Validate checks entries.
func (in *CountEntriesInput) Validate() error {
	if len(in.Entries) == 0 || len(in.Entries) > 2000 {
		return httpx.Validation("send 1 to 2000 entries")
	}
	for i, e := range in.Entries {
		switch {
		case strings.TrimSpace(e.Code) == "" && e.ItemID == "":
			return httpx.Validation("entry %d: code or item_id is required", i+1)
		case e.Mode != "" && e.Mode != "add" && e.Mode != "set":
			return httpx.Validation("entry %d: mode must be add or set", i+1)
		case e.Qty != nil && *e.Qty < 0:
			return httpx.Validation("entry %d: qty cannot be negative", i+1)
		}
	}
	return nil
}

// CountEntryError is an entry that could not be applied (the rest are).
type CountEntryError struct {
	Index   int    `json:"index"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// CountEntriesResult is the response of POST /v1/stock-counts/{id}/entries.
type CountEntriesResult struct {
	Applied int               `json:"applied"`
	Errors  []CountEntryError `json:"errors"`
	Lines   []StockCountLine  `json:"lines"` // the lines these entries touched, with their running count
}

// EnterCounts applies scans / keyed counts. Unknown codes and out-of-scope
// items are reported per entry so a scanner session never loses good scans.
func (s *Service) EnterCounts(ctx context.Context, tenantID, id, userID string, in CountEntriesInput) (*CountEntriesResult, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	res := &CountEntriesResult{Errors: []CountEntryError{}, Lines: []StockCountLine{}}
	touched := map[string]bool{}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		c, err := s.loadCount(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if c.Status != CountCounting {
			return httpx.Conflict("count is %s", c.Status)
		}
		for i, e := range in.Entries {
			fail := func(format string, a ...any) {
				res.Errors = append(res.Errors, CountEntryError{Index: i, Code: e.Code, Message: fmt.Sprintf(format, a...)})
			}
			qty := 1.0
			itemID := e.ItemID
			if itemID == "" {
				lk, err := s.LookupByCode(ctx, tenantID, e.Code)
				if err != nil {
					if httpx.AsError(err).Status < 500 {
						fail("%s", httpx.AsError(err).Message)
						continue
					}
					return err
				}
				itemID = lk.Item.ID
				if lk.Scale != nil && lk.Scale.Qty > 0 {
					qty = lk.Scale.Qty
				}
			}
			if e.Qty != nil {
				qty = *e.Qty
			}
			it, err := loadItemLite(ctx, tx, tenantID, itemID)
			if err != nil {
				fail("%s", httpx.AsError(err).Message)
				continue
			}
			base, ok := ToBaseUnit(qty, e.Unit, it.Unit, it.Alts)
			if !ok {
				fail("%s has no unit %s", it.Name, e.Unit)
				continue
			}
			batchID, err := s.countBatch(ctx, tx, tenantID, c.ID, it, e.BatchNo)
			if err != nil {
				if httpx.AsError(err).Status < 500 {
					fail("%s", httpx.AsError(err).Message)
					continue
				}
				return err
			}
			// Find (or add) the line.
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_count_lines WHERE count_id=$1 AND item_id=$2 AND batch_id=$3)`,
				c.ID, it.ID, batchID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				inScope, err := countScopeHas(ctx, tx, tenantID, c, it.ID)
				if err != nil {
					return err
				}
				if !inScope {
					fail("%s is outside this count's scope", it.Name)
					continue
				}
				// Not in the snapshot: book qty was zero when frozen.
				var cost int64
				_ = tx.QueryRow(ctx, `SELECT avg_cost_paise FROM stock_balances WHERE tenant_id=$1 AND store_id=$2 AND item_id=$3 AND batch_id=$4`,
					tenantID, c.StoreID, it.ID, batchID).Scan(&cost)
				if cost == 0 {
					cost = it.PurchasePricePaise
				}
				if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (count_id, item_id, batch_id, line_no, frozen_qty, avg_cost_paise, in_scope)
					VALUES ($1,$2,$3,(SELECT COALESCE(max(line_no),0)+1 FROM stock_count_lines WHERE count_id=$1),0,$4,false)`,
					c.ID, it.ID, batchID, cost); err != nil {
					return err
				}
			}
			set := `counted_qty = COALESCE(counted_qty, 0) + $4`
			if e.Mode == "set" {
				set = `counted_qty = $4`
			}
			if _, err := tx.Exec(ctx, `UPDATE stock_count_lines SET `+set+`, counted_at=now(), counted_by=$5
				WHERE count_id=$1 AND item_id=$2 AND batch_id=$3`, c.ID, it.ID, batchID, Q3(base), orSystem(userID)); err != nil {
				return err
			}
			touched[it.ID+"|"+batchID] = true
			res.Applied++
		}
		if len(touched) > 0 {
			lines, err := countLines(ctx, tx, tenantID, c)
			if err != nil {
				return err
			}
			for _, l := range lines {
				b := l.BatchID
				if b == "" {
					b = NilUUID
				}
				if touched[l.ItemID+"|"+b] {
					res.Lines = append(res.Lines, l)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// countBatch resolves the batch a count entry is for. A batch item without a
// batch_no is accepted only when the snapshot holds exactly one batch of it.
func (s *Service) countBatch(ctx context.Context, tx pgx.Tx, tenantID, countID string, it *itemLite, batchNo string) (string, error) {
	batchNo = strings.TrimSpace(batchNo)
	if batchNo != "" {
		return upsertBatch(ctx, tx, tenantID, it.ID, MoveLine{BatchNo: batchNo})
	}
	if !it.TrackBatches {
		return NilUUID, nil
	}
	rows, err := tx.Query(ctx, `SELECT batch_id FROM stock_count_lines WHERE count_id=$1 AND item_id=$2 AND batch_id <> $3`, countID, it.ID, NilUUID)
	if err != nil {
		return "", err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) == 0 {
		return NilUUID, nil
	}
	return "", httpx.Validation("%s has %d batches here: send batch_no", it.Name, len(ids))
}

// countScopeHas reports whether an item belongs to a count's scope.
func countScopeHas(ctx context.Context, q db.Querier, tenantID string, c *StockCount, itemID string) (bool, error) {
	if len(c.ItemIDs) > 0 {
		for _, id := range c.ItemIDs {
			if id == itemID {
				return true, nil
			}
		}
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM items i
		LEFT JOIN item_bins ib ON ib.tenant_id=i.tenant_id AND ib.store_id=$3 AND ib.item_id=i.id
		WHERE i.tenant_id=$1 AND i.id=$2 AND ($4 = '' OR i.category=$4) AND ($5 = '' OR ib.bin_code=$5))`,
		tenantID, itemID, c.StoreID, c.Category, c.BinCode).Scan(&ok)
	return ok, err
}

// PostCountInput is the body of POST /v1/stock-counts/{id}/post.
type PostCountInput struct {
	Note string `json:"note,omitempty"`
	// Uncounted: skip (default: lines nobody counted keep their book stock) |
	// zero (a full stock take: whatever was not found is written off).
	Uncounted string `json:"uncounted,omitempty"`
}

// Validate checks the posting options.
func (in *PostCountInput) Validate() error {
	in.Note = strings.TrimSpace(in.Note)
	switch in.Uncounted {
	case "", "skip", "zero":
	default:
		return httpx.Validation("uncounted must be skip or zero")
	}
	return nil
}

// PostStockCount posts the variances as adjustment movements (source
// stock_count, reason adjustment, one bhandar.stock.moved) and closes the count.
func (s *Service) PostStockCount(ctx context.Context, tenantID, id, userID string, in PostCountInput) (*StockCount, []Movement, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	var movs []Movement
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		c, err := s.loadCount(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if c.Status != CountCounting {
			return httpx.Conflict("count is %s", c.Status)
		}
		lines, err := countLines(ctx, tx, tenantID, c)
		if err != nil {
			return err
		}
		note := "Stock count: " + c.Name
		if in.Note != "" {
			note += " — " + in.Note
		}
		req := postRequest{TenantID: tenantID, StoreID: c.StoreID, Source: SourceStockCount, SourceID: c.ID, Reason: ReasonAdjustment, CreatedBy: userID}
		for _, l := range lines {
			counted := l.CountedQty
			if counted == nil {
				if in.Uncounted != "zero" {
					continue
				}
				z := 0.0
				counted = &z
			}
			diff := Q3(*counted - l.FrozenQty)
			if diff == 0 {
				continue
			}
			req.Lines = append(req.Lines, MoveLine{LineNo: l.LineNo, ItemID: l.ItemID, BatchNo: l.BatchNo, Qty: diff,
				UnitCostPaise: l.AvgCostPaise, Note: note})
		}
		if len(req.Lines) > 0 {
			if movs, err = s.post(ctx, tx, req); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE stock_counts SET status='posted', posted_at=now(), posted_by=$3, note=COALESCE($4, note)
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, orSystem(userID), nilIfEmpty(in.Note))
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if len(movs) > 0 {
		s.kick()
	}
	c, err := s.GetStockCount(ctx, tenantID, id, "variance")
	if movs == nil {
		movs = []Movement{}
	}
	return c, movs, err
}

// CancelStockCount abandons an open count.
func (s *Service) CancelStockCount(ctx context.Context, tenantID, id string) (*StockCount, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE stock_counts SET status='cancelled', cancelled_at=now() WHERE tenant_id=$1 AND id=$2 AND status='counting'`, tenantID, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		c, err := s.loadCount(ctx, s.DB, tenantID, id, false)
		if err != nil {
			return nil, err
		}
		return nil, httpx.Conflict("count is %s", c.Status)
	}
	return s.GetStockCount(ctx, tenantID, id, "")
}
