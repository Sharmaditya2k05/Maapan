package production

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/internal/numbering"
	"github.com/maapan/nirmaan/internal/units"
)

// Run is a production run.
type Run struct {
	ID                  string     `json:"id"`
	RunNo               string     `json:"run_no"`
	BOMID               string     `json:"bom_id"`
	ItemID              string     `json:"item_id"`
	ItemName            string     `json:"item_name"`
	StoreID             string     `json:"store_id"`
	Qty                 float64    `json:"qty"`
	OutputQty           *float64   `json:"output_qty,omitempty"` // good output so far (BOM unit)
	RejectedQty         float64    `json:"rejected_qty"`
	YieldPercent        *float64   `json:"yield_percent,omitempty"`         // good ÷ (good + rejected)
	YieldVsPlanPercent  *float64   `json:"yield_vs_plan_percent,omitempty"` // good ÷ planned qty
	PlannedDate         string     `json:"planned_date"`
	Status              string     `json:"status"`
	OutputBatchNo       *string    `json:"output_batch_no,omitempty"`
	TotalCostPaise      *int64     `json:"total_cost_paise,omitempty"`
	MaterialCostPaise   int64      `json:"material_cost_paise"`
	OperationsCostPaise int64      `json:"operations_cost_paise"`
	ByproductValuePaise int64      `json:"byproduct_value_paise"`
	IssuedValuePaise    int64      `json:"issued_value_paise"`
	WIPPaise            int64      `json:"wip_paise"`
	PlannedHours        float64    `json:"planned_hours"`
	ActualHours         float64    `json:"actual_hours"`
	Receipts            int        `json:"receipts"`
	FailureReason       *string    `json:"failure_reason,omitempty"`
	Notes               *string    `json:"notes,omitempty"`
	CreatedBy           string     `json:"created_by"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	Lines               []Line     `json:"lines,omitempty"`
	Shortages           []Shortage `json:"shortages,omitempty"`
	ReceiptList         []Receipt  `json:"receipt_list,omitempty"`
}

// Receipt is one (partial or final) completion of a run.
type Receipt struct {
	ID              string    `json:"id"`
	Seq             int       `json:"seq"`
	PostingID       string    `json:"posting_id"`
	Final           bool      `json:"final"`
	GoodQty         float64   `json:"good_qty"`
	RejectedQty     float64   `json:"rejected_qty"`
	BatchNo         *string   `json:"batch_no,omitempty"`
	LabourHours     float64   `json:"labour_hours"`
	MaterialPaise   int64     `json:"material_paise"`
	OperationsPaise int64     `json:"operations_paise"`
	ByproductPaise  int64     `json:"byproduct_paise"`
	TotalPaise      int64     `json:"total_paise"`
	UnitCostPaise   int64     `json:"unit_cost_paise"`
	CreatedAt       time.Time `json:"created_at"`
}

// Service implements production runs.
type Service struct {
	DB        *pgxpool.Pool
	BOMs      *bom.Service
	Inventory clients.Inventory
	Relay     *events.Relay
	Log       *slog.Logger
	Now       func() time.Time
}

// CreateInput is the body of POST /v1/production-runs. Give bom_id (any active
// BOM, e.g. an alternate) or item_id (its primary BOM effective on planned_date).
type CreateInput struct {
	BOMID       string  `json:"bom_id,omitempty"`
	ItemID      string  `json:"item_id,omitempty"`
	Qty         float64 `json:"qty"`
	StoreID     string  `json:"store_id,omitempty"`
	PlannedDate string  `json:"planned_date,omitempty"`
	Notes       string  `json:"notes,omitempty"`
}

// Validate checks the body.
func (in *CreateInput) Validate() error {
	if (in.BOMID == "" && in.ItemID == "") || in.Qty <= 0 {
		return httpx.Validation("bom_id (or item_id) and a positive qty are required")
	}
	if in.PlannedDate != "" {
		if _, err := time.Parse(time.DateOnly, in.PlannedDate); err != nil {
			return httpx.Validation("planned_date must be YYYY-MM-DD")
		}
	}
	return nil
}

func canStore(p *auth.Principal, storeID string) bool {
	return p.Claims == nil || p.Claims.CanAccessStore(storeID)
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// catalog looks up units (and names) for ids; nil Inventory → empty (factor 1).
func (s *Service) catalog(ctx context.Context, ids []string) (units.Catalog, map[string]clients.Item, error) {
	if s.Inventory == nil {
		return nil, map[string]clients.Item{}, nil
	}
	items, err := s.Inventory.Lookup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	return clients.Catalog(items), items, nil
}

// Create plans a run from an active BOM and reports material availability.
func (s *Service) Create(ctx context.Context, p *auth.Principal, in CreateInput) (*Run, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	store := in.StoreID
	if store == "" {
		store = p.StoreID
	}
	if store == "" || !canStore(p, store) {
		return nil, httpx.Forbidden("store_id missing or not accessible")
	}
	date := in.PlannedDate
	if date == "" {
		date = today(s.now())
	}
	bomID := in.BOMID
	if bomID == "" {
		id, err := bom.EffectiveBOMID(ctx, s.DB, p.TenantID, in.ItemID, date)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, httpx.Validation("the item has no primary BOM effective on %s", date)
		}
		bomID = id
	}
	b, err := bom.Load(ctx, s.DB, p.TenantID, bomID)
	if err != nil {
		return nil, err
	}
	if b.Status != bom.StatusActive {
		return nil, httpx.Validation("only an active BOM can be produced")
	}
	if !b.EffectiveOn(date) {
		return nil, httpx.Validation("BOM v%d is not effective on %s", b.Version, date)
	}
	ids := []string{b.ItemID}
	for _, c := range b.Components {
		ids = append(ids, c.ItemID)
	}
	cat, _, err := s.catalog(ctx, ids)
	if err != nil {
		return nil, err
	}
	lines, err := PlanLines(b, in.Qty, cat)
	if err != nil {
		return nil, err
	}
	_, hours := bom.OperationsFor(b, in.Qty)
	pd, _ := time.ParseInLocation(time.DateOnly, date, ist)
	var id string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		no, err := numbering.Next(ctx, tx, p.TenantID, numbering.ProductionRun, pd)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO production_runs (tenant_id, run_no, bom_id, item_id, item_name, store_id, qty,
			planned_date, notes, created_by, planned_hours) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			p.TenantID, no, b.ID, b.ItemID, b.ItemName, store, units.Round3(in.Qty), date, nilIfEmpty(in.Notes), p.UserID(), hours).Scan(&id); err != nil {
			return err
		}
		for _, l := range lines {
			if err := insertLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id, true)
}

func insertLine(ctx context.Context, tx pgx.Tx, runID string, l Line) error {
	_, err := tx.Exec(ctx, `INSERT INTO production_run_lines (run_id, line_no, kind, item_id, item_name, unit, base_unit, factor,
		is_sub_assembly, unplanned, planned_qty, planned_base_qty) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		runID, l.LineNo, l.Kind, l.ItemID, l.ItemName, l.Unit, l.BaseUnit, l.Factor, l.IsSubAssembly, l.Unplanned,
		l.PlannedQty, l.PlannedBaseQty)
	return err
}

