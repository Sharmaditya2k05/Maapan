// Package bom holds Bills of Materials: multi-level explosion with cycle
// detection and cost roll-up (pure functions), plus storage and HTTP.
//
// Quantities are float64 kept to 3 decimals; money is always int64 paise,
// computed with exact integer arithmetic (units.MulDiv), never float.
package bom

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/units"
)

// BOM statuses.
const (
	StatusDraft    = "draft"
	StatusActive   = "active"
	StatusArchived = "archived"
)

// Scrap methods. Divide is the market convention (Tally, SAP: a 2% scrap means
// 2% of what is issued is lost, so issue = net ÷ 0.98). Multiply is the old
// nirmaan behaviour (net × 1.02), kept for BOMs created before the switch.
const (
	ScrapDivide   = "divide"
	ScrapMultiply = "multiply"
)

// BOM is a recipe: OutputQty of ItemID is made from Components (+ Operations cost).
type BOM struct {
	ID            string      `json:"id"`
	ItemID        string      `json:"item_id"`
	ItemName      string      `json:"item_name"`
	Version       int         `json:"version"`
	OutputQty     float64     `json:"output_qty"`
	Unit          string      `json:"unit"`
	Status        string      `json:"status"`
	ScrapMethod   string      `json:"scrap_method"`
	YieldPercent  float64     `json:"yield_percent"` // expected good output ÷ input; materials are grossed up by it
	EffectiveFrom *string     `json:"effective_from,omitempty"`
	EffectiveTo   *string     `json:"effective_to,omitempty"`
	IsPrimary     bool        `json:"is_primary"`               // false = an alternate recipe/route
	AlternateName *string     `json:"alternate_name,omitempty"` // e.g. "Machine B", "Imported resin"
	Notes         *string     `json:"notes,omitempty"`
	CreatedBy     string      `json:"created_by"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Components    []Component `json:"components"`
	Operations    []Operation `json:"operations"`
}

// Component is one input of a BOM. Qty is per OutputQty of the parent, in Unit
// (converted to the item's stock unit through bhandar's alternate units).
type Component struct {
	LineNo        int     `json:"line_no"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Qty           float64 `json:"qty"`
	Unit          string  `json:"unit"`
	ScrapPercent  float64 `json:"scrap_percent"`
	IsSubAssembly bool    `json:"is_sub_assembly"`
	// SubBOMID pins a specific BOM; empty means "the component item's effective primary BOM".
	SubBOMID *string `json:"sub_bom_id,omitempty"`
}

// Operation is a routing step per batch of OutputQty: a fixed cost plus
// labour/machine hours at an hourly rate.
type Operation struct {
	LineNo           int     `json:"line_no"`
	Name             string  `json:"name"`
	WorkCenter       string  `json:"work_center,omitempty"`
	Hours            float64 `json:"hours"`
	RatePaisePerHour int64   `json:"rate_paise_per_hour"`
	CostPaise        int64   `json:"cost_paise"` // fixed cost per batch
}

// BatchCostPaise is the operation's cost for one batch.
func (o Operation) BatchCostPaise() int64 {
	return o.CostPaise + units.Extend(o.Hours, o.RatePaisePerHour)
}

// Input is the body for creating/updating a BOM.
type Input struct {
	ItemID        string      `json:"item_id"`
	ItemName      string      `json:"item_name"`
	OutputQty     float64     `json:"output_qty"`
	Unit          string      `json:"unit,omitempty"`
	ScrapMethod   string      `json:"scrap_method,omitempty"`  // divide (default) | multiply
	YieldPercent  float64     `json:"yield_percent,omitempty"` // default 100
	EffectiveFrom string      `json:"effective_from,omitempty"`
	EffectiveTo   string      `json:"effective_to,omitempty"`
	IsPrimary     *bool       `json:"is_primary,omitempty"` // default true
	AlternateName string      `json:"alternate_name,omitempty"`
	Notes         string      `json:"notes,omitempty"`
	Components    []Component `json:"components"`
	Operations    []Operation `json:"operations,omitempty"`
}

