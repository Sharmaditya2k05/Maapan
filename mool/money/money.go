// Package money handles amounts as int64 paise. Never use float64 for money
// except when converting user input or display values at the edges.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseRupees converts a rupee amount written by a person or a spreadsheet
// ("1,23,456.78", "₹ 99", "Rs.1500", "-250", "(250)") to paise without going
// through float64. More than two decimals are rounded half away from zero.
func ParseRupees(s string) (int64, error) {
	t := strings.TrimSpace(s)
	t = strings.NewReplacer("₹", "", "Rs.", "", "Rs", "", "INR", "", ",", "", " ", "").Replace(t)
	neg := false
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		neg, t = true, t[1:len(t)-1]
	}
	switch {
	case strings.HasPrefix(t, "-"):
		neg, t = !neg, t[1:]
	case strings.HasPrefix(t, "+"):
		t = t[1:]
	}
	if t == "" {
		return 0, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(t, ".")
	if whole == "" {
		whole = "0"
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
	}
	rupees, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	var paise int64
	switch {
	case len(frac) == 0:
	case len(frac) == 1:
		paise = int64(frac[0]-'0') * 10
	default:
		paise = int64(frac[0]-'0')*10 + int64(frac[1]-'0')
		if len(frac) > 2 && frac[2] >= '5' {
			paise++
		}
	}
	p := rupees*100 + paise
	if neg {
		p = -p
	}
	return p, nil
}

// FromRupees converts a rupee value held in a float64 (e.g. from a JSON
// number) to paise, rounding half away from zero on the decimal value the
// float represents (1.005 → 101, not the binary artefact 100).
func FromRupees(r float64) int64 {
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return 0
	}
	if p, err := ParseRupees(strconv.FormatFloat(r, 'f', -1, 64)); err == nil {
		return p
	}
	return int64(math.Round(r * 100))
}

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
