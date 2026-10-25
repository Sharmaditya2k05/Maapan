package production

import (
	"testing"

	"github.com/maapan/nirmaan/internal/bom"
)

func TestNextStatus(t *testing.T) {
	ok := [][3]string{
		{StatusPlanned, "start", StatusInProgress},
		{StatusInProgress, "complete", StatusCompleted},
		{StatusPlanned, "complete", StatusCompleted},
		{StatusPlanned, "cancel", StatusCancelled},
		{StatusInProgress, "cancel", StatusCancelled},
	}
	for _, c := range ok {
		if got, err := NextStatus(c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("%s+%s = %s %v", c[0], c[1], got, err)
		}
	}
	for _, c := range [][2]string{{StatusCompleted, "cancel"}, {StatusInProgress, "start"}, {StatusCancelled, "complete"}} {
		if _, err := NextStatus(c[0], c[1]); err == nil {
			t.Errorf("%s+%s should fail", c[0], c[1])
		}
	}
}

func TestPlanLinesShortagesAndCost(t *testing.T) {
	b := &bom.BOM{ID: "b", ItemID: "bread", ItemName: "Bread", OutputQty: 10, Unit: "PCS",
		Components: []bom.Component{
			{ItemID: "flour", ItemName: "Flour", Qty: 5, Unit: "KG", ScrapPercent: 2},
			{ItemID: "yeast", ItemName: "Yeast", Qty: 100, Unit: "GM"},
		},
		Operations: []bom.Operation{{Name: "Baking", CostPaise: 5000}}}
	lines := PlanLines(b, 20)
	if len(lines) != 3 || lines[0].PlannedQty != 10.2 || lines[1].PlannedQty != 200 || lines[2].Kind != "produce" || lines[2].PlannedQty != 20 {
		t.Fatalf("lines = %+v", lines)
	}
	sh := Shortages(lines, map[string]float64{"flour": 8, "yeast": 500})
	if len(sh) != 1 || sh[0].ItemID != "flour" || sh[0].Short != 2.2 {
		t.Fatalf("shortages = %+v", sh)
	}
	// flour 10 kg actual × ₹40 = 40000, yeast 200 g × 50p = 10000, ops 5000 × 20/10 = 10000 → 60000; 19 loaves made
	_, total, unit := CostRun(lines, map[string]float64{"flour": 10}, map[string]int64{"flour": 4000, "yeast": 50}, 5000, 10, 20, 19)
	if total != 60000 || unit != 3158 {
		t.Fatalf("total %d unit %d", total, unit)
	}
}
