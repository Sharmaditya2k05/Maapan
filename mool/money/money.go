// Package money handles amounts as int64 paise. Never use float64 for money
// except when converting user input or display values at the edges.
package money

import (
	"fmt"
	"math"
	"strings"
)

// FromRupees converts a rupee value (e.g. from a spreadsheet) to paise, rounding half away from zero.
func FromRupees(r float64) int64 { return int64(math.Round(r * 100)) }

// ToRupees converts paise to rupees for display/export only.
func ToRupees(p int64) float64 { return float64(p) / 100 }

// MulDiv returns round(a*b/c) with half-away-from-zero rounding, without float error.
func MulDiv(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	n := a * b
	q, r := n/c, n%c
	if r < 0 {
		r = -r
	}
	if 2*r >= abs(c) {
		if (n < 0) != (c < 0) {
			q--
		} else {
			q++
		}
	}
	return q
}

// ApplyBP returns round(amount * bp / 10000), e.g. ApplyBP(10000, 1800) = 1800.
func ApplyBP(amount int64, bp int) int64 { return MulDiv(amount, int64(bp), 10000) }

// RoundToRupee rounds paise to the nearest whole rupee and returns the rounded
// amount and the round-off (rounded - original).
func RoundToRupee(p int64) (rounded, roundOff int64) {
	rounded = MulDiv(p, 1, 100) * 100
	return rounded, rounded - p
}

// Format renders paise in Indian grouping: 12345678 -> "₹1,23,456.78".
func Format(p int64) string {
	sign := ""
	if p < 0 {
		sign = "-"
		p = -p
	}
	rupees, paise := p/100, p%100
	s := fmt.Sprintf("%d", rupees)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	return fmt.Sprintf("%s₹%s.%02d", sign, s, paise)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
