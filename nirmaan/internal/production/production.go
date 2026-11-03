// Package production runs BOMs: plan → start (material issue, WIP) → one or
// more completions (partial or final, good + rejected output, by-products,
// unplanned consumption) or a failed batch. Each completion publishes
// nirmaan.production.completed, so bhandar consumes raw material and receives
// finished goods, and bahi books the transfer.
//
// Quantities on events are in each item's stock (base) unit; money is int64
// paise computed with integer arithmetic.
package production

import (
	"sort"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/units"
)

// Run statuses.
const (
	StatusPlanned    = "planned"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// Line kinds.
const (
	KindConsume   = "consume"
	KindProduce   = "produce"
	KindByproduct = "byproduct"
)

// NextStatus validates a run state change for action
// (start | issue | receive | complete | fail | cancel).
func NextStatus(current, action string) (string, error) {
	open := current == StatusPlanned || current == StatusInProgress
	switch {
	case action == "start" && current == StatusPlanned:
		return StatusInProgress, nil
	case action == "issue" && current == StatusInProgress:
		return StatusInProgress, nil
	case action == "receive" && open:
		return StatusInProgress, nil
	case action == "complete" && open:
		return StatusCompleted, nil
	case action == "fail" && open:
		return StatusFailed, nil
	case action == "cancel" && open:
		return StatusCancelled, nil
	}
	return "", httpx.Conflict("cannot %s a run that is %s", action, current)
}

// Line is a planned/actual material line. PlannedQty is in Unit (as the BOM
// says); PlannedBaseQty, IssuedQty and ActualQty are in BaseUnit (stock unit).
type Line struct {
	LineNo         int      `json:"line_no"`
	Kind           string   `json:"kind"` // consume | produce | byproduct
	ItemID         string   `json:"item_id"`
	ItemName       string   `json:"item_name"`
	Unit           string   `json:"unit"`
	BaseUnit       string   `json:"base_unit"`
	Factor         float64  `json:"factor"` // base units per Unit
	IsSubAssembly  bool     `json:"is_sub_assembly"`
	Unplanned      bool     `json:"unplanned"`
	PlannedQty     float64  `json:"planned_qty"`
	PlannedBaseQty float64  `json:"planned_base_qty"`
	IssuedQty      float64  `json:"issued_qty"`
	ActualQty      *float64 `json:"actual_qty,omitempty"`
	UnitCostPaise  *int64   `json:"unit_cost_paise,omitempty"` // per base unit
}

func (l Line) actual() float64 {
	if l.ActualQty == nil {
		return 0
	}
	return *l.ActualQty
}

// PlanLines turns a BOM into the direct component quantities (yield and scrap
// included) for qty output, converting each to its stock unit through cat.
// Sub-assemblies are consumed as stock items; produce them in their own runs.
func PlanLines(b *bom.BOM, qty float64, cat units.Catalog) ([]Line, error) {
	lines := make([]Line, 0, len(b.Components)+1)
	for i, c := range b.Components {
		f, base, err := cat.FactorFor(c.ItemID, c.Unit)
		if err != nil {
			return nil, httpx.Validation("%s: %s", c.ItemName, err.Error())
		}
		need := bom.GrossQty(c, b, qty)
		lines = append(lines, Line{
			LineNo: i + 1, Kind: KindConsume, ItemID: c.ItemID, ItemName: c.ItemName, Unit: units.Norm(c.Unit),
			BaseUnit: base, Factor: f, IsSubAssembly: c.IsSubAssembly,
			PlannedQty: units.Round3(need), PlannedBaseQty: units.Round3(need * f),
		})
	}
	f, base, err := cat.FactorFor(b.ItemID, b.Unit)
	if err != nil {
		return nil, httpx.Validation("%s: %s", b.ItemName, err.Error())
	}
	lines = append(lines, Line{LineNo: len(b.Components) + 1, Kind: KindProduce, ItemID: b.ItemID, ItemName: b.ItemName,
		Unit: units.Norm(b.Unit), BaseUnit: base, Factor: f, PlannedQty: units.Round3(qty), PlannedBaseQty: units.Round3(qty * f)})
	return lines, nil
}

// Shortage is a component the store does not have enough of.
type Shortage struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name"`
	Unit     string  `json:"unit"`
	Required float64 `json:"required"`
	OnHand   float64 `json:"on_hand"`
	Reserved float64 `json:"reserved,omitempty"` // issued to other open runs, not yet consumed
	Short    float64 `json:"short"`
}

