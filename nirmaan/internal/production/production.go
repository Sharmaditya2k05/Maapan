// Package production runs BOMs: plan → start → complete, consuming raw
// material and producing finished goods in bhandar via nirmaan.production.completed.
package production

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/nirmaan/internal/bom"
	"github.com/taraju/nirmaan/internal/clients"
)

// Run statuses.
const (
	StatusPlanned    = "planned"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

// NextStatus validates a run state change for action (start | complete | cancel).
func NextStatus(current, action string) (string, error) {
	switch {
	case action == "start" && current == StatusPlanned:
		return StatusInProgress, nil
	case action == "complete" && (current == StatusPlanned || current == StatusInProgress):
		return StatusCompleted, nil
	case action == "cancel" && (current == StatusPlanned || current == StatusInProgress):
		return StatusCancelled, nil
	}
	return "", httpx.Conflict("cannot %s a run that is %s", action, current)
}

// PlanLines turns a BOM into the direct component quantities (with scrap) for qty output.
// Sub-assemblies are consumed as stock items; produce them in their own runs.
func PlanLines(b *bom.BOM, qty float64) []Line {
	lines := make([]Line, 0, len(b.Components)+1)
	for i, c := range b.Components {
		lines = append(lines, Line{
			LineNo: i + 1, Kind: "consume", ItemID: c.ItemID, ItemName: c.ItemName, Unit: c.Unit,
			IsSubAssembly: c.IsSubAssembly,
			PlannedQty:    bom.Round3(c.Qty / b.OutputQty * qty * (1 + c.ScrapPercent/100)),
		})
	}
	lines = append(lines, Line{LineNo: len(b.Components) + 1, Kind: "produce", ItemID: b.ItemID, ItemName: b.ItemName,
		Unit: b.Unit, PlannedQty: bom.Round3(qty)})
	return lines
}

// Shortage is a component the store does not have enough of.
type Shortage struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name"`
	Required float64 `json:"required"`
	OnHand   float64 `json:"on_hand"`
	Short    float64 `json:"short"`
}

// Shortages compares consume lines with on-hand stock.
func Shortages(lines []Line, onHand map[string]float64) []Shortage {
	out := []Shortage{}
	for _, l := range lines {
		if l.Kind != "consume" {
			continue
		}
		if have := onHand[l.ItemID]; have < l.PlannedQty {
			out = append(out, Shortage{ItemID: l.ItemID, ItemName: l.ItemName, Required: l.PlannedQty, OnHand: have,
				Short: bom.Round3(l.PlannedQty - have)})
		}
	}
	return out
}

// Run is a production run.
type Run struct {
	ID             string     `json:"id"`
	BOMID          string     `json:"bom_id"`
	ItemID         string     `json:"item_id"`
	ItemName       string     `json:"item_name"`
	StoreID        string     `json:"store_id"`
	Qty            float64    `json:"qty"`
	OutputQty      *float64   `json:"output_qty,omitempty"`
	PlannedDate    string     `json:"planned_date"`
	Status         string     `json:"status"`
	OutputBatchNo  *string    `json:"output_batch_no,omitempty"`
	TotalCostPaise *int64     `json:"total_cost_paise,omitempty"`
	Notes          *string    `json:"notes,omitempty"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CancelledAt    *time.Time `json:"cancelled_at,omitempty"`
	Lines          []Line     `json:"lines,omitempty"`
	Shortages      []Shortage `json:"shortages,omitempty"`
}

// Line is a planned/actual material line.
type Line struct {
	LineNo        int      `json:"line_no"`
	Kind          string   `json:"kind"` // consume | produce
	ItemID        string   `json:"item_id"`
	ItemName      string   `json:"item_name"`
	Unit          string   `json:"unit"`
	IsSubAssembly bool     `json:"is_sub_assembly"`
	PlannedQty    float64  `json:"planned_qty"`
	ActualQty     *float64 `json:"actual_qty,omitempty"`
	UnitCostPaise *int64   `json:"unit_cost_paise,omitempty"`
}

// Service implements production runs.
type Service struct {
	DB      *pgxpool.Pool
	BOMs    *bom.Service
	Bhandar *clients.Bhandar
	Relay   *events.Relay
	Log     *slog.Logger
	Now     func() time.Time
}

// CreateInput is the body of POST /v1/production-runs.
type CreateInput struct {
	BOMID       string  `json:"bom_id"`
	Qty         float64 `json:"qty"`
	StoreID     string  `json:"store_id,omitempty"`
	PlannedDate string  `json:"planned_date,omitempty"`
	Notes       string  `json:"notes,omitempty"`
}

// Validate checks the body.
func (in *CreateInput) Validate() error {
	if in.BOMID == "" || in.Qty <= 0 {
		return httpx.Validation("bom_id and a positive qty are required")
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

var ist = time.FixedZone("IST", 5*3600+1800)

// Create plans a run from an active BOM and reports material availability.
func (s *Service) Create(ctx context.Context, p *auth.Principal, in CreateInput) (*Run, error) {
	store := in.StoreID
	if store == "" {
		store = p.StoreID
	}
	if store == "" || !canStore(p, store) {
		return nil, httpx.Forbidden("store_id missing or not accessible")
	}
	b, err := bom.Load(ctx, s.DB, p.TenantID, in.BOMID)
	if err != nil {
		return nil, err
	}
	if b.Status != bom.StatusActive {
		return nil, httpx.Validation("only an active BOM can be produced")
	}
	date := in.PlannedDate
	if date == "" {
		date = s.Now().In(ist).Format(time.DateOnly)
	}
	var id string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO production_runs (tenant_id, bom_id, item_id, item_name, store_id, qty,
			planned_date, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			p.TenantID, b.ID, b.ItemID, b.ItemName, store, bom.Round3(in.Qty), date, nilIfEmpty(in.Notes), p.UserID()).Scan(&id); err != nil {
			return err
		}
		for _, l := range PlanLines(b, in.Qty) {
			if _, err := tx.Exec(ctx, `INSERT INTO production_run_lines (run_id, line_no, kind, item_id, item_name, unit,
				is_sub_assembly, planned_qty) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				id, l.LineNo, l.Kind, l.ItemID, l.ItemName, l.Unit, l.IsSubAssembly, l.PlannedQty); err != nil {
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

const runCols = `id, bom_id, item_id, item_name, store_id, qty::float8, output_qty::float8, to_char(planned_date,'YYYY-MM-DD'),
	status, output_batch_no, total_cost_paise, notes, created_by, created_at, started_at, completed_at, cancelled_at`

func scanRun(row pgx.Row) (*Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.BOMID, &r.ItemID, &r.ItemName, &r.StoreID, &r.Qty, &r.OutputQty, &r.PlannedDate,
		&r.Status, &r.OutputBatchNo, &r.TotalCostPaise, &r.Notes, &r.CreatedBy, &r.CreatedAt, &r.StartedAt,
		&r.CompletedAt, &r.CancelledAt)
	return &r, err
}