// Validate checks the shape of a BOM and fills defaults.
func (in *Input) Validate() error {
	in.ItemName = strings.TrimSpace(in.ItemName)
	if in.ItemID == "" || in.ItemName == "" {
		return httpx.Validation("item_id and item_name are required")
	}
	if in.OutputQty <= 0 {
		return httpx.Validation("output_qty must be positive")
	}
	switch in.ScrapMethod {
	case "":
		in.ScrapMethod = ScrapDivide
	case ScrapDivide, ScrapMultiply:
	default:
		return httpx.Validation("scrap_method must be divide or multiply")
	}
	if in.YieldPercent == 0 {
		in.YieldPercent = 100
	}
	if in.YieldPercent <= 0 || in.YieldPercent > 100 {
		return httpx.Validation("yield_percent must be above 0 and at most 100")
	}
	for _, d := range []string{in.EffectiveFrom, in.EffectiveTo} {
		if d != "" {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return httpx.Validation("effective dates must be YYYY-MM-DD")
			}
		}
	}
	if in.EffectiveFrom != "" && in.EffectiveTo != "" && in.EffectiveTo < in.EffectiveFrom {
		return httpx.Validation("effective_to is before effective_from")
	}
	if in.IsPrimary == nil {
		t := true
		in.IsPrimary = &t
	}
	if !*in.IsPrimary && strings.TrimSpace(in.AlternateName) == "" {
		return httpx.Validation("an alternate BOM needs alternate_name")
	}
	if len(in.Components) == 0 || len(in.Components) > 300 {
		return httpx.Validation("a BOM needs 1 to 300 components")
	}
	seen := map[string]bool{}
	for i, c := range in.Components {
		switch {
		case c.ItemID == "" || strings.TrimSpace(c.ItemName) == "":
			return httpx.Validation("component %d needs item_id and item_name", i+1)
		case c.ItemID == in.ItemID:
			return httpx.Validation("component %d is the finished item itself", i+1)
		case c.Qty <= 0:
			return httpx.Validation("component %d qty must be positive", i+1)
		case c.ScrapPercent < 0 || c.ScrapPercent >= 100:
			return httpx.Validation("component %d scrap_percent must be 0 to <100", i+1)
		case seen[c.ItemID]:
			return httpx.Validation("component %d repeats an item; combine the quantities", i+1)
		}
		seen[c.ItemID] = true
	}
	for i, o := range in.Operations {
		if strings.TrimSpace(o.Name) == "" || o.CostPaise < 0 || o.Hours < 0 || o.RatePaisePerHour < 0 {
			return httpx.Validation("operation %d needs a name and non-negative cost, hours and rate", i+1)
		}
	}
	return nil
}

// Resolver loads BOMs for explosion/costing.
type Resolver interface {
	// BOM returns a BOM by id (error if missing).
	BOM(id string) (*BOM, error)
	// ActiveBOMFor returns the effective primary BOM of an item, or nil when it has none.
	ActiveBOMFor(itemID string) (*BOM, error)
}

// ErrCycle reports a BOM that (indirectly) contains itself.
var ErrCycle = errors.New("bom cycle")

func subBOM(r Resolver, c Component) (*BOM, error) {
	if !c.IsSubAssembly {
		return nil, nil
	}
	if c.SubBOMID != nil && *c.SubBOMID != "" {
		return r.BOM(*c.SubBOMID)
	}
	b, err := r.ActiveBOMFor(c.ItemID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, httpx.Validation("sub-assembly %s has no active BOM", c.ItemName)
	}
	return b, nil
}

// ApplyScrap grosses a net quantity up for scrap% using method.
func ApplyScrap(net, pct float64, method string) float64 {
	if pct <= 0 {
		return net
	}
	if method == ScrapMultiply {
		return net * (1 + pct/100)
	}
	return net / (1 - pct/100)
}

// GrossQty is the component quantity (in the component's unit) needed for
// parentQty of the parent, including the parent's yield and the component's scrap.
func GrossQty(c Component, parent *BOM, parentQty float64) float64 {
	q := c.Qty / parent.OutputQty * parentQty
	if y := parent.YieldPercent; y > 0 && y < 100 {
		q = q * 100 / y
	}
	return ApplyScrap(q, c.ScrapPercent, parent.ScrapMethod)
}

// Node is one line of the indented explosion tree.
type Node struct {
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Qty           float64 `json:"qty"`
	Unit          string  `json:"unit"`
	Level         int     `json:"level"`
	IsSubAssembly bool    `json:"is_sub_assembly"`
	BOMID         string  `json:"bom_id,omitempty"`
	Children      []Node  `json:"children,omitempty"`
}

// Requirement is a flattened raw-material need, in the item's base unit when known.
type Requirement struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name"`
	Unit     string  `json:"unit"`
	Qty      float64 `json:"qty"`
}