// Shortages compares the still-to-consume base quantity of consume lines with
// on-hand stock less what other open runs have been issued.
func Shortages(lines []Line, onHand, reserved map[string]float64) []Shortage {
	out := []Shortage{}
	for _, l := range lines {
		if l.Kind != KindConsume {
			continue
		}
		need := units.Round3(l.PlannedBaseQty - l.actual())
		if need <= 0 {
			continue
		}
		have := units.Round3(onHand[l.ItemID] - reserved[l.ItemID])
		if have < need {
			out = append(out, Shortage{ItemID: l.ItemID, ItemName: l.ItemName, Unit: l.BaseUnit, Required: need,
				OnHand: onHand[l.ItemID], Reserved: reserved[l.ItemID], Short: units.Round3(need - have)})
		}
	}
	return out
}

// Price is a cost per base unit with its source (see clients.Source*).
type Price struct {
	Paise  int64
	Source string
}

// ReceiptInput is everything CalcReceipt needs about one completion.
type ReceiptInput struct {
	BOM          *bom.BOM
	Lines        []Line // run lines, ActualQty = consumed/produced so far
	RunQty       float64
	PriorPortion float64 // good + rejected of earlier receipts (BOM unit)
	Good         float64 // good output this receipt (BOM unit)
	Rejected     float64 // rejected/failed output this receipt (BOM unit)
	Final        bool
	Overrides    map[int]float64 // run line no → consumed base qty this receipt
	ByProducts   []ByProductLine
	LabourHours  *float64
	Prices       map[string]Price
}

// ByProductLine is a by-product (base unit) at a value per base unit.
type ByProductLine struct {
	LineNo   int
	ItemID   string
	ItemName string
	BaseUnit string
	Qty      float64
	Rate     Price
}

