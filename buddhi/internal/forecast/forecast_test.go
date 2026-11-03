package forecast

import (
	"math"
	"testing"
	"time"
)

func TestFitFlatSeries(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC) // a Sunday
	daily := make([]float64, 28)
	for i := range daily {
		daily[i] = 10
	}
	m := Fit(daily, start, DefaultAlpha)
	if math.Abs(m.Level-10) > 1e-9 {
		t.Fatalf("level = %v", m.Level)
	}
	if d := m.Demand(start.AddDate(0, 0, 28), 7); math.Abs(d-70) > 1e-6 {
		t.Fatalf("7-day demand = %v", d)
	}
}

func TestFitWeekendSeasonality(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC) // Sunday
	daily := make([]float64, 56)
	for i := range daily {
		if wd := start.AddDate(0, 0, i).Weekday(); wd == time.Saturday || wd == time.Sunday {
			daily[i] = 30
		} else {
			daily[i] = 10
		}
	}
	m := Fit(daily, start, DefaultAlpha)
	if m.DOW[time.Sunday] <= 1.5 || m.DOW[time.Wednesday] >= 1 {
		t.Fatalf("expected weekend uplift, got %v", m.DOW)
	}
	next := start.AddDate(0, 0, 56) // Sunday again
	if sun, wed := m.Demand(next, 1), m.Demand(next.AddDate(0, 0, 3), 1); sun <= 2*wed {
		t.Fatalf("sunday %v should be well above wednesday %v", sun, wed)
	}
	// Weekly total should stay close to the true 110 units/week.
	if w := m.Demand(next, 7); math.Abs(w-110) > 8 {
		t.Fatalf("weekly demand %v", w)
	}
}

func TestReorder(t *testing.T) {
	m := Model{Level: 5}
	for i := range m.DOW {
		m.DOW[i] = 1
	}
	now := time.Now()
	if s := Reorder(m, now, 100, 7, 7); s.Needed {
		t.Fatalf("100 on hand covers 35: %+v", s)
	}
	s := Reorder(m, now, 20, 7, 7)
	if !s.Needed || s.SuggestedQty != 50 {
		t.Fatalf("want 70-20=50, got %+v", s)
	}
}

func TestEmptyAndZero(t *testing.T) {
	if m := Fit(nil, time.Now(), 0); m.Level != 0 {
		t.Fatal("empty")
	}
	if m := Fit(make([]float64, 30), time.Now(), 0); m.Level != 0 || m.DOW[0] != 1 {
		t.Fatal("zero series")
	}
}

func TestSuggestCombinesForecastAndRule(t *testing.T) {
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	flat := &Model{Level: 10}
	for i := range flat.DOW {
		flat.DOW[i] = 1
	}
	// Fast seller above its level: forecast alone triggers (70 over 7 days > 30).
	c := Suggest(flat, from, Stock{OnHand: 30, ReorderLevel: 20, ReorderQty: 50}, 7, 7)
	if !c.Needed || c.ForecastQty != 110 || c.RuleQty != 0 || c.SuggestedQty != 110 || !c.StockoutRisk {
		t.Fatalf("forecast case %+v", c)
	}
	if c.DaysOfCover == nil || *c.DaysOfCover != 3 {
		t.Fatalf("days of cover %+v", c.DaysOfCover)
	}
	// Slow seller at its level: the rule wins.
	slow := &Model{Level: 0.1}
	for i := range slow.DOW {
		slow.DOW[i] = 1
	}
	c = Suggest(slow, from, Stock{OnHand: 5, ReorderLevel: 5, ReorderQty: 24}, 7, 7)
	if !c.Needed || c.SuggestedQty != 24 || len(c.Reasons) != 1 || c.Reasons[0] != "below_reorder_level" {
		t.Fatalf("rule case %+v", c)
	}
	// No history, no rule: nothing to say.
	if c := Suggest(nil, from, Stock{OnHand: 0}, 7, 7); c.Needed {
		t.Fatalf("%+v", c)
	}
	// No history, below level without a qty: top up to twice the level.
	if c := Suggest(nil, from, Stock{OnHand: 2, ReorderLevel: 10}, 7, 7); c.SuggestedQty != 18 {
		t.Fatalf("%+v", c)
	}
}
