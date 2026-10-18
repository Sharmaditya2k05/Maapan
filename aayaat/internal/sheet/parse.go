package sheet

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ParsePaise parses an Indian-format amount ("₹ 1,23,456.78", "Rs. 500",
// "(1,200.00)", "-45", "1.2e3") into paise without float rounding errors.
// Empty input returns 0, nil.
func ParsePaise(s string) (int64, error) {
	s = cleanNumber(s)
	if s == "" {
		return 0, nil
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	if strings.HasSuffix(s, "-") { // "1200.00-" style negatives
		neg, s = !neg, strings.TrimSuffix(s, "-")
	}
	if strings.HasPrefix(s, "-") {
		neg, s = !neg, s[1:]
	}
	s = strings.TrimPrefix(s, "+")
	if strings.ContainsAny(s, "eE") { // Excel scientific notation
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		p := int64(math.Round(f * 100))
		if neg {
			p = -p
		}
		return p, nil
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !digits(whole) || (frac != "" && !digits(frac)) {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is too large", s)
	}
	frac += "000"
	f, _ := strconv.ParseInt(frac[:2], 10, 64)
	p := w*100 + f
	if frac[2] >= '5' { // round half up on the third decimal
		p++
	}
	if neg {
		p = -p
	}
	return p, nil
}

// ParseQty parses a quantity to 3 decimals ("1,250.5 Pcs" → 1250.5).
func ParseQty(s string) (float64, error) {
	s = cleanNumber(strings.TrimRightFunc(strings.TrimSpace(s), func(r rune) bool { return unicode.IsLetter(r) || r == ' ' }))
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a quantity", s)
	}
	return math.Round(f*1000) / 1000, nil
}

// ParseDrCr splits "1,200.50 Dr" / "Cr 300" / "-450" into paise (always >= 0)
// and side "dr" | "cr". side is "" when the text does not say; negative
// numbers without a marker are treated as Cr (MargBooks/Tally convention).
func ParseDrCr(s string) (int64, string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	side := ""
	for _, m := range []struct{ tok, side string }{{"dr", "dr"}, {"cr", "cr"}, {"debit", "dr"}, {"credit", "cr"}} {
		if strings.HasSuffix(t, m.tok) || strings.HasSuffix(t, m.tok+".") {
			side = m.side
			t = strings.TrimSuffix(strings.TrimSuffix(t, "."), m.tok)
			break
		}
		if strings.HasPrefix(t, m.tok+" ") || strings.HasPrefix(t, m.tok+".") {
			side = m.side
			t = strings.TrimPrefix(strings.TrimPrefix(t, m.tok), ".")
			break
		}
	}
	p, err := ParsePaise(t)
	if err != nil {
		return 0, "", err
	}
	if p < 0 {
		p = -p
		if side == "" {
			side = "cr"
		} else if side == "dr" {
			side = "cr"
		} else {
			side = "dr"
		}
	}
	return p, side, nil
}

// NormalizeSide maps "Dr", "D", "Debit", "Cr", "C", "Credit" to dr/cr ("" if unknown).
func NormalizeSide(s string) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), ".")) {
	case "dr", "d", "debit", "receivable":
		return "dr"
	case "cr", "c", "credit", "payable":
		return "cr"
	}
	return ""
}

// ParseRateBP parses a GST rate ("18", "18%", "18.00 %", "GST 5%") to basis points.
func ParseRateBP(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimPrefix(t, "gst")
	t = strings.TrimPrefix(t, "igst")
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "%"))
	if t == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 || f > 100 {
		return 0, fmt.Errorf("%q is not a GST rate", s)
	}
	return int(math.Round(f * 100)), nil
}

var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

var monthYear = regexp.MustCompile(`^(\d{1,2})[/\-.](\d{2}|\d{4})$`)

// ParseDate accepts dd/mm/yyyy, dd-mm-yyyy, dd.mm.yyyy, yyyy-mm-dd, dd-Mon-yyyy,
// Excel serial numbers and MargBooks expiry style MM/YY (→ last day of month).
// It returns YYYY-MM-DD.
func ParseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if m := monthYear.FindStringSubmatch(s); m != nil {
		mon, _ := strconv.Atoi(m[1])
		yr, _ := strconv.Atoi(m[2])
		if yr < 100 {
			yr += 2000
		}
		if mon >= 1 && mon <= 12 {
			return time.Date(yr, time.Month(mon)+1, 0, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), nil
		}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 1000 && n < 100000 {
		return excelEpoch.AddDate(0, 0, int(n)).Format(time.DateOnly), nil
	}
	for _, layout := range []string{"02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006", "02.01.2006", "2006-01-02",
		"02-Jan-2006", "2-Jan-2006", "02 Jan 2006", "02-Jan-06", "02/01/06", "02-01-06"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.DateOnly), nil
		}
	}
	return "", errors.New("unrecognised date " + strconvQuote(s) + " (use dd/mm/yyyy)")
}

func strconvQuote(s string) string { return strconv.Quote(s) }

func cleanNumber(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, p := range []string{"₹", "rs.", "rs", "inr"} {
		if strings.HasPrefix(low, p) {
			s = s[len(p):]
			low = low[len(p):]
		}
	}
	return strings.NewReplacer(",", "", " ", "", " ", "", "₹", "").Replace(s)
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