// Explode expands root for qty units of output into an indented tree and the
// flattened raw-material requirement (leaves only), detecting cycles. cat
// converts component units to stock units (nil: quantities stay as entered).
func Explode(r Resolver, root *BOM, qty float64, cat units.Catalog) (Node, []Requirement, error) {
	if qty <= 0 {
		return Node{}, nil, httpx.Validation("qty must be positive")
	}
	totals := map[string]*Requirement{}
	var walk func(b *BOM, q float64, level int, path []string) ([]Node, error)
	walk = func(b *BOM, q float64, level int, path []string) ([]Node, error) {
		if err := checkCycle(path, b.ID); err != nil {
			return nil, err
		}
		path = append(path, b.ID)
		var nodes []Node
		for _, c := range b.Components {
			need := GrossQty(c, b, q)
			n := Node{ItemID: c.ItemID, ItemName: c.ItemName, Qty: units.Round3(need), Unit: c.Unit, Level: level, IsSubAssembly: c.IsSubAssembly}
			sb, err := subBOM(r, c)
			if err != nil {
				return nil, err
			}
			if sb != nil {
				n.BOMID = sb.ID
				// the sub-BOM is expressed in its own output unit
				subQty, err := convertBetween(cat, c.ItemID, need, c.Unit, sb.Unit)
				if err != nil {
					return nil, err
				}
				if n.Children, err = walk(sb, subQty, level+1, path); err != nil {
					return nil, err
				}
			} else {
				f, base, err := cat.FactorFor(c.ItemID, c.Unit)
				if err != nil {
					return nil, httpx.Validation("%s: %s", c.ItemName, err.Error())
				}
				t := totals[c.ItemID]
				if t == nil {
					t = &Requirement{ItemID: c.ItemID, ItemName: c.ItemName, Unit: base}
					totals[c.ItemID] = t
				} else if t.Unit != base {
					return nil, httpx.Validation("%s is used in %s and %s; set up the unit conversion in bhandar", c.ItemName, t.Unit, base)
				}
				t.Qty += need * f
			}
			nodes = append(nodes, n)
		}
		return nodes, nil
	}
	children, err := walk(root, qty, 1, nil)
	if err != nil {
		return Node{}, nil, err
	}
	tree := Node{ItemID: root.ItemID, ItemName: root.ItemName, Qty: units.Round3(qty), Unit: root.Unit, Level: 0, BOMID: root.ID, Children: children}
	flat := make([]Requirement, 0, len(totals))
	for _, t := range totals {
		t.Qty = units.Round3(t.Qty)
		flat = append(flat, *t)
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].ItemName < flat[j].ItemName })
	return tree, flat, nil
}

func checkCycle(path []string, id string) error {
	for _, p := range path {
		if p == id {
			return fmt.Errorf("%w: %s", ErrCycle, strings.Join(append(path, id), " → "))
		}
	}
	return nil
}

// convertBetween converts qty of item from unit `from` to unit `to` via its base unit.
func convertBetween(cat units.Catalog, itemID string, qty float64, from, to string) (float64, error) {
	if units.Norm(from) == units.Norm(to) {
		return qty, nil
	}
	ff, _, err := cat.FactorFor(itemID, from)
	if err != nil {
		return 0, httpx.Validation("%s", err.Error())
	}
	ft, _, err := cat.FactorFor(itemID, to)
	if err != nil {
		return 0, httpx.Validation("%s", err.Error())
	}
	if ft == 0 {
		return qty, nil
	}
	return qty * ff / ft, nil
}

// CostLine is one direct component in a cost roll-up.
type CostLine struct {
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Qty           float64 `json:"qty"` // for the requested quantity, incl. yield and scrap, in Unit
	Unit          string  `json:"unit"`
	BaseQty       float64 `json:"base_qty"` // the same in the item's stock unit
	BaseUnit      string  `json:"base_unit"`
	UnitCostPaise int64   `json:"unit_cost_paise"` // per base unit
	ExtendedPaise int64   `json:"extended_paise"`
	PriceSource   string  `json:"price_source,omitempty"`
	IsSubAssembly bool    `json:"is_sub_assembly"`
}

// CostResult is the rolled-up cost of making Qty units.
type CostResult struct {
	BOMID           string     `json:"bom_id"`
	Qty             float64    `json:"qty"`
	MaterialPaise   int64      `json:"material_paise"`
	OperationsPaise int64      `json:"operations_paise"`
	TotalPaise      int64      `json:"total_paise"`
	UnitCostPaise   int64      `json:"unit_cost_paise"`
	LabourHours     float64    `json:"labour_hours"`
	Lines           []CostLine `json:"lines"`
	// MissingPrices are leaf items with no cost: the roll-up is incomplete
	// (Complete is false) and production completion refuses to book at ₹0.
	MissingPrices []string `json:"missing_prices,omitempty"`
	Complete      bool     `json:"complete"`
}

// Pricing is what Cost needs: unit conversions and a price per base unit.
type Pricing struct {
	Units   units.Catalog
	Prices  map[string]int64  // paise per base unit
	Sources map[string]string // optional: where each price came from
}

