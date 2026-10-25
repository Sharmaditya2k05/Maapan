// Package bom holds Bills of Materials: multi-level explosion with cycle
// detection and cost roll-up (pure functions), plus storage and HTTP.
package bom

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maapan/mool/httpx"
)

// BOM statuses.
const (
	StatusDraft    = "draft"
	StatusActive   = "active"
	StatusArchived = "archived"
)

// BOM is a recipe: OutputQty of ItemID is made from Components (+ Operations cost).
type BOM struct {
	ID         string      `json:"id"`
	ItemID     string      `json:"item_id"`
	ItemName   string      `json:"item_name"`
	Version    int         `json:"version"`
	OutputQty  float64     `json:"output_qty"`
	Unit       string      `json:"unit"`
	Status     string      `json:"status"`
	Notes      *string     `json:"notes,omitempty"`
	CreatedBy  string      `json:"created_by"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Components []Component `json:"components"`
	Operations []Operation `json:"operations"`
}

// Component is one input of a BOM. Qty is per OutputQty of the parent.
type Component struct {
	LineNo        int     `json:"line_no"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Qty           float64 `json:"qty"`
	Unit          string  `json:"unit"`
	ScrapPercent  float64 `json:"scrap_percent"`
	IsSubAssembly bool    `json:"is_sub_assembly"`
	// SubBOMID pins a specific BOM; empty means "the component item's active BOM".
	SubBOMID *string `json:"sub_bom_id,omitempty"`
}

// Operation is a labour/overhead cost per batch of OutputQty.
type Operation struct {
	LineNo    int    `json:"line_no"`
	Name      string `json:"name"`
	CostPaise int64  `json:"cost_paise"`
}

// Input is the body for creating/updating a BOM.
type Input struct {
	ItemID     string      `json:"item_id"`
	ItemName   string      `json:"item_name"`
	OutputQty  float64     `json:"output_qty"`
	Unit       string      `json:"unit,omitempty"`
	Notes      string      `json:"notes,omitempty"`
	Components []Component `json:"components"`
	Operations []Operation `json:"operations,omitempty"`
}

// Validate checks the shape of a BOM.
func (in *Input) Validate() error {
	in.ItemName = strings.TrimSpace(in.ItemName)
	if in.ItemID == "" || in.ItemName == "" {
		return httpx.Validation("item_id and item_name are required")
	}
	if in.OutputQty <= 0 {
		return httpx.Validation("output_qty must be positive")
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
		if strings.TrimSpace(o.Name) == "" || o.CostPaise < 0 {
			return httpx.Validation("operation %d needs a name and non-negative cost", i+1)
		}
	}
	return nil
}

// Resolver loads BOMs for explosion/costing.
type Resolver interface {
	// BOM returns a BOM by id (error if missing).
	BOM(id string) (*BOM, error)
	// ActiveBOMFor returns the active BOM of an item, or nil when it has none.
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

// grossQty is the component quantity needed for parentQty of the parent, including scrap.
func grossQty(c Component, parent *BOM, parentQty float64) float64 {
	return c.Qty / parent.OutputQty * parentQty * (1 + c.ScrapPercent/100)
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

// Requirement is a flattened raw-material need.
type Requirement struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name"`
	Unit     string  `json:"unit"`
	Qty      float64 `json:"qty"`
}

// Explode expands root for qty units of output into an indented tree and the
// flattened raw-material requirement (leaves only), detecting cycles.
func Explode(r Resolver, root *BOM, qty float64) (Node, []Requirement, error) {
	if qty <= 0 {
		return Node{}, nil, httpx.Validation("qty must be positive")
	}
	totals := map[string]*Requirement{}
	var walk func(b *BOM, q float64, level int, path []string) ([]Node, error)
	walk = func(b *BOM, q float64, level int, path []string) ([]Node, error) {
		for _, id := range path {
			if id == b.ID {
				return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(append(path, b.ID), " → "))
			}
		}
		path = append(path, b.ID)
		var nodes []Node
		for _, c := range b.Components {
			need := grossQty(c, b, q)
			n := Node{ItemID: c.ItemID, ItemName: c.ItemName, Qty: Round3(need), Unit: c.Unit, Level: level, IsSubAssembly: c.IsSubAssembly}
			sb, err := subBOM(r, c)
			if err != nil {
				return nil, err
			}
			if sb != nil {
				n.BOMID = sb.ID
				if n.Children, err = walk(sb, need, level+1, path); err != nil {
					return nil, err
				}
			} else {
				t := totals[c.ItemID]
				if t == nil {
					t = &Requirement{ItemID: c.ItemID, ItemName: c.ItemName, Unit: c.Unit}
					totals[c.ItemID] = t
				}
				t.Qty += need
			}
			nodes = append(nodes, n)
		}
		return nodes, nil
	}
	children, err := walk(root, qty, 1, nil)
	if err != nil {
		return Node{}, nil, err
	}
	tree := Node{ItemID: root.ItemID, ItemName: root.ItemName, Qty: Round3(qty), Unit: root.Unit, Level: 0, BOMID: root.ID, Children: children}
	flat := make([]Requirement, 0, len(totals))
	for _, t := range totals {
		t.Qty = Round3(t.Qty)
		flat = append(flat, *t)
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].ItemName < flat[j].ItemName })
	return tree, flat, nil
}

