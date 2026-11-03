package bom

import (
	"errors"
	"testing"

	"github.com/maapan/nirmaan/internal/units"
)

type memResolver struct {
	byID     map[string]*BOM
	activeOf map[string]string
}

func (m memResolver) BOM(id string) (*BOM, error) {
	if b, ok := m.byID[id]; ok {
		return b, nil
	}
	return nil, errors.New("not found " + id)
}

func (m memResolver) ActiveBOMFor(itemID string) (*BOM, error) {
	if id, ok := m.activeOf[itemID]; ok {
		return m.byID[id], nil
	}
	return nil, nil
}

// Chair (1) = 4 legs (sub-assembly, 2% scrap) + 1 seat board + 6 screws, ₹100 assembly
// Leg (10) = 5 kg wood (10% scrap) + 20 screws, ₹50 turning
func fixture(method string) (memResolver, *BOM) {
	leg := &BOM{ID: "bom-leg", ItemID: "leg", ItemName: "Leg", OutputQty: 10, Unit: "PCS", ScrapMethod: method, Components: []Component{
		{ItemID: "wood", ItemName: "Wood", Qty: 5, Unit: "KG", ScrapPercent: 10},
		{ItemID: "screw", ItemName: "Screw", Qty: 20, Unit: "PCS"},
	}, Operations: []Operation{{Name: "Turning", CostPaise: 5000}}}
	chair := &BOM{ID: "bom-chair", ItemID: "chair", ItemName: "Chair", OutputQty: 1, Unit: "PCS", ScrapMethod: method, Components: []Component{
		{ItemID: "leg", ItemName: "Leg", Qty: 4, Unit: "PCS", ScrapPercent: 2, IsSubAssembly: true},
		{ItemID: "seat", ItemName: "Seat board", Qty: 1, Unit: "PCS"},
		{ItemID: "screw", ItemName: "Screw", Qty: 6, Unit: "PCS"},
	}, Operations: []Operation{{Name: "Assembly", CostPaise: 10000}}}
	r := memResolver{
		byID:     map[string]*BOM{leg.ID: leg, chair.ID: chair},
		activeOf: map[string]string{"leg": leg.ID, "chair": chair.ID},
	}
	return r, chair
}

func TestScrapMethods(t *testing.T) {
	if got := units.Round3(ApplyScrap(5, 2, ScrapDivide)); got != 5.102 {
		t.Errorf("divide: 5 / 0.98 = %v", got)
	}
	if got := units.Round3(ApplyScrap(5, 2, ScrapMultiply)); got != 5.1 {
		t.Errorf("multiply: 5 × 1.02 = %v", got)
	}
	if got := ApplyScrap(5, 2, ""); units.Round3(got) != 5.102 {
		t.Errorf("default must be divide, got %v", got)
	}
	// yield 80%: 10 good need inputs for 12.5
	b := &BOM{OutputQty: 10, YieldPercent: 80}
	if got := GrossQty(Component{Qty: 4}, b, 10); got != 5 {
		t.Errorf("yield gross-up = %v", got)
	}
}

