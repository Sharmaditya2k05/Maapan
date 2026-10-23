package gst

import (
	"sort"
	"time"
)

// IST is the Indian Standard Time zone used for every date rule in this package.
// A fixed zone keeps the package free of tzdata so it also runs in distroless images.
var IST = time.FixedZone("IST", 5*3600+30*60)

// RateWindow is a GST slab and the period in which it is in force.
type RateWindow struct {
	BP   int       // rate in basis points (1800 = 18 %)
	From time.Time // zero = since GST began (1 Jul 2017)
	To   time.Time // zero = still in force (inclusive date when set)
	Note string
}

var (
	gstStart    = time.Date(2017, 7, 1, 0, 0, 0, 0, IST)
	diamond2022 = time.Date(2022, 7, 18, 0, 0, 0, 0, IST)
	// The 56th GST Council (3 Sep 2025) collapsed the 12 % and 28 % slabs into
	// 5 % / 18 % and added a 40 % de-merit rate, effective 22 Sep 2025.
	rateCut2025End   = time.Date(2025, 9, 21, 0, 0, 0, 0, IST)
	rateCut2025Start = time.Date(2025, 9, 22, 0, 0, 0, 0, IST)
)

// Schedule lists every slab that has applied under GST with its validity.
// It is the single place to touch when the Council changes rates. Tobacco,
// pan masala and related goods stayed at 28 % + compensation cess after
// 22 Sep 2025 pending the cess wind-down, so 28 % remains valid; item-level
// enforcement (which goods may use it) belongs to kar's HSN master.
var Schedule = []RateWindow{
	{BP: 0, Note: "nil-rated"},
	{BP: 25, Note: "rough diamonds and precious stones"},
	{BP: 150, From: diamond2022, Note: "cut and polished diamonds"},
	{BP: 300, Note: "gold, silver, jewellery"},
	{BP: 500, Note: "merit rate"},
	{BP: 1200, To: rateCut2025End, Note: "merged into 5 % by the 56th GST Council"},
	{BP: 1800, Note: "standard rate"},
	{BP: 2800, Note: "pre-22-Sep-2025 higher rate; tobacco and pan masala only after that"},
	{BP: 4000, From: rateCut2025Start, Note: "de-merit and luxury rate"},
}

// ValidRate reports whether bp was a slab in force on the given date.
// A zero date means "today".
func ValidRate(bp int, on time.Time) bool {
	d := dateIST(on)
	for _, w := range Schedule {
		if w.BP == bp && w.contains(d) {
			return true
		}
	}
	return false
}

// RatesOn returns the slabs in force on the given date, ascending.
// A zero date means "today".
func RatesOn(on time.Time) []int {
	d := dateIST(on)
	var out []int
	for _, w := range Schedule {
		if w.contains(d) {
			out = append(out, w.BP)
		}
	}
	sort.Ints(out)
	return out
}

// StandardRatesBP are the slabs in force when the process started.
//
// Deprecated: dated documents must use ValidRate(bp, on) so that, for
// example, an August-2025 purchase bill at 12 % still validates while a bill
// dated today does not.
var StandardRatesBP = RatesOn(time.Time{})

func (w RateWindow) contains(d time.Time) bool {
	from := w.From
	if from.IsZero() {
		from = gstStart
	}
	if d.Before(dateIST(from)) {
		return false
	}
	if !w.To.IsZero() && d.After(dateIST(w.To)) {
		return false
	}
	return true
}

// dateIST truncates t to a calendar date in IST; a zero time means today.
func dateIST(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now()
	}
	t = t.In(IST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, IST)
}