const runCols = `id, run_no, bom_id, item_id, item_name, store_id, qty::float8, output_qty::float8, rejected_qty::float8,
	to_char(planned_date,'YYYY-MM-DD'), status, output_batch_no, total_cost_paise, material_cost_paise, operations_cost_paise,
	byproduct_value_paise, issued_value_paise, planned_hours::float8, actual_hours::float8, receipts, failure_reason,
	notes, created_by, created_at, started_at, completed_at, cancelled_at`

func scanRun(row pgx.Row) (*Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.RunNo, &r.BOMID, &r.ItemID, &r.ItemName, &r.StoreID, &r.Qty, &r.OutputQty, &r.RejectedQty,
		&r.PlannedDate, &r.Status, &r.OutputBatchNo, &r.TotalCostPaise, &r.MaterialCostPaise, &r.OperationsCostPaise,
		&r.ByproductValuePaise, &r.IssuedValuePaise, &r.PlannedHours, &r.ActualHours, &r.Receipts, &r.FailureReason,
		&r.Notes, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.CompletedAt, &r.CancelledAt)
	if err != nil {
		return nil, err
	}
	good := 0.0
	if r.OutputQty != nil {
		good = *r.OutputQty
	}
	r.YieldPercent, r.YieldVsPlanPercent = Yield(good, r.RejectedQty, r.Qty)
	if r.Status == StatusInProgress && r.IssuedValuePaise > r.MaterialCostPaise {
		r.WIPPaise = r.IssuedValuePaise - r.MaterialCostPaise
	}
	return &r, nil
}

