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
