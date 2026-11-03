package posting

import (
	"testing"

	"github.com/maapan/mool/events"
)

func entriesBy(v Voucher) map[string][2]int64 {
	m := map[string][2]int64{}
	for _, e := range v.Entries {
		cur := m[e.AccountCode]
		m[e.AccountCode] = [2]int64{cur[0] + e.Debit, cur[1] + e.Credit}
	}
	return m
}

func TestProductionPosting(t *testing.T) {
	raw := []events.ProductionLine{{Qty: 10, UnitCostPaise: 1000}} // ₹100 of material

	// Normal run with ₹20 of labour absorbed.
	v, err := Production(events.ProductionCompletedPayload{ProductionRunID: "r1", Consumed: raw, TotalCostPaise: 12000}, "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	if m := entriesBy(v); m[StockFinished][0] != 12000 || m[StockRaw][1] != 10000 || m[WorkInProgress][1] != 2000 {
		t.Fatalf("normal run: %v", m)
	}

	// Failed batch: material written off, no finished stock.
	v, err = Production(events.ProductionCompletedPayload{ProductionRunID: "r2", Consumed: raw, TotalCostPaise: 10000, Failed: true}, "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	if m := entriesBy(v); m[StockLoss][0] != 10000 || m[StockRaw][1] != 10000 || m[StockFinished][0] != 0 {
		t.Fatalf("failed run: %v", m)
	}

	// By-products took ₹30 of the cost: main product ₹70, by-products stay in stock.
	v, err = Production(events.ProductionCompletedPayload{ProductionRunID: "r3", Consumed: raw, TotalCostPaise: 7000}, "2026-11-01")
	if err != nil {
		t.Fatalf("by-product run must balance: %v", err)
	}
	if m := entriesBy(v); m[StockFinished][0] != 10000 || m[StockRaw][1] != 10000 || m[WorkInProgress] != [2]int64{} {
		t.Fatalf("by-product run: %v", m)
	}
}