// CostLine is one direct component in a cost roll-up.
type CostLine struct {
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Qty           float64 `json:"qty"` // for the requested quantity, incl. scrap
	UnitCostPaise int64   `json:"unit_cost_paise"`
	ExtendedPaise int64   `json:"extended_paise"`
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
	Lines           []CostLine `json:"lines"`
	MissingPrices   []string   `json:"missing_prices,omitempty"` // leaf items costed at 0
}

// Cost rolls up material (leaf prices, sub-assemblies recursively) plus
// operations, for qty units of root. prices are paise per unit of raw material.
func Cost(r Resolver, root *BOM, qty float64, prices map[string]int64) (CostResult, error) {
	if qty <= 0 {
		return CostResult{}, httpx.Validation("qty must be positive")
	}
	missing := map[string]bool{}
	var unitCost func(b *BOM, path []string) (float64, error)
	unitCost = func(b *BOM, path []string) (float64, error) {
		for _, id := range path {
			if id == b.ID {
				return 0, fmt.Errorf("%w: %s", ErrCycle, strings.Join(append(path, b.ID), " → "))
			}
		}
		path = append(path, b.ID)
		total := 0.0
		for _, c := range b.Components {
			cu, err := componentUnitCost(r, c, prices, missing, func(sb *BOM) (float64, error) { return unitCost(sb, path) })
			if err != nil {
				return 0, err
			}
			total += grossQty(c, b, b.OutputQty) * cu
		}
		for _, o := range b.Operations {
			total += float64(o.CostPaise)
		}
		return total / b.OutputQty, nil
	}

	res := CostResult{BOMID: root.ID, Qty: Round3(qty)}
	var material float64
	for _, c := range root.Components {
		cu, err := componentUnitCost(r, c, prices, missing, func(sb *BOM) (float64, error) { return unitCost(sb, []string{root.ID}) })
		if err != nil {
			return CostResult{}, err
		}
		need := grossQty(c, root, qty)
		ext := need * cu
		material += ext
		res.Lines = append(res.Lines, CostLine{
			ItemID: c.ItemID, ItemName: c.ItemName, Qty: Round3(need), UnitCostPaise: int64(math.Round(cu)),
			ExtendedPaise: int64(math.Round(ext)), IsSubAssembly: c.IsSubAssembly,
		})
	}
	var ops float64
	for _, o := range root.Operations {
		ops += float64(o.CostPaise) * qty / root.OutputQty
	}
	res.MaterialPaise = int64(math.Round(material))
	res.OperationsPaise = int64(math.Round(ops))
	res.TotalPaise = res.MaterialPaise + res.OperationsPaise
	res.UnitCostPaise = int64(math.Round((material + ops) / qty))
	for id := range missing {
		res.MissingPrices = append(res.MissingPrices, id)
	}
	sort.Strings(res.MissingPrices)
	return res, nil
}

func componentUnitCost(r Resolver, c Component, prices map[string]int64, missing map[string]bool,
	sub func(*BOM) (float64, error)) (float64, error) {
	sb, err := subBOM(r, c)
	if err != nil {
		return 0, err
	}
	if sb != nil {
		return sub(sb)
	}
	p, ok := prices[c.ItemID]
	if !ok || p == 0 {
		missing[c.ItemID] = true
	}
	return float64(p), nil
}

// Round3 rounds a quantity to 3 decimals.
func Round3(q float64) float64 { return math.Round(q*1000) / 1000 }