func TestExplodeMultiLevelWithScrap(t *testing.T) {
	r, chair := fixture(ScrapMultiply) // the old formula keeps its numbers
	tree, flat, err := Explode(r, chair, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 10 chairs → 40 legs × 1.02 = 40.8 legs; wood 5/10 × 40.8 × 1.1 = 22.44; screws 81.6 + 60
	want := map[string]float64{"wood": 22.44, "screw": 141.6, "seat": 10}
	if len(flat) != len(want) {
		t.Fatalf("flat = %+v", flat)
	}
	for _, rq := range flat {
		if rq.Qty != want[rq.ItemID] {
			t.Errorf("%s = %v want %v", rq.ItemID, rq.Qty, want[rq.ItemID])
		}
	}
	if tree.Children[0].Qty != 40.8 || len(tree.Children[0].Children) != 2 || tree.Children[0].Children[0].Level != 2 {
		t.Errorf("tree = %+v", tree.Children[0])
	}

	r, chair = fixture(ScrapDivide)
	_, flat, err = Explode(r, chair, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	// legs 40 / 0.98 = 40.8163…; wood 0.5 × 40.8163 / 0.9 = 22.676
	for _, rq := range flat {
		if rq.ItemID == "wood" && rq.Qty != 22.676 {
			t.Errorf("divide wood = %v", rq.Qty)
		}
	}
}

func TestExplodeDetectsCycle(t *testing.T) {
	a := &BOM{ID: "A", ItemID: "a", OutputQty: 1, Components: []Component{{ItemID: "b", ItemName: "B", Qty: 1, IsSubAssembly: true}}}
	b := &BOM{ID: "B", ItemID: "b", OutputQty: 1, Components: []Component{{ItemID: "c", ItemName: "C", Qty: 1, IsSubAssembly: true}}}
	c := &BOM{ID: "C", ItemID: "c", OutputQty: 1, Components: []Component{{ItemID: "a", ItemName: "A", Qty: 1, IsSubAssembly: true}}}
	r := memResolver{byID: map[string]*BOM{"A": a, "B": b, "C": c}, activeOf: map[string]string{"a": "A", "b": "B", "c": "C"}}
	if _, _, err := Explode(r, a, 1, nil); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
	if _, err := Cost(r, a, 1, Pricing{}); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected cycle in cost, got %v", err)
	}
}

func TestExplodeMissingSubBOM(t *testing.T) {
	r, chair := fixture(ScrapDivide)
	delete(r.activeOf, "leg")
	if _, _, err := Explode(r, chair, 1, nil); err == nil {
		t.Fatal("expected error for sub-assembly without active BOM")
	}
}

func TestCostRollUp(t *testing.T) {
	r, chair := fixture(ScrapMultiply)
	prices := map[string]int64{"wood": 8000, "screw": 200, "seat": 30000} // ₹80/kg, ₹2/screw, ₹300/seat
	res, err := Cost(r, chair, 2, Pricing{Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	// Leg unit cost = (5×1.1×8000 + 20×200 + 5000) / 10 = 5300
	// 2 chairs: legs 8.16 × 5300 = 43248, seats 60000, screws 2400 → 105648; ops 20000
	if res.Lines[0].UnitCostPaise != 5300 {
		t.Errorf("leg unit cost %d", res.Lines[0].UnitCostPaise)
	}
	if res.MaterialPaise != 105648 || res.OperationsPaise != 20000 || res.TotalPaise != 125648 || res.UnitCostPaise != 62824 || !res.Complete {
		t.Errorf("cost = %+v", res)
	}
	delete(prices, "seat")
	res, _ = Cost(r, chair, 1, Pricing{Prices: prices})
	if len(res.MissingPrices) != 1 || res.MissingPrices[0] != "seat" || res.Complete {
		t.Errorf("missing = %v complete=%v", res.MissingPrices, res.Complete)
	}
}

func TestCostConvertsUnitsAndRouting(t *testing.T) {
	// 10 loaves: 5 kg flour + 100 GM yeast (stocked in KG at ₹400/kg); baking 1.5 h at ₹200/h + ₹10 fixed
	b := &BOM{ID: "bread", ItemID: "bread", ItemName: "Bread", OutputQty: 10, Unit: "PCS", Components: []Component{
		{ItemID: "flour", ItemName: "Flour", Qty: 5, Unit: "KG"},
		{ItemID: "yeast", ItemName: "Yeast", Qty: 100, Unit: "GM"},
	}, Operations: []Operation{{Name: "Baking", Hours: 1.5, RatePaisePerHour: 20000, CostPaise: 1000}}}
	cat := units.Catalog{"flour": {Base: "KG"}, "yeast": {Base: "KG"}, "bread": {Base: "PCS"}}
	res, err := Cost(memResolver{}, b, 10, Pricing{Units: cat, Prices: map[string]int64{"flour": 4000, "yeast": 40000}})
	if err != nil {
		t.Fatal(err)
	}
	// flour 5 × 4000 = 20000; yeast 0.1 kg × 40000 = 4000; ops 1000 + 1.5 × 20000 = 31000
	if res.Lines[1].BaseQty != 0.1 || res.Lines[1].BaseUnit != "KG" || res.Lines[1].ExtendedPaise != 4000 {
		t.Errorf("yeast line = %+v", res.Lines[1])
	}
	if res.MaterialPaise != 24000 || res.OperationsPaise != 31000 || res.LabourHours != 1.5 || res.UnitCostPaise != 5500 {
		t.Errorf("cost = %+v", res)
	}
	// an unconvertible unit is an error, never a silent 1:1
	cat["yeast"] = units.ItemUnits{Base: "PCS"}
	if _, err := Cost(memResolver{}, b, 10, Pricing{Units: cat, Prices: map[string]int64{"flour": 4000, "yeast": 40000}}); err == nil {
		t.Error("GM → PCS accepted")
	}
	if issues := UnitIssues(b, cat); len(issues) != 1 {
		t.Errorf("unit issues = %v", issues)
	}
}

func TestInputValidate(t *testing.T) {
	in := Input{ItemID: "x", ItemName: "X", OutputQty: 1, Components: []Component{{ItemID: "x", ItemName: "X", Qty: 1}}}
	if in.Validate() == nil {
		t.Error("self-reference accepted")
	}
	in.Components = []Component{{ItemID: "y", ItemName: "Y", Qty: 1, ScrapPercent: 100}}
	if in.Validate() == nil {
		t.Error("100% scrap accepted")
	}
	in.Components[0].ScrapPercent = 5
	if err := in.Validate(); err != nil {
		t.Error(err)
	}
	if in.ScrapMethod != ScrapDivide || in.YieldPercent != 100 || in.IsPrimary == nil || !*in.IsPrimary {
		t.Errorf("defaults = %+v", in)
	}
	in.EffectiveFrom, in.EffectiveTo = "2026-05-01", "2026-04-01"
	if in.Validate() == nil {
		t.Error("reversed effective dates accepted")
	}
	in.EffectiveTo = ""
	f := false
	in.IsPrimary = &f
	if in.Validate() == nil {
		t.Error("unnamed alternate accepted")
	}
}

func TestEffectiveOn(t *testing.T) {
	from, to := "2026-04-01", "2026-09-30"
	b := &BOM{EffectiveFrom: &from, EffectiveTo: &to}
	for d, want := range map[string]bool{"2026-03-31": false, "2026-04-01": true, "2026-09-30": true, "2026-10-01": false} {
		if b.EffectiveOn(d) != want {
			t.Errorf("%s: %v", d, !want)
		}
	}
}