func loadLines(ctx context.Context, q db.Querier, runID string) ([]Line, error) {
	rows, err := q.Query(ctx, `SELECT line_no, kind, item_id, item_name, unit, base_unit, factor::float8, is_sub_assembly,
		unplanned, planned_qty::float8, planned_base_qty::float8, issued_qty::float8, actual_qty::float8, unit_cost_paise
		FROM production_run_lines WHERE run_id=$1 ORDER BY line_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.LineNo, &l.Kind, &l.ItemID, &l.ItemName, &l.Unit, &l.BaseUnit, &l.Factor, &l.IsSubAssembly,
			&l.Unplanned, &l.PlannedQty, &l.PlannedBaseQty, &l.IssuedQty, &l.ActualQty, &l.UnitCostPaise); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func loadReceipts(ctx context.Context, q db.Querier, runID string) ([]Receipt, error) {
	rows, err := q.Query(ctx, `SELECT id, seq, posting_id, is_final, good_qty::float8, rejected_qty::float8, batch_no,
		labour_hours::float8, material_paise, operations_paise, byproduct_paise, total_paise, unit_cost_paise, created_at
		FROM production_receipts WHERE run_id=$1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		var r Receipt
		if err := rows.Scan(&r.ID, &r.Seq, &r.PostingID, &r.Final, &r.GoodQty, &r.RejectedQty, &r.BatchNo, &r.LabourHours,
			&r.MaterialPaise, &r.OperationsPaise, &r.ByproductPaise, &r.TotalPaise, &r.UnitCostPaise, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get loads a run; withAvailability adds shortages from bhandar for open runs.
func (s *Service) Get(ctx context.Context, p *auth.Principal, id string, withAvailability bool) (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM production_runs WHERE tenant_id=$1 AND id=$2`, p.TenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("production run not found")
	}
	if err != nil {
		return nil, err
	}
	if !canStore(p, r.StoreID) {
		return nil, httpx.NotFound("production run not found")
	}
	if r.Lines, err = loadLines(ctx, s.DB, id); err != nil {
		return nil, err
	}
	if r.ReceiptList, err = loadReceipts(ctx, s.DB, id); err != nil {
		return nil, err
	}
	if withAvailability && (r.Status == StatusPlanned || r.Status == StatusInProgress) && s.Inventory != nil {
		sh, err := s.shortages(ctx, p.TenantID, r)
		if err != nil {
			s.Log.Warn("availability check failed", "error", err.Error())
		} else {
			r.Shortages = sh
		}
	}
	return r, nil
}

// reserved is material issued to other in-progress runs at the store and not yet consumed.
func reserved(ctx context.Context, q db.Querier, tenantID, storeID, exceptRun string) (map[string]float64, error) {
	rows, err := q.Query(ctx, `SELECT l.item_id, sum(GREATEST(l.issued_qty - COALESCE(l.actual_qty,0), 0))::float8
		FROM production_run_lines l JOIN production_runs r ON r.id = l.run_id
		WHERE r.tenant_id=$1 AND r.store_id=$2 AND r.status='in_progress' AND r.id <> $3 AND l.kind='consume'
		GROUP BY l.item_id`, tenantID, storeID, exceptRun)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var q float64
		if err := rows.Scan(&id, &q); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}

func (s *Service) shortages(ctx context.Context, tenantID string, r *Run) ([]Shortage, error) {
	pos, err := s.Inventory.Stock(ctx, r.StoreID)
	if err != nil {
		return nil, err
	}
	onHand := make(map[string]float64, len(pos))
	for id, p := range pos {
		onHand[id] = p.OnHand
	}
	res, err := reserved(ctx, s.DB, tenantID, r.StoreID, r.ID)
	if err != nil {
		return nil, err
	}
	return Shortages(r.Lines, onHand, res), nil
}

// List lists runs.
func (s *Service) List(ctx context.Context, p *auth.Principal, storeID, status, search string, page httpx.Page) ([]Run, error) {
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) && storeID == "" {
		storeID = p.StoreID
	}
	if storeID != "" && !canStore(p, storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	rows, err := s.DB.Query(ctx, `SELECT `+runCols+` FROM production_runs WHERE tenant_id=$1
		AND ($2 = '' OR store_id::text=$2) AND ($3 = '' OR status=$3)
		AND ($4 = '' OR run_no ILIKE '%' || $4 || '%' OR item_name ILIKE '%' || $4 || '%')
		ORDER BY planned_date DESC, created_at DESC LIMIT $5 OFFSET $6`, p.TenantID, storeID, status, search, page.Limit, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// StartInput is the body of POST /v1/production-runs/{id}/start.
type StartInput struct {
	Force         bool   `json:"force"`
	IssueMaterial *bool  `json:"issue_material,omitempty"` // default true: issue the planned material (WIP)
	Note          string `json:"note,omitempty"`
}

// Start moves a planned run to in_progress, refusing when material is short
// unless force, and (by default) issues the planned material to the run.
func (s *Service) Start(ctx context.Context, p *auth.Principal, id string, in StartInput) (*Run, error) {
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "start"); err != nil {
		return nil, err
	}
	if !in.Force {
		if s.Inventory == nil {
			return nil, httpx.Unavailable("bhandar is not configured; pass force=true")
		}
		sh, err := s.shortages(ctx, p.TenantID, r)
		if err != nil {
			return nil, err
		}
		if len(sh) > 0 {
			return nil, httpx.Conflict("not enough material to start; pass force=true to override").
				WithDetails(map[string]any{"shortages": sh})
		}
	}
	issue := in.IssueMaterial == nil || *in.IssueMaterial
	var lines []issueLine
	if issue {
		for _, l := range r.Lines {
			if l.Kind == KindConsume && l.PlannedBaseQty > 0 {
				lines = append(lines, issueLine{line: l, qty: l.PlannedBaseQty})
			}
		}
	}
	prices := s.issuePrices(ctx, r.StoreID, lines)
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE production_runs SET status='in_progress', started_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='planned'`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("run changed concurrently")
		}
		if len(lines) > 0 {
			return recordIssue(ctx, tx, p, r.ID, in.Note, lines, prices)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id, false)
}

type issueLine struct {
	line Line
	qty  float64 // base unit
}

// issuePrices values an issue at the store's average cost (best effort: WIP is informational).
func (s *Service) issuePrices(ctx context.Context, storeID string, lines []issueLine) map[string]int64 {
	out := map[string]int64{}
	if s.Inventory == nil || len(lines) == 0 {
		return out
	}
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.line.ItemID)
	}
	prices, _, err := clients.ResolvePrices(ctx, s.Inventory, ids, storeID, clients.BasisAverage)
	if err != nil {
		s.Log.Warn("issue valuation failed", "error", err.Error())
		return out
	}
	for id, p := range prices {
		out[id] = p.Paise
	}
	return out
}

