package production

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/internal/units"
)

// ByProduct is a secondary output received into stock (bran from flour, offcuts).
type ByProduct struct {
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name,omitempty"`
	Qty           float64 `json:"qty"`
	Unit          string  `json:"unit,omitempty"`            // default: the item's stock unit
	UnitCostPaise int64   `json:"unit_cost_paise,omitempty"` // value per `unit`; default bhandar's average cost
}

// ManualCost prices an item bhandar has no cost for (per stock unit).
type ManualCost struct {
	ItemID        string `json:"item_id"`
	UnitCostPaise int64  `json:"unit_cost_paise"`
}

// CompleteInput is the body of POST /v1/production-runs/{id}/complete.
type CompleteInput struct {
	// OutputQty is the good output of this completion in the BOM's unit.
	// Omitted on a final completion: the rest of the planned qty. 0 on a
	// final completion with nothing good: a failed batch.
	OutputQty     *float64     `json:"output_qty,omitempty"`
	RejectedQty   float64      `json:"rejected_qty,omitempty"`
	Final         *bool        `json:"final,omitempty"` // default true; false = partial completion, run stays open
	OutputBatchNo string       `json:"output_batch_no,omitempty"`
	Consumed      []Actual     `json:"consumed,omitempty"` // overrides; items outside the BOM = unplanned consumption
	ByProducts    []ByProduct  `json:"by_products,omitempty"`
	UnitCosts     []ManualCost `json:"unit_costs,omitempty"` // only used for items with no bhandar cost
	LabourHours   *float64     `json:"labour_hours,omitempty"`
	FailureReason string       `json:"failure_reason,omitempty"`
}

// Validate checks the body.
func (in *CompleteInput) Validate() error {
	if in.OutputQty != nil && *in.OutputQty < 0 {
		return httpx.Validation("output_qty cannot be negative")
	}
	if in.RejectedQty < 0 {
		return httpx.Validation("rejected_qty cannot be negative")
	}
	if in.LabourHours != nil && *in.LabourHours < 0 {
		return httpx.Validation("labour_hours cannot be negative")
	}
	for _, a := range in.Consumed {
		if a.ItemID == "" || a.Qty < 0 {
			return httpx.Validation("consumed lines need item_id and non-negative qty")
		}
	}
	for _, b := range in.ByProducts {
		if b.ItemID == "" || b.Qty <= 0 || b.UnitCostPaise < 0 {
			return httpx.Validation("by_products need item_id, a positive qty and a non-negative unit_cost_paise")
		}
	}
	for _, m := range in.UnitCosts {
		if m.ItemID == "" || m.UnitCostPaise <= 0 {
			return httpx.Validation("unit_costs need item_id and a positive unit_cost_paise")
		}
	}
	return nil
}

func (in *CompleteInput) final() bool { return in.Final == nil || *in.Final }

// CompletedEvent is nirmaan.production.completed: mool's payload plus
// nirmaan's run context (extra fields are ignored by older consumers).
// Quantities are in each item's stock unit, which Unit names.
type CompletedEvent struct {
	events.ProductionCompletedPayload
	RunNo               string  `json:"run_no"`
	ReceiptSeq          int     `json:"receipt_seq"`
	Final               bool    `json:"final"`
	Failed              bool    `json:"failed"`
	GoodQty             float64 `json:"good_qty"`
	RejectedQty         float64 `json:"rejected_qty"`
	MaterialPaise       int64   `json:"material_paise"`
	OperationsPaise     int64   `json:"operations_paise"`
	ByproductValuePaise int64   `json:"byproduct_value_paise"`
}

// PostingID is the production_run_id carried by a receipt's event: the run id
// for the first receipt (so a single completion looks as it always did) and
// "<run id>/<seq>" for later ones, keeping bhandar/bahi idempotency per receipt.
func PostingID(runID string, seq int) string {
	if seq <= 1 {
		return runID
	}
	return fmt.Sprintf("%s/%d", runID, seq)
}