func loadLines(ctx context.Context, q db.Querier, runID string) ([]Line, error) {
	rows, err := q.Query(ctx, `SELECT line_no, kind, item_id, item_name, unit, is_sub_assembly, planned_qty::float8,
		actual_qty::float8, unit_cost_paise FROM production_run_lines WHERE run_id=$1 ORDER BY line_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.LineNo, &l.Kind, &l.ItemID, &l.ItemName, &l.Unit, &l.IsSubAssembly, &l.PlannedQty,
			&l.ActualQty, &l.UnitCostPaise); err != nil {
			return nil, err
		}
		out = append(out, l)
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
	if withAvailability && (r.Status == StatusPlanned || r.Status == StatusInProgress) {
		onHand, err := s.Bhandar.OnHand(ctx, r.StoreID)
		if err != nil {
			s.Log.Warn("availability check failed", "error", err.Error())
		} else {
			r.Shortages = Shortages(r.Lines, onHand)
		}
	}
	return r, nil
}

// List lists runs.
func (s *Service) List(ctx context.Context, p *auth.Principal, storeID, status string, page httpx.Page) ([]Run, error) {
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) && storeID == "" {
		storeID = p.StoreID
	}
	if storeID != "" && !canStore(p, storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	rows, err := s.DB.Query(ctx, `SELECT `+runCols+` FROM production_runs WHERE tenant_id=$1
		AND ($2 = '' OR store_id::text=$2) AND ($3 = '' OR status=$3)
		ORDER BY planned_date DESC, created_at DESC LIMIT $4 OFFSET $5`, p.TenantID, storeID, status, page.Limit, page.Offset)
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

// Start moves a planned run to in_progress, refusing when material is short unless force.
func (s *Service) Start(ctx context.Context, p *auth.Principal, id string, force bool) (*Run, error) {
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "start"); err != nil {
		return nil, err
	}
	if !force {
		onHand, err := s.Bhandar.OnHand(ctx, r.StoreID)
		if err != nil {
			return nil, err
		}
		if sh := Shortages(r.Lines, onHand); len(sh) > 0 {
			return nil, httpx.Conflict("not enough material to start; pass force=true to override").
				WithDetails(map[string]any{"shortages": sh})
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE production_runs SET status='in_progress', started_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='planned'`, p.TenantID, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.Conflict("run changed concurrently")
	}
	return s.Get(ctx, p, id, false)
}

