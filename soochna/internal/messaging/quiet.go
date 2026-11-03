package messaging

import (
	"fmt"
	"strings"
	"time"
)

// QuietHours is a daily window (local time) in which promotional messages are
// held. TRAI's TCCCPR 2018 allows promotional commercial communication only
// between 09:00 and 21:00, so the default window is 21:00–09:00 IST.
// The zero value disables quiet hours.
type QuietHours struct {
	Start, End int // minutes after local midnight; Start > End wraps midnight
	Loc        *time.Location
}

// IST is India Standard Time (fixed, no DST); used when tzdata is unavailable.
var IST = time.FixedZone("IST", 5*3600+30*60)

// ParseQuietHours reads "HH:MM-HH:MM" ("" or "off" disables).
func ParseQuietHours(spec string, loc *time.Location) (QuietHours, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "off") {
		return QuietHours{}, nil
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return QuietHours{}, fmt.Errorf("quiet hours %q: want HH:MM-HH:MM", spec)
	}
	start, err1 := hhmm(a)
	end, err2 := hhmm(b)
	if err1 != nil || err2 != nil || start == end {
		return QuietHours{}, fmt.Errorf("quiet hours %q: want HH:MM-HH:MM", spec)
	}
	if loc == nil {
		loc = IST
	}
	return QuietHours{Start: start, End: end, Loc: loc}, nil
}

func hhmm(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Enabled reports whether a window is set.
func (q QuietHours) Enabled() bool { return q.Loc != nil && q.Start != q.End }

// NextAllowed returns (now, false) outside the window, or (end of the window, true) inside it.
func (q QuietHours) NextAllowed(now time.Time) (time.Time, bool) {
	if !q.Enabled() {
		return now, false
	}
	l := now.In(q.Loc)
	m := l.Hour()*60 + l.Minute()
	inside := false
	if q.Start < q.End {
		inside = m >= q.Start && m < q.End
	} else {
		inside = m >= q.Start || m < q.End
	}
	if !inside {
		return now, false
	}
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, q.Loc)
	end := midnight.Add(time.Duration(q.End) * time.Minute)
	if !end.After(l) {
		end = end.AddDate(0, 0, 1)
	}
	return end, true
}