func recordIssue(ctx context.Context, tx pgx.Tx, p *auth.Principal, runID, note string, lines []issueLine, prices map[string]int64) error {
	var issueID string
	if err := tx.QueryRow(ctx, `INSERT INTO production_issues (tenant_id, run_id, seq, note, issued_by)
		VALUES ($1,$2,(SELECT COALESCE(max(seq),0)+1 FROM production_issues WHERE run_id=$2),$3,$4) RETURNING id`,
		p.TenantID, runID, nilIfEmpty(note), p.UserID()).Scan(&issueID); err != nil {
		return err
	}
	var value int64
	for _, l := range lines {
		var cost *int64
		if c, ok := prices[l.line.ItemID]; ok {
			cost = &c
			value += units.Extend(l.qty, c)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO production_issue_lines (issue_id, run_line_no, item_id, qty, unit_cost_paise)
			VALUES ($1,$2,$3,$4,$5)`, issueID, l.line.LineNo, l.line.ItemID, units.Round3(l.qty), cost); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE production_run_lines SET issued_qty = issued_qty + $3 WHERE run_id=$1 AND line_no=$2`,
			runID, l.line.LineNo, units.Round3(l.qty)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE production_runs SET issued_value_paise = issued_value_paise + $3 WHERE tenant_id=$1 AND id=$2`,
		p.TenantID, runID, value)
	return err
}

// Actual is a quantity of one item: a consumption override, an extra issue,
// or (with item_name) an item that is not in the BOM.
type Actual struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name,omitempty"`
	Qty      float64 `json:"qty"`
	Unit     string  `json:"unit,omitempty"` // default: the line's unit (the BOM's), or the item's stock unit
}

// IssueInput is the body of POST /v1/production-runs/{id}/issue.
type IssueInput struct {
	Lines []Actual `json:"lines"`
	Note  string   `json:"note,omitempty"`
}

// Validate checks the body.
func (in *IssueInput) Validate() error {
	if len(in.Lines) == 0 {
		return httpx.Validation("lines are required")
	}
	for _, a := range in.Lines {
		if a.ItemID == "" || a.Qty <= 0 {
			return httpx.Validation("issue lines need item_id and a positive qty")
		}
	}
	return nil
}

// Issue issues more material to an in-progress run (top-ups and items outside the BOM).
func (s *Service) Issue(ctx context.Context, p *auth.Principal, id string, in IssueInput) (*Run, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "issue"); err != nil {
		return nil, err
	}
	resolved, added, err := s.resolveActuals(ctx, r.Lines, in.Lines)
	if err != nil {
		return nil, err
	}
	var lines []issueLine
	all := append(append([]Line{}, r.Lines...), added...)
	for lineNo, q := range resolved {
		for _, l := range all {
			if l.LineNo == lineNo {
				lines = append(lines, issueLine{line: l, qty: q})
			}
		}
	}
	prices := s.issuePrices(ctx, r.StoreID, lines)
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockRun(ctx, tx, p.TenantID, id, StatusInProgress); err != nil {
			return err
		}
		for _, l := range added {
			if err := insertLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		return recordIssue(ctx, tx, p, id, in.Note, lines, prices)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id, false)
}

func lockRun(ctx context.Context, tx pgx.Tx, tenantID, id string, want ...string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM production_runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, id).Scan(&status)
	if db.IsNotFound(err) {
		return httpx.NotFound("production run not found")
	}
	if err != nil {
		return err
	}
	for _, w := range want {
		if status == w {
			return nil
		}
	}
	return httpx.Conflict("run changed concurrently (now %s)", status)
}

