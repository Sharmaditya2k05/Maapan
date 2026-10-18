// Package forecast predicts item demand with simple exponential smoothing on a
// day-of-week de-seasonalised series. Pure Go, no LLM: cheap enough to run for
// every item of every store each night.
package forecast

import (
	"math"
	"time"
)

// Model is a fitted demand model for one item at one store.
type Model struct {
	Level       float64    // de-seasonalised units per day
	DOW         [7]float64 // multipliers indexed by time.Weekday (Sunday=0)
	HistoryDays int
}

// DefaultAlpha weights recent days; 0.3 reacts within ~1 week without chasing noise.
const DefaultAlpha = 0.3

// Fit fits daily quantities; daily[i] is the quantity sold on start+i days
// (days with no sales must be present as 0).
func Fit(daily []float64, start time.Time, alpha float64) Model {
	m := Model{HistoryDays: len(daily)}
	for i := range m.DOW {
		m.DOW[i] = 1
	}
	if len(daily) == 0 {
		return m
	}
	if alpha <= 0 || alpha > 1 {
		alpha = DefaultAlpha
	}

	var total float64
	for _, q := range daily {
		total += q
	}
	mean := total / float64(len(daily))
	if mean == 0 {
		return m
	}

	// Day-of-week factors need at least two full weeks; shrink towards 1 with a
	// prior of 2 observations so a single odd Sunday doesn't dominate.
	if len(daily) >= 14 {
		var sum [7]float64
		var cnt [7]float64
		for i, q := range daily {
			wd := start.AddDate(0, 0, i).Weekday()
			sum[wd] += q
			cnt[wd]++
		}
		const prior = 2.0
		var norm float64
		for d := 0; d < 7; d++ {
			raw := 1.0
			if cnt[d] > 0 {
				raw = (sum[d] / cnt[d]) / mean
			}
			m.DOW[d] = (cnt[d]*raw + prior) / (cnt[d] + prior)
			norm += m.DOW[d]
		}
		norm /= 7
		for d := range m.DOW {
			m.DOW[d] /= norm
		}
	}

	// SES over the de-seasonalised series, initialised with the first week's mean.
	init := len(daily)
	if init > 7 {
		init = 7
	}
	var level float64
	for i := 0; i < init; i++ {
		level += daily[i] / m.factor(start.AddDate(0, 0, i))
	}
	level /= float64(init)
	for i := init; i < len(daily); i++ {
		x := daily[i] / m.factor(start.AddDate(0, 0, i))
		level = alpha*x + (1-alpha)*level
	}
	m.Level = math.Max(level, 0)
	return m
}

func (m Model) factor(d time.Time) float64 {
	f := m.DOW[d.Weekday()]
	if f <= 0 {
		return 1
	}
	return f
}

// Demand returns the expected units sold over [from, from+days).
func (m Model) Demand(from time.Time, days int) float64 {
	var s float64
	for i := 0; i < days; i++ {
		s += m.Level * m.factor(from.AddDate(0, 0, i))
	}
	return s
}

// Suggestion is a re-order recommendation.
type Suggestion struct {
	Needed       bool    `json:"needed"`
	DemandLead   float64 `json:"demand_over_lead_time"`
	SuggestedQty float64 `json:"suggested_qty"`
}

// Reorder recommends buying when demand over the supplier lead time exceeds what
// is on hand; it then orders enough to also cover coverDays after arrival.
func Reorder(m Model, from time.Time, onHand float64, leadDays, coverDays int) Suggestion {
	lead := m.Demand(from, leadDays)
	s := Suggestion{DemandLead: round3(lead)}
	if lead <= onHand {
		return s
	}
	target := m.Demand(from, leadDays+coverDays)
	s.Needed = true
	s.SuggestedQty = math.Ceil(target - onHand)
	return s
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