// Cost rolls up material (leaf prices, sub-assemblies recursively) plus
// operations, for qty units of root.
func Cost(r Resolver, root *BOM, qty float64, pr Pricing) (CostResult, error) {
	if qty <= 0 {
		return CostResult{}, httpx.Validation("qty must be positive")
	}
	missing := map[string]bool{}
	// rate returns the cost per base unit of b's output item.
	var rate func(b *BOM, path []string) (int64, error)
	componentRate := func(c Component, path []string) (int64, string, error) {
		sb, err := subBOM(r, c)
		if err != nil {
			return 0, "", err
		}
		if sb != nil {
			v, err := rate(sb, path)
			return v, "rollup", err
		}
		p, ok := pr.Prices[c.ItemID]
		if !ok || p <= 0 {
			missing[c.ItemID] = true
			return 0, "", nil
		}
		return p, pr.Sources[c.ItemID], nil
	}
	rate = func(b *BOM, path []string) (int64, error) {
		if err := checkCycle(path, b.ID); err != nil {
			return 0, err
		}
		path = append(path, b.ID)
		var total int64
		for _, c := range b.Components {
			cr, _, err := componentRate(c, path)
			if err != nil {
				return 0, err
			}
			baseQty, _, err := baseOf(pr.Units, c, GrossQty(c, b, b.OutputQty))
			if err != nil {
				return 0, err
			}
			total += units.Extend(baseQty, cr)
		}
		for _, o := range b.Operations {
			total += o.BatchCostPaise()
		}
		f, _, err := pr.Units.FactorFor(b.ItemID, b.Unit)
		if err != nil {
			return 0, httpx.Validation("%s: %s", b.ItemName, err.Error())
		}
		return units.Rate(total, b.OutputQty*f), nil
	}

	res := CostResult{BOMID: root.ID, Qty: units.Round3(qty), Lines: []CostLine{}}
	for _, c := range root.Components {
		cr, src, err := componentRate(c, []string{root.ID})
		if err != nil {
			return CostResult{}, err
		}
		need := GrossQty(c, root, qty)
		baseQty, baseUnit, err := baseOf(pr.Units, c, need)
		if err != nil {
			return CostResult{}, err
		}
		ext := units.Extend(baseQty, cr)
		res.MaterialPaise += ext
		res.Lines = append(res.Lines, CostLine{
			ItemID: c.ItemID, ItemName: c.ItemName, Qty: units.Round3(need), Unit: c.Unit,
			BaseQty: units.Round3(baseQty), BaseUnit: baseUnit, UnitCostPaise: cr, ExtendedPaise: ext,
			PriceSource: src, IsSubAssembly: c.IsSubAssembly,
		})
	}
	res.OperationsPaise, res.LabourHours = OperationsFor(root, qty)
	res.TotalPaise = res.MaterialPaise + res.OperationsPaise
	res.UnitCostPaise = units.Rate(res.TotalPaise, qty)
	for id := range missing {
		res.MissingPrices = append(res.MissingPrices, id)
	}
	sort.Strings(res.MissingPrices)
	res.Complete = len(res.MissingPrices) == 0
	return res, nil
}

// OperationsFor is the operations cost (paise) and hours for qty output of b.
func OperationsFor(b *BOM, qty float64) (int64, float64) {
	var paise int64
	var hours float64
	for _, o := range b.Operations {
		paise += units.MulDiv(o.BatchCostPaise(), units.Milli(qty), units.Milli(b.OutputQty))
		hours += o.Hours * qty / b.OutputQty
	}
	return paise, units.Round3(hours)
}

func baseOf(cat units.Catalog, c Component, qty float64) (float64, string, error) {
	f, base, err := cat.FactorFor(c.ItemID, c.Unit)
	if err != nil {
		return 0, "", httpx.Validation("%s: %s", c.ItemName, err.Error())
	}
	return qty * f, base, nil
}

// Round3 rounds a quantity to 3 decimals.
func Round3(q float64) float64 { return units.Round3(q) }

// EffectiveOn reports whether b's date range covers date (YYYY-MM-DD).
func (b *BOM) EffectiveOn(date string) bool {
	if b.EffectiveFrom != nil && *b.EffectiveFrom != "" && date < *b.EffectiveFrom {
		return false
	}
	if b.EffectiveTo != nil && *b.EffectiveTo != "" && date > *b.EffectiveTo {
		return false
	}
	return true
}

// UnitIssues lists components (and the output) whose unit cannot be converted
// to the item's stock unit.
func UnitIssues(b *BOM, cat units.Catalog) []string {
	var out []string
	if _, _, err := cat.FactorFor(b.ItemID, b.Unit); err != nil {
		out = append(out, b.ItemName+": "+err.Error())
	}
	for _, c := range b.Components {
		if _, _, err := cat.FactorFor(c.ItemID, c.Unit); err != nil {
			out = append(out, c.ItemName+": "+err.Error())
		}
	}
	return out
}
