package bom

import (
	"errors"
	"testing"
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

// Chair (1) = 4 legs (sub-assembly, 2% scrap) + 1 seat board + 2 hrs labour (₹100 op)
// Leg (10) = 5 kg wood (10% scrap) + 20 screws
func fixture() (memResolver, *BOM) {
	leg := &BOM{ID: "bom-leg", ItemID: "leg", ItemName: "Leg", OutputQty: 10, Components: []Component{
		{ItemID: "wood", ItemName: "Wood", Qty: 5, Unit: "KG", ScrapPercent: 10},
		{ItemID: "screw", ItemName: "Screw", Qty: 20, Unit: "PCS"},
	}, Operations: []Operation{{Name: "Turning", CostPaise: 5000}}}
	chair := &BOM{ID: "bom-chair", ItemID: "chair", ItemName: "Chair", OutputQty: 1, Components: []Component{
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

func TestExplodeMultiLevelWithScrap(t *testing.T) {
	r, chair := fixture()
	tree, flat, err := Explode(r, chair, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 10 chairs → 40 legs × 1.02 = 40.8 legs
	// wood: 5/10 × 40.8 × 1.1 = 22.44 kg ; screws: 20/10 × 40.8 + 6 × 10 = 81.6 + 60 = 141.6
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
}

func TestExplodeDetectsCycle(t *testing.T) {
	a := &BOM{ID: "A", ItemID: "a", OutputQty: 1, Components: []Component{{ItemID: "b", ItemName: "B", Qty: 1, IsSubAssembly: true}}}
	b := &BOM{ID: "B", ItemID: "b", OutputQty: 1, Components: []Component{{ItemID: "c", ItemName: "C", Qty: 1, IsSubAssembly: true}}}
	c := &BOM{ID: "C", ItemID: "c", OutputQty: 1, Components: []Component{{ItemID: "a", ItemName: "A", Qty: 1, IsSubAssembly: true}}}
	r := memResolver{byID: map[string]*BOM{"A": a, "B": b, "C": c}, activeOf: map[string]string{"a": "A", "b": "B", "c": "C"}}
	if _, _, err := Explode(r, a, 1); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
	if _, err := Cost(r, a, 1, nil); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected cycle in cost, got %v", err)
	}
}

func TestExplodeMissingSubBOM(t *testing.T) {
	r, chair := fixture()
	delete(r.activeOf, "leg")
	if _, _, err := Explode(r, chair, 1); err == nil {
		t.Fatal("expected error for sub-assembly without active BOM")
	}
}

func TestCostRollUp(t *testing.T) {
	r, chair := fixture()
	prices := map[string]int64{"wood": 8000, "screw": 200, "seat": 30000} // ₹80/kg, ₹2/screw, ₹300/seat
	res, err := Cost(r, chair, 2, prices)
	if err != nil {
		t.Fatal(err)
	}
	// Leg unit cost = (5×1.1×8000 + 20×200 + 5000) / 10 = (44000 + 4000 + 5000)/10 = 5300
	// Chair material per unit = 4×1.02×5300 + 30000 + 6×200 = 21624 + 30000 + 1200 = 52824
	// 2 chairs: material 105648, ops 2×10000 = 20000, total 125648, unit 62824
	if res.Lines[0].UnitCostPaise != 5300 {
		t.Errorf("leg unit cost %d", res.Lines[0].UnitCostPaise)
	}
	if res.MaterialPaise != 105648 || res.OperationsPaise != 20000 || res.TotalPaise != 125648 || res.UnitCostPaise != 62824 {
		t.Errorf("cost = %+v", res)
	}
	if len(res.MissingPrices) != 0 {
		t.Errorf("missing = %v", res.MissingPrices)
	}
	delete(prices, "seat")
	res, _ = Cost(r, chair, 1, prices)
	if len(res.MissingPrices) != 1 || res.MissingPrices[0] != "seat" {
		t.Errorf("missing = %v", res.MissingPrices)
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
}