// Cancel cancels an open run (no stock has moved yet).
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, id string) (*Run, error) {
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "cancel"); err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE production_runs SET status='cancelled', cancelled_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status IN ('planned','in_progress')`, p.TenantID, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id, false)
}

// Actual overrides the consumed quantity of one item.
type Actual struct {
	ItemID string  `json:"item_id"`
	Qty    float64 `json:"qty"`
}

// CompleteInput is the body of POST /v1/production-runs/{id}/complete.
type CompleteInput struct {
	OutputQty     float64  `json:"output_qty,omitempty"` // default: planned qty
	OutputBatchNo string   `json:"output_batch_no,omitempty"`
	Consumed      []Actual `json:"consumed,omitempty"` // default: planned quantities
}

// Validate checks the body.
func (in *CompleteInput) Validate() error {
	if in.OutputQty < 0 {
		return httpx.Validation("output_qty cannot be negative")
	}
	for _, a := range in.Consumed {
		if a.ItemID == "" || a.Qty < 0 {
			return httpx.Validation("consumed lines need item_id and non-negative qty")
		}
	}
	return nil
}

// CostRun prices consumption: rawCost is paise/unit per consumed item; ops are
// the BOM's operation costs per OutputQty batch. Returns per-line costs, total and unit cost.
func CostRun(lines []Line, actual map[string]float64, unitCost map[string]int64, opsPerBatch int64,
	bomOutput, plannedQty, outputQty float64) (consumeCost map[string]int64, total int64, unit int64) {
	consumeCost = map[string]int64{}
	var sum float64
	for _, l := range lines {
		if l.Kind != "consume" {
			continue
		}
		q := l.PlannedQty
		if a, ok := actual[l.ItemID]; ok {
			q = a
		}
		consumeCost[l.ItemID] = unitCost[l.ItemID]
		sum += q * float64(unitCost[l.ItemID])
	}
	sum += float64(opsPerBatch) * plannedQty / bomOutput
	total = int64(math.Round(sum))
	if outputQty > 0 {
		unit = int64(math.Round(sum / outputQty))
	}
	return consumeCost, total, unit
}

// Complete records actuals, costs the run and publishes nirmaan.production.completed
// so bhandar consumes raw material and receives finished goods.
func (s *Service) Complete(ctx context.Context, p *auth.Principal, id string, in CompleteInput) (*Run, error) {
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := NextStatus(r.Status, "complete"); err != nil {
		return nil, err
	}
	b, err := bom.Load(ctx, s.DB, p.TenantID, r.BOMID)
	if err != nil {
		return nil, err
	}
	// Unit costs: raw materials at bhandar purchase price, sub-assemblies at rolled-up cost.
	res, err := s.BOMs.CostByID(ctx, p.TenantID, b.ID, b.OutputQty)
	if err != nil {
		return nil, err
	}
	unitCost := map[string]int64{}
	for _, cl := range res.Lines {
		unitCost[cl.ItemID] = cl.UnitCostPaise
	}
	actual := map[string]float64{}
	for _, a := range in.Consumed {
		actual[a.ItemID] = bom.Round3(a.Qty)
	}
	outputQty := r.Qty
	if in.OutputQty > 0 {
		outputQty = bom.Round3(in.OutputQty)
	}
	var ops int64
	for _, o := range b.Operations {
		ops += o.CostPaise
	}
	costs, total, unit := CostRun(r.Lines, actual, unitCost, ops, b.OutputQty, r.Qty, outputQty)

	pl := events.ProductionCompletedPayload{
		ProductionRunID: r.ID, StoreID: r.StoreID, BOMID: r.BOMID, TotalCostPaise: total,
		CompletedAt: s.Now().UTC().Format(time.RFC3339),
	}
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE production_runs SET status='completed', completed_at=now(), output_qty=$3,
			output_batch_no=$4, total_cost_paise=$5, started_at=COALESCE(started_at, now())
			WHERE tenant_id=$1 AND id=$2 AND status IN ('planned','in_progress')`,
			p.TenantID, id, outputQty, nilIfEmpty(in.OutputBatchNo), total)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("run changed concurrently")
		}
		for _, l := range r.Lines {
			q, c := l.PlannedQty, costs[l.ItemID]
			if l.Kind == "produce" {
				q, c = outputQty, unit
				pl.Produced = append(pl.Produced, events.ProductionLine{ItemID: l.ItemID, Qty: q, UnitCostPaise: c,
					BatchNo: strings.TrimSpace(in.OutputBatchNo)})
			} else {
				if a, ok := actual[l.ItemID]; ok {
					q = a
				}
				if q > 0 {
					pl.Consumed = append(pl.Consumed, events.ProductionLine{ItemID: l.ItemID, Qty: q, UnitCostPaise: c})
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE production_run_lines SET actual_qty=$3, unit_cost_paise=$4
				WHERE run_id=$1 AND line_no=$2`, id, l.LineNo, q, c); err != nil {
				return err
			}
		}
		ev, err := events.New(events.ProductionCompleted, "nirmaan", p.TenantID, r.StoreID, pl)
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	if s.Relay != nil {
		s.Relay.Kick()
	}
	return s.Get(ctx, p, id, false)
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}
