package production

import (
	"testing"

	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/units"
)

func TestNextStatus(t *testing.T) {
	ok := [][3]string{
		{StatusPlanned, "start", StatusInProgress},
		{StatusInProgress, "complete", StatusCompleted},
		{StatusPlanned, "complete", StatusCompleted},
		{StatusInProgress, "receive", StatusInProgress},
		{StatusInProgress, "fail", StatusFailed},
		{StatusInProgress, "issue", StatusInProgress},
		{StatusPlanned, "cancel", StatusCancelled},
		{StatusInProgress, "cancel", StatusCancelled},
	}
	for _, c := range ok {
		if got, err := NextStatus(c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("%s+%s = %s %v", c[0], c[1], got, err)
		}
	}
	for _, c := range [][2]string{{StatusCompleted, "cancel"}, {StatusInProgress, "start"}, {StatusCancelled, "complete"},
		{StatusFailed, "receive"}, {StatusPlanned, "issue"}} {
		if _, err := NextStatus(c[0], c[1]); err == nil {
			t.Errorf("%s+%s should fail", c[0], c[1])
		}
	}
}

func bread(method string) *bom.BOM {
	return &bom.BOM{ID: "b", ItemID: "bread", ItemName: "Bread", OutputQty: 10, Unit: "PCS", ScrapMethod: method,
		Components: []bom.Component{
			{ItemID: "flour", ItemName: "Flour", Qty: 5, Unit: "KG", ScrapPercent: 2},
			{ItemID: "yeast", ItemName: "Yeast", Qty: 100, Unit: "GM"},
		},
		Operations: []bom.Operation{{Name: "Baking", CostPaise: 5000}}}
}

func TestPlanLinesConvertUnits(t *testing.T) {
	cat := units.Catalog{"flour": {Base: "KG"}, "yeast": {Base: "KG"}, "bread": {Base: "PCS"}}
	lines, err := PlanLines(bread(bom.ScrapDivide), 20, cat)
	if err != nil {
		t.Fatal(err)
	}
	// flour 10 / 0.98 = 10.204 kg; yeast 200 GM = 0.2 KG
	if lines[0].PlannedQty != 10.204 || lines[1].PlannedQty != 200 || lines[1].PlannedBaseQty != 0.2 || lines[1].BaseUnit != "KG" {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[2].Kind != KindProduce || lines[2].PlannedBaseQty != 20 {
		t.Fatalf("produce = %+v", lines[2])
	}
	cat["yeast"] = units.ItemUnits{Base: "PCS"}
	if _, err := PlanLines(bread(bom.ScrapDivide), 20, cat); err == nil {
		t.Fatal("GM of a PCS item accepted")
	}
}

func TestShortagesWithReservations(t *testing.T) {
	lines, _ := PlanLines(bread(bom.ScrapMultiply), 20, nil)
	// flour 10.2 needed; 12 on hand but 3 issued to another run
	sh := Shortages(lines, map[string]float64{"flour": 12, "yeast": 500}, map[string]float64{"flour": 3})
	if len(sh) != 1 || sh[0].ItemID != "flour" || sh[0].Short != 1.2 || sh[0].Reserved != 3 {
		t.Fatalf("shortages = %+v", sh)
	}
}

func prices() map[string]Price {
	return map[string]Price{"flour": {4000, "avg_store"}, "yeast": {50, "avg_store"}}
}

func TestCalcReceiptFinalLikeBefore(t *testing.T) {
	// old behaviour: plan 20, 19 made, flour overridden to 10 kg → 60000 total, 3158 per loaf
	b := bread(bom.ScrapMultiply)
	lines, _ := PlanLines(b, 20, nil)
	c, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 20, Good: 19, Final: true,
		Overrides: map[int]float64{1: 10}, Prices: prices()})
	if err != nil {
		t.Fatal(err)
	}
	// flour 10 × 4000 = 40000; yeast 200 × 50 = 10000; ops 5000 × 20/10 = 10000
	if c.TotalPaise != 60000 || c.UnitCostPaise != 3158 || c.MaterialPaise != 50000 || c.OperationsPaise != 10000 {
		t.Fatalf("calc = %+v", c)
	}
}

