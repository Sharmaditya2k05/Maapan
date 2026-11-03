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

// Stock is bhandar's view of one item at one store.
type Stock struct {
	OnHand       float64
	ReorderLevel float64 // 0 = no rule set
	ReorderQty   float64 // 0 = top up to twice the level
}

// Combined is a re-order suggestion from both signals.
type Combined struct {
	Needed         bool     `json:"needed"`
	SuggestedQty   float64  `json:"suggested_qty"`
	ForecastQty    float64  `json:"forecast_qty"`     // what demand over lead time + cover needs
	RuleQty        float64  `json:"reorder_rule_qty"` // what bhandar's re-order rule asks for
	DemandLead     float64  `json:"forecast_demand_over_lead_time"`
	DaysOfCover    *float64 `json:"days_of_cover,omitempty"` // on hand ÷ forecast daily demand
	StockoutRisk   bool     `json:"stockout_before_delivery"`
	Reasons        []string `json:"reasons"` // forecast | below_reorder_level
	ForecastActive bool     `json:"-"`
}

// Suggest combines buddhi's demand forecast (m, nil when the item has no
// sales history) with bhandar's stock and re-order rule. The larger of the two
// quantities wins, so a slow seller still respects its re-order level and a
// fast seller is bought before it hits the level.
func Suggest(m *Model, from time.Time, st Stock, leadDays, coverDays int) Combined {
	c := Combined{Reasons: []string{}}
	if m != nil && m.Level > 0 {
		c.ForecastActive = true
		f := Reorder(*m, from, st.OnHand, leadDays, coverDays)
		c.DemandLead = f.DemandLead
		if f.Needed {
			c.ForecastQty = f.SuggestedQty
			c.Reasons = append(c.Reasons, "forecast")
		}
		cover := math.Round(100*math.Max(st.OnHand, 0)/m.Level) / 100
		c.DaysOfCover = &cover
		c.StockoutRisk = cover < float64(leadDays)
	}
	if st.ReorderLevel > 0 && st.OnHand <= st.ReorderLevel {
		q := st.ReorderQty
		if q <= 0 {
			q = 2*st.ReorderLevel - st.OnHand
		}
		c.RuleQty = math.Ceil(math.Max(q, 0))
		c.Reasons = append(c.Reasons, "below_reorder_level")
		if !c.ForecastActive && st.OnHand <= 0 {
			c.StockoutRisk = true
		}
	}
	c.SuggestedQty = math.Max(c.ForecastQty, c.RuleQty)
	c.Needed = c.SuggestedQty > 0
	return c
}