// resolveActuals maps Actual quantities onto run lines (base unit), creating
// unplanned consume lines for items not on the run.
func (s *Service) resolveActuals(ctx context.Context, lines []Line, acts []Actual) (map[int]float64, []Line, error) {
	out := map[int]float64{}
	var added []Line
	if len(acts) == 0 {
		return out, nil, nil
	}
	ids := make([]string, 0, len(acts))
	for _, a := range acts {
		ids = append(ids, a.ItemID)
	}
	cat, items, err := s.catalog(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	next := 0
	for _, l := range lines {
		if l.LineNo > next {
			next = l.LineNo
		}
	}
	byItem := map[string]Line{}
	for _, l := range lines {
		if l.Kind == KindConsume {
			byItem[l.ItemID] = l
		}
	}
	for _, a := range acts {
		l, ok := byItem[a.ItemID]
		if !ok {
			name := strings.TrimSpace(a.ItemName)
			if it, ok := items[a.ItemID]; ok && name == "" {
				name = it.Name
			}
			if name == "" {
				return nil, nil, httpx.Validation("item %s is not in the BOM; give item_name (or create it in bhandar)", a.ItemID)
			}
			unit := a.Unit
			if unit == "" {
				if it, ok := items[a.ItemID]; ok {
					unit = it.Unit
				}
			}
			f, base, err := cat.FactorFor(a.ItemID, unit)
			if err != nil {
				return nil, nil, httpx.Validation("%s: %s", name, err.Error())
			}
			next++
			l = Line{LineNo: next, Kind: KindConsume, ItemID: a.ItemID, ItemName: name, Unit: units.Norm(unit),
				BaseUnit: base, Factor: f, Unplanned: true}
			added = append(added, l)
			byItem[a.ItemID] = l
		}
		f := l.Factor
		if a.Unit != "" && units.Norm(a.Unit) != l.Unit {
			fa, _, err := cat.FactorFor(a.ItemID, a.Unit)
			if err != nil {
				return nil, nil, httpx.Validation("%s: %s", l.ItemName, err.Error())
			}
			f = fa
		}
		out[l.LineNo] += units.Round3(a.Qty * f)
	}
	return out, added, nil
}

// Cancel cancels an open run that has not booked any completion (no stock has moved).
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, id string) (*Run, error) {
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "cancel"); err != nil {
		return nil, err
	}
	if r.Receipts > 0 {
		return nil, httpx.Conflict("the run has %d completion(s); record a final completion instead of cancelling", r.Receipts)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE production_runs SET status='cancelled', cancelled_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status IN ('planned','in_progress') AND receipts=0`, p.TenantID, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id, false)
}

// WIP lists in-progress runs with material issued and not yet consumed.
func (s *Service) WIP(ctx context.Context, p *auth.Principal, storeID string) ([]Run, int64, error) {
	runs, err := s.List(ctx, p, storeID, StatusInProgress, "", httpx.Page{Limit: 500})
	if err != nil {
		return nil, 0, err
	}
	var total int64
	for _, r := range runs {
		total += r.WIPPaise
	}
	return runs, total, nil
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}