// Complete records one completion: partial (run stays in progress), final
// (completed) or a failed batch (final with no good output). It costs the
// material at bhandar's weighted-average cost, refuses to book anything at ₹0
// (give unit_costs for items bhandar cannot price) and publishes
// nirmaan.production.completed.
func (s *Service) Complete(ctx context.Context, p *auth.Principal, id string, in CompleteInput) (*Run, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	r, err := s.Get(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	final := in.final()
	if _, err := NextStatus(r.Status, "receive"); err != nil {
		return nil, err
	}
	if s.Inventory == nil {
		return nil, httpx.Unavailable("bhandar is not configured; costs cannot be read")
	}
	b, err := bom.Load(ctx, s.DB, p.TenantID, r.BOMID)
	if err != nil {
		return nil, err
	}
	priorGood := 0.0
	if r.OutputQty != nil {
		priorGood = *r.OutputQty
	}
	prior := priorGood + r.RejectedQty
	good := 0.0
	switch {
	case in.OutputQty != nil:
		good = units.Round3(*in.OutputQty)
	case final:
		if good = units.Round3(r.Qty - prior - in.RejectedQty); good < 0 {
			good = 0
		}
	default:
		return nil, httpx.Validation("a partial completion needs output_qty")
	}
	failed := final && good == 0 && priorGood == 0

	// consumption overrides and unplanned lines
	overrides, added, err := s.resolveActuals(ctx, r.Lines, in.Consumed)
	if err != nil {
		return nil, err
	}
	lines := append(append([]Line{}, r.Lines...), added...)

	// by-products: convert to stock units, take a line number each
	next := 0
	for _, l := range lines {
		if l.LineNo > next {
			next = l.LineNo
		}
	}
	var bps []ByProductLine
	var bpLines []Line
	if len(in.ByProducts) > 0 {
		ids := make([]string, 0, len(in.ByProducts))
		for _, bp := range in.ByProducts {
			ids = append(ids, bp.ItemID)
		}
		cat, items, err := s.catalog(ctx, ids)
		if err != nil {
			return nil, err
		}
		existing := map[string]Line{}
		for _, l := range lines {
			if l.Kind == KindByproduct {
				existing[l.ItemID] = l
			}
		}
		for _, bp := range in.ByProducts {
			it := items[bp.ItemID]
			name := strings.TrimSpace(bp.ItemName)
			if name == "" {
				name = it.Name
			}
			if name == "" {
				return nil, httpx.Validation("by-product %s needs item_name (or create it in bhandar)", bp.ItemID)
			}
			unit := bp.Unit
			if unit == "" {
				unit = it.Unit
			}
			f, base, err := cat.FactorFor(bp.ItemID, unit)
			if err != nil {
				return nil, httpx.Validation("%s: %s", name, err.Error())
			}
			l, ok := existing[bp.ItemID]
			if !ok {
				next++
				l = Line{LineNo: next, Kind: KindByproduct, ItemID: bp.ItemID, ItemName: name, Unit: units.Norm(unit), BaseUnit: base, Factor: f, Unplanned: true}
				bpLines = append(bpLines, l)
				existing[bp.ItemID] = l
			}
			rate := Price{}
			if bp.UnitCostPaise > 0 {
				rate = Price{Paise: units.MulDiv(bp.UnitCostPaise, 1000, units.Milli(f)), Source: clients.SourceManual}
			}
			bps = append(bps, ByProductLine{LineNo: l.LineNo, ItemID: bp.ItemID, ItemName: name, BaseUnit: base, Qty: bp.Qty * f, Rate: rate})
		}
	}

	// prices: bhandar weighted average → sub-assembly roll-up → manual
	ids := []string{}
	for _, l := range lines {
		if l.Kind == KindConsume {
			ids = append(ids, l.ItemID)
		}
	}
	for _, bp := range bps {
		if bp.Rate.Paise == 0 {
			ids = append(ids, bp.ItemID)
		}
	}
	got, _, err := clients.ResolvePrices(ctx, s.Inventory, ids, r.StoreID, clients.BasisAverage)
	if err != nil {
		return nil, err
	}
	prices := map[string]Price{}
	for id, pr := range got {
		prices[id] = Price{Paise: pr.Paise, Source: pr.Source}
	}
	for _, l := range lines {
		if _, ok := prices[l.ItemID]; ok || l.Kind != KindConsume || !l.IsSubAssembly {
			continue
		}
		if pr, ok := s.rollupPrice(ctx, p.TenantID, r.StoreID, l.ItemID, r.PlannedDate); ok {
			prices[l.ItemID] = pr
		}
	}
	for _, m := range in.UnitCosts {
		if pr, ok := prices[m.ItemID]; !ok || pr.Paise <= 0 {
			prices[m.ItemID] = Price{Paise: m.UnitCostPaise, Source: clients.SourceManual}
		}
	}
	for i := range bps {
		if bps[i].Rate.Paise == 0 {
			bps[i].Rate = prices[bps[i].ItemID]
		}
	}

	calc, err := CalcReceipt(ReceiptInput{
		BOM: b, Lines: lines, RunQty: r.Qty, PriorPortion: prior, Good: good, Rejected: in.RejectedQty, Final: final,
		Overrides: overrides, ByProducts: bps, LabourHours: in.LabourHours, Prices: prices,
	})
	if err != nil {
		return nil, err
	}
	if len(calc.Missing) > 0 {
		names := []string{}
		for _, id := range calc.Missing {
			for _, l := range lines {
				if l.ItemID == id {
					names = append(names, l.ItemName)
					break
				}
			}
		}
		return nil, httpx.Validation("no cost for %s: bhandar has no average cost or purchase price; give unit_costs (paise per stock unit) to complete",
			strings.Join(names, ", ")).WithDetails(map[string]any{"missing_prices": calc.Missing})
	}

	var receiptSeq int
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockRun(ctx, tx, p.TenantID, id, StatusPlanned, StatusInProgress); err != nil {
			return err
		}
		var receipts int
		if err := tx.QueryRow(ctx, `SELECT receipts FROM production_runs WHERE id=$1`, id).Scan(&receipts); err != nil {
			return err
		}
		if receipts != r.Receipts {
			return httpx.Conflict("run changed concurrently")
		}
		receiptSeq = receipts + 1
		postingID := PostingID(r.ID, receiptSeq)
		for _, l := range append(added, bpLines...) {
			if err := insertLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		var receiptID string
		if err := tx.QueryRow(ctx, `INSERT INTO production_receipts (tenant_id, run_id, seq, posting_id, is_final, good_qty,
			rejected_qty, batch_no, labour_hours, material_paise, operations_paise, byproduct_paise, total_paise, unit_cost_paise, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`,
			p.TenantID, id, receiptSeq, postingID, final, good, units.Round3(in.RejectedQty), nilIfEmpty(in.OutputBatchNo),
			calc.LabourHours, calc.MaterialPaise, calc.OperationsPaise, calc.ByproductPaise, calc.TotalPaise, calc.UnitCostPaise,
			p.UserID()).Scan(&receiptID); err != nil {
			return err
		}
		pl := CompletedEvent{
			ProductionCompletedPayload: events.ProductionCompletedPayload{
				ProductionRunID: postingID, StoreID: r.StoreID, BOMID: r.BOMID, TotalCostPaise: calc.TotalPaise,
				CompletedAt: s.now().UTC().Format(time.RFC3339), Consumed: []events.ProductionLine{}, Produced: []events.ProductionLine{},
			},
			RunNo: r.RunNo, ReceiptSeq: receiptSeq, Final: final, Failed: failed, GoodQty: good, RejectedQty: units.Round3(in.RejectedQty),
			MaterialPaise: calc.MaterialPaise, OperationsPaise: calc.OperationsPaise, ByproductValuePaise: calc.ByproductPaise,
		}
		for _, cl := range calc.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO production_receipt_lines (receipt_id, run_line_no, kind, item_id, qty, unit_cost_paise, price_source)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, receiptID, cl.LineNo, cl.Kind, cl.ItemID, cl.Qty, cl.UnitCostPaise, nilIfEmpty(cl.PriceSource)); err != nil {
				return err
			}
			// line totals: actual qty accumulates; the unit cost is the running weighted average
			if _, err := tx.Exec(ctx, `UPDATE production_run_lines SET
				unit_cost_paise = CASE WHEN COALESCE(actual_qty,0) + $3 > 0
					THEN round((COALESCE(actual_qty,0) * COALESCE(unit_cost_paise,0) + $3 * $4) / (COALESCE(actual_qty,0) + $3))::bigint
					ELSE $4 END,
				actual_qty = COALESCE(actual_qty,0) + $3
				WHERE run_id=$1 AND line_no=$2`, id, cl.LineNo, cl.Qty, cl.UnitCostPaise); err != nil {
				return err
			}
			ev := events.ProductionLine{ItemID: cl.ItemID, Qty: cl.Qty, Unit: cl.BaseUnit, UnitCostPaise: cl.UnitCostPaise}
			switch cl.Kind {
			case KindConsume:
				pl.Consumed = append(pl.Consumed, ev)
			case KindProduce:
				ev.BatchNo = strings.TrimSpace(in.OutputBatchNo)
				pl.Produced = append(pl.Produced, ev)
			case KindByproduct:
				pl.Produced = append(pl.Produced, ev)
			}
		}
		status := StatusInProgress
		switch {
		case failed:
			status = StatusFailed
		case final:
			status = StatusCompleted
		}
		if _, err := tx.Exec(ctx, `UPDATE production_runs SET status=$3, receipts=$4,
			output_qty = COALESCE(output_qty,0) + $5, rejected_qty = rejected_qty + $6,
			material_cost_paise = material_cost_paise + $7, operations_cost_paise = operations_cost_paise + $8,
			byproduct_value_paise = byproduct_value_paise + $9, total_cost_paise = COALESCE(total_cost_paise,0) + $10,
			actual_hours = actual_hours + $11, output_batch_no = COALESCE($12, output_batch_no),
			failure_reason = COALESCE($13, failure_reason), started_at = COALESCE(started_at, now()),
			completed_at = CASE WHEN $14 THEN now() ELSE completed_at END
			WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, id, status, receiptSeq, good, units.Round3(in.RejectedQty), calc.MaterialPaise, calc.OperationsPaise,
			calc.ByproductPaise, calc.TotalPaise, calc.LabourHours, nilIfEmpty(in.OutputBatchNo), nilIfEmpty(in.FailureReason), final); err != nil {
			return err
		}
		if len(pl.Consumed)+len(pl.Produced) == 0 {
			return nil // nothing moved (e.g. a partial with only overrides of 0)
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

// rollupPrice prices a sub-assembly with no stock cost from its effective BOM (per stock unit).
func (s *Service) rollupPrice(ctx context.Context, tenantID, storeID, itemID, date string) (Price, bool) {
	if s.BOMs == nil {
		return Price{}, false
	}
	bomID, err := bom.EffectiveBOMID(ctx, s.DB, tenantID, itemID, date)
	if err != nil || bomID == "" {
		return Price{}, false
	}
	res, err := s.BOMs.CostByID(ctx, tenantID, bomID, 0, bom.CostOptions{StoreID: storeID, Date: date})
	if err != nil || !res.Complete {
		return Price{}, false
	}
	sb, err := bom.Load(ctx, s.DB, tenantID, bomID)
	if err != nil {
		return Price{}, false
	}
	f := 1.0
	if s.Inventory != nil {
		if items, err := s.Inventory.Lookup(ctx, []string{itemID}); err == nil {
			if it, ok := items[itemID]; ok {
				if ff, err := units.Factor(sb.Unit, it.Units()); err == nil {
					f = ff
				}
			}
		}
	}
	return Price{Paise: units.Rate(res.TotalPaise, sb.OutputQty*f), Source: clients.SourceRollup}, true
}
