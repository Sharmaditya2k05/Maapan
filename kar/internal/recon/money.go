// Package recon reconciles purchase bills with the supplier-reported GSTR-2B
// statement and records Invoice Management System (IMS) actions on each
// document. Amounts are held in paise; GSTN JSON carries rupees with two
// decimals, converted only at the JSON edge by Rs.
package recon

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Rs is an amount in paise that reads and writes GSTN JSON as rupees with two
// decimals (12345 paise ⇄ 123.45).
type Rs int64

// String renders rupees with two decimals.
func (r Rs) String() string {
	v, sign := int64(r), ""
	if v < 0 {
		v, sign = -v, "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// MarshalJSON writes a JSON number in rupees.
func (r Rs) MarshalJSON() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalJSON accepts a JSON number or numeric string in rupees.
func (r *Rs) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*r = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	p, err := ParseRupees(s)
	if err != nil {
		return err
	}
	*r = p
	return nil
}

// ParseRupees parses "1,234.5" / "-12" / "1e3" into paise, rounding half away
// from zero beyond two decimals. Empty means zero.
func ParseRupees(s string) (Rs, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, nil
	}
	if !strings.ContainsAny(s, "eE") {
		neg := strings.HasPrefix(s, "-")
		s = strings.TrimLeft(s, "+-")
		intPart, frac, _ := strings.Cut(s, ".")
		if intPart == "" {
			intPart = "0"
		}
		whole, err := strconv.ParseInt(intPart, 10, 64)
		if err == nil && (frac == "" || isDigits(frac)) {
			frac += "000"
			p := whole*100 + int64(frac[0]-'0')*10 + int64(frac[1]-'0')
			if frac[2] >= '5' {
				p++
			}
			if neg {
				p = -p
			}
			return Rs(p), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	return Rs(math.Round(f * 100)), nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Heads is a tax amount split by head.
type Heads struct {
	IGST Rs `json:"igst"`
	CGST Rs `json:"cgst"`
	SGST Rs `json:"sgst"`
	Cess Rs `json:"cess"`
}

// Add returns h + sign·o.
func (h Heads) Add(o Heads, sign int64) Heads {
	return Heads{IGST: h.IGST + Rs(sign)*o.IGST, CGST: h.CGST + Rs(sign)*o.CGST, SGST: h.SGST + Rs(sign)*o.SGST, Cess: h.Cess + Rs(sign)*o.Cess}
}