func TestCalcReceiptPartialThenFinal(t *testing.T) {
	b := bread(bom.ScrapMultiply)
	lines, _ := PlanLines(b, 20, nil)
	// first half: 9 good + 1 rejected → half the material and operations
	c1, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 20, Good: 9, Rejected: 1, Prices: prices()})
	if err != nil {
		t.Fatal(err)
	}
	// flour 5.1 × 4000 = 20400; yeast 100 × 50 = 5000; ops 5000
	if c1.MaterialPaise != 25400 || c1.OperationsPaise != 5000 || c1.UnitCostPaise != units.Rate(30400, 9) {
		t.Fatalf("partial = %+v", c1)
	}
	for i := range lines {
		for _, cl := range c1.Lines {
			if cl.LineNo == lines[i].LineNo {
				q := cl.Qty
				lines[i].ActualQty = &q
			}
		}
	}
	// final: 8 good, nothing said about the rest → settles the whole run (20)
	c2, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 20, PriorPortion: 10, Good: 8, Final: true, Prices: prices()})
	if err != nil {
		t.Fatal(err)
	}
	if c2.MaterialPaise != 25400 || c2.OperationsPaise != 5000 || c2.EffectivePortion != 20 {
		t.Fatalf("final = %+v", c2)
	}
	if _, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 20, Prices: prices()}); err == nil {
		t.Error("empty partial accepted")
	}
}

func TestCalcReceiptByProductsUnplannedLabourMissing(t *testing.T) {
	b := bread(bom.ScrapMultiply)
	b.Operations = []bom.Operation{{Name: "Baking", Hours: 2, RatePaisePerHour: 10000}} // 2 h × ₹100 per batch of 10
	lines, _ := PlanLines(b, 10, nil)
	lines = append(lines, Line{LineNo: 9, Kind: KindConsume, ItemID: "salt", ItemName: "Salt", BaseUnit: "KG", Factor: 1, Unplanned: true})
	hours := 3.0
	p := prices()
	p["salt"] = Price{2000, "avg_store"}
	c, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 10, Good: 10, Final: true,
		Overrides: map[int]float64{9: 0.5}, LabourHours: &hours, Prices: p,
		ByProducts: []ByProductLine{{LineNo: 10, ItemID: "crumbs", ItemName: "Crumbs", BaseUnit: "KG", Qty: 1, Rate: Price{1000, "manual"}}}})
	if err != nil {
		t.Fatal(err)
	}
	// flour 5.1 × 4000 = 20400, yeast 100 × 50 = 5000, salt 0.5 × 2000 = 1000 → 26400
	// labour 3 h at the routing rate ₹100/h = 30000; by-product 1000 → main 55400 over 10
	if c.MaterialPaise != 26400 || c.OperationsPaise != 30000 || c.ByproductPaise != 1000 || c.MainCostPaise != 55400 ||
		c.UnitCostPaise != 5540 || c.LabourHours != 3 {
		t.Fatalf("calc = %+v", c)
	}
	delete(p, "yeast")
	c, err = CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 10, Good: 10, Final: true, Prices: p})
	if err != nil || len(c.Missing) != 1 || c.Missing[0] != "yeast" {
		t.Fatalf("missing = %+v %v", c.Missing, err)
	}
}

func TestCalcReceiptFailedBatch(t *testing.T) {
	b := bread(bom.ScrapMultiply)
	lines, _ := PlanLines(b, 20, nil)
	c, err := CalcReceipt(ReceiptInput{BOM: b, Lines: lines, RunQty: 20, Good: 0, Final: true, Prices: prices()})
	if err != nil {
		t.Fatal(err)
	}
	if c.TotalPaise != 50800+10000 || c.UnitCostPaise != 0 || c.GoodBaseQty != 0 {
		t.Fatalf("failed = %+v", c)
	}
	for _, l := range c.Lines {
		if l.Kind == KindProduce {
			t.Fatal("failed batch produced goods")
		}
	}
}

func TestYieldAndPostingID(t *testing.T) {
	a, v := Yield(18, 2, 20)
	if *a != 90 || *v != 90 {
		t.Errorf("yield %v %v", *a, *v)
	}
	if PostingID("r", 1) != "r" || PostingID("r", 3) != "r/3" {
		t.Error("posting id")
	}
}
