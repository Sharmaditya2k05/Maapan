package inventory

import (
	"testing"
	"time"
)

func TestWeightedAverage(t *testing.T) {
	// 10 @ ₹8.20 + 20 @ ₹9.10 → 30 @ ₹8.80
	if got := WeightedAverage(10, 820, 20, 910); got != 880 {
		t.Fatalf("avg = %d", got)
	}
	// Negative or empty stock takes the arrival cost.
	if WeightedAverage(-5, 820, 10, 900) != 900 || WeightedAverage(0, 820, 10, 900) != 900 {
		t.Fatal("empty/negative stock must reset the cost")
	}
	// An outbound (qtyIn <= 0) never changes the average.
	if WeightedAverage(10, 820, -3, 999) != 820 {
		t.Fatal("outbound must not change the average")
	}
}

func TestApply(t *testing.T) {
	q, avg, applied := Apply(0, 0, 10, 820) // purchase into empty stock
	if q != 10 || avg != 820 || applied != 820 {
		t.Fatalf("purchase: %v %d %d", q, avg, applied)
	}
	q, avg, applied = Apply(q, avg, -4, 0) // sale at average
	if q != 6 || avg != 820 || applied != 820 {
		t.Fatalf("sale: %v %d %d", q, avg, applied)
	}
	q, avg, applied = Apply(q, avg, 6, 940) // second purchase re-averages: (6×820 + 6×940)/12 = 880
	if q != 12 || avg != 880 || applied != 940 {
		t.Fatalf("second purchase: %v %d %d", q, avg, applied)
	}
	q, _, applied = Apply(q, avg, -15, 0) // POS never refuses: goes negative at the last cost
	if q != -3 || applied != 880 {
		t.Fatalf("negative: %v %d", q, applied)
	}
	// Nothing in stock and no cost yet: the caller's cost (item purchase price) is applied.
	if _, _, applied = Apply(0, 0, -1, 500); applied != 500 {
		t.Fatalf("fallback cost: %d", applied)
	}
	if Q3(1.23456) != 1.235 || ValuePaise(2.5, 101) != 253 || ValuePaise(-2, 100) != -200 {
		t.Fatal("rounding helpers")
	}
}

func TestTransferStateMachine(t *testing.T) {
	ok := [][3]string{
		{TransferDraft, ActionDispatch, TransferDispatched},
		{TransferDispatched, ActionReceive, TransferReceived},
		{TransferDraft, ActionCancel, TransferCancelled},
		{TransferDispatched, ActionCancel, TransferCancelled},
	}
	for _, c := range ok {
		if got, err := NextTransferStatus(c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("%s + %s = %s, %v (want %s)", c[0], c[1], got, err, c[2])
		}
	}
	bad := [][2]string{
		{TransferDraft, ActionReceive}, {TransferReceived, ActionCancel}, {TransferCancelled, ActionDispatch},
		{TransferDispatched, ActionDispatch}, {TransferReceived, ActionReceive},
	}
	for _, c := range bad {
		if _, err := NextTransferStatus(c[0], c[1]); err == nil {
			t.Errorf("%s + %s must fail", c[0], c[1])
		}
	}
}

func TestReorderSuggestion(t *testing.T) {
	if need, _ := ReorderSuggestion(30, 24, 96); need {
		t.Fatal("above level")
	}
	if need, q := ReorderSuggestion(24, 24, 96); !need || q != 96 {
		t.Fatalf("at level: %v %v", need, q)
	}
	if need, q := ReorderSuggestion(-10, 24, 20); !need || q != 34 {
		t.Fatalf("below level with small reorder qty must at least restore the level: %v %v", need, q)
	}
	if need, q := ReorderSuggestion(5, 24, 0); !need || q != 19 {
		t.Fatalf("no reorder qty configured: %v %v", need, q)
	}
	if need, _ := ReorderSuggestion(0, 0, 10); need {
		t.Fatal("level 0 means no re-ordering")
	}
}

func TestAgingAndDays(t *testing.T) {
	if AgingBucket(0) != "0-30" || AgingBucket(30) != "0-30" || AgingBucket(31) != "31-60" || AgingBucket(90) != "61-90" || AgingBucket(91) != "90+" {
		t.Fatal("buckets")
	}
	today := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	if DaysUntil(today, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)) != 30 || DaysUntil(today, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) != -22 {
		t.Fatal("days until")
	}
}

func TestToBaseUnit(t *testing.T) {
	alts := []AltUnit{{Unit: "BOX", Factor: 48}, {Unit: "DOZ", Factor: 12}}
	if q, ok := ToBaseUnit(2, "box", "PCS", alts); !ok || q != 96 {
		t.Fatalf("box: %v %v", q, ok)
	}
	if q, ok := ToBaseUnit(5, "", "PCS", alts); !ok || q != 5 {
		t.Fatal("empty unit = base")
	}
	if q, ok := ToBaseUnit(5, "pcs", "PCS", alts); !ok || q != 5 {
		t.Fatal("base unit, any case")
	}
	if q, ok := ToBaseUnit(5, "CTN", "PCS", alts); ok || q != 5 {
		t.Fatal("unknown unit passes through and reports !ok")
	}
}