// CalcLine is one costed line of a receipt.
type CalcLine struct {
	LineNo        int     `json:"line_no"`
	Kind          string  `json:"kind"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	BaseUnit      string  `json:"unit"`
	Qty           float64 `json:"qty"`
	UnitCostPaise int64   `json:"unit_cost_paise"`
	ExtendedPaise int64   `json:"extended_paise"`
	PriceSource   string  `json:"price_source,omitempty"`
}

// ReceiptCalc is the costed result of one completion.
type ReceiptCalc struct {
	Lines            []CalcLine `json:"lines"`
	MaterialPaise    int64      `json:"material_paise"`
	OperationsPaise  int64      `json:"operations_paise"`
	ByproductPaise   int64      `json:"byproduct_paise"`
	TotalPaise       int64      `json:"total_paise"`     // material + operations (gross)
	MainCostPaise    int64      `json:"main_cost_paise"` // total − by-products, carried by good output
	UnitCostPaise    int64      `json:"unit_cost_paise"` // per base unit of good output
	GoodBaseQty      float64    `json:"good_base_qty"`
	LabourHours      float64    `json:"labour_hours"`
	EffectivePortion float64    `json:"effective_portion"` // cumulative BOM-unit output the material is settled to
	Missing          []string   `json:"missing_prices,omitempty"`
}

// CalcReceipt costs one completion.
//
// Default consumption of a planned line settles material up to the cumulative
// portion made so far: planned × portion ÷ run qty − already consumed, where
// portion = good + rejected; a final completion settles at least the whole run
// (so shortfall and wastage raise the unit cost, as before). Overrides replace
// the default; unplanned lines consume only what is entered. Operations are
// charged the same way; LabourHours replaces the planned hours for the portion.
func CalcReceipt(in ReceiptInput) (ReceiptCalc, error) {
	if in.Good < 0 || in.Rejected < 0 {
		return ReceiptCalc{}, httpx.Validation("quantities cannot be negative")
	}
	cum := in.PriorPortion + in.Good + in.Rejected
	if in.Final && cum < in.RunQty {
		cum = in.RunQty
	}
	if !in.Final && in.Good+in.Rejected <= 0 {
		return ReceiptCalc{}, httpx.Validation("a partial completion needs good or rejected output")
	}
	res := ReceiptCalc{EffectivePortion: units.Round3(cum)}
	missing := map[string]bool{}
	var produce *Line
	for i := range in.Lines {
		l := in.Lines[i]
		switch l.Kind {
		case KindProduce:
			produce = &in.Lines[i]
			continue
		case KindByproduct:
			continue
		}
		q := 0.0
		if !l.Unplanned && in.RunQty > 0 {
			q = units.Round3(l.PlannedBaseQty*cum/in.RunQty - l.actual())
			if q < 0 {
				q = 0
			}
		}
		if o, ok := in.Overrides[l.LineNo]; ok {
			q = units.Round3(o)
		}
		if q <= 0 {
			continue
		}
		p, ok := in.Prices[l.ItemID]
		if !ok || p.Paise <= 0 {
			missing[l.ItemID] = true
		}
		ext := units.Extend(q, p.Paise)
		res.MaterialPaise += ext
		res.Lines = append(res.Lines, CalcLine{LineNo: l.LineNo, Kind: KindConsume, ItemID: l.ItemID, ItemName: l.ItemName,
			BaseUnit: l.BaseUnit, Qty: q, UnitCostPaise: p.Paise, ExtendedPaise: ext, PriceSource: p.Source})
	}
	for id := range missing {
		res.Missing = append(res.Missing, id)
	}
	sort.Strings(res.Missing)

	// operations: fixed cost pro-rata, labour by planned or actual hours
	fixedFor := func(q float64) int64 {
		var t int64
		for _, o := range in.BOM.Operations {
			t += units.MulDiv(o.CostPaise, units.Milli(q), units.Milli(in.BOM.OutputQty))
		}
		return t
	}
	labourFor := func(q float64) (int64, float64) {
		var t int64
		var h float64
		for _, o := range in.BOM.Operations {
			t += units.MulDiv(units.Extend(o.Hours, o.RatePaisePerHour), units.Milli(q), units.Milli(in.BOM.OutputQty))
			h += o.Hours * q / in.BOM.OutputQty
		}
		return t, h
	}
	ops := fixedFor(cum) - fixedFor(in.PriorPortion)
	lc, lh := labourFor(cum)
	pc, ph := labourFor(in.PriorPortion)
	labour, hours := lc-pc, lh-ph
	if in.LabourHours != nil {
		batchLabour, batchHours := labourFor(in.BOM.OutputQty)
		hours = *in.LabourHours
		labour = 0
		if batchHours > 0 {
			labour = units.MulDiv(batchLabour, units.Milli(hours), units.Milli(batchHours)) // at the routing's average rate
		}
	}
	if ops < 0 {
		ops = 0
	}
	if labour < 0 {
		labour = 0
	}
	res.OperationsPaise = ops + labour
	res.LabourHours = units.Round3(hours)

	var byLines []CalcLine
	for _, bp := range in.ByProducts {
		if bp.Qty <= 0 {
			continue
		}
		if bp.Rate.Paise <= 0 {
			return ReceiptCalc{}, httpx.Validation("by-product %s needs a value (unit_cost_paise)", bp.ItemName)
		}
		ext := units.Extend(bp.Qty, bp.Rate.Paise)
		res.ByproductPaise += ext
		byLines = append(byLines, CalcLine{LineNo: bp.LineNo, Kind: KindByproduct, ItemID: bp.ItemID, ItemName: bp.ItemName,
			BaseUnit: bp.BaseUnit, Qty: units.Round3(bp.Qty), UnitCostPaise: bp.Rate.Paise, ExtendedPaise: ext, PriceSource: bp.Rate.Source})
	}
	res.TotalPaise = res.MaterialPaise + res.OperationsPaise
	res.MainCostPaise = res.TotalPaise - res.ByproductPaise
	if res.MainCostPaise < 0 {
		return ReceiptCalc{}, httpx.Validation("by-products (%d paise) are worth more than the run's cost (%d paise)", res.ByproductPaise, res.TotalPaise)
	}
	if produce != nil && in.Good > 0 {
		res.GoodBaseQty = units.Round3(in.Good * produce.Factor)
		res.UnitCostPaise = units.Rate(res.MainCostPaise, res.GoodBaseQty)
		res.Lines = append(res.Lines, CalcLine{LineNo: produce.LineNo, Kind: KindProduce, ItemID: produce.ItemID,
			ItemName: produce.ItemName, BaseUnit: produce.BaseUnit, Qty: res.GoodBaseQty, UnitCostPaise: res.UnitCostPaise,
			ExtendedPaise: res.MainCostPaise})
	}
	res.Lines = append(res.Lines, byLines...)
	return res, nil
}

// Yield is good ÷ (good + rejected) × 100, and good ÷ planned × 100.
func Yield(good, rejected, planned float64) (actual, vsPlan *float64) {
	if t := good + rejected; t > 0 {
		v := units.Round3(good / t * 100)
		actual = &v
	}
	if planned > 0 && good+rejected > 0 {
		v := units.Round3(good / planned * 100)
		vsPlan = &v
	}
	return
}

var ist = bom.IST

func today(now time.Time) string { return bom.Today(now) }
