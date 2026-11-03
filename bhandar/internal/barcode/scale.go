package barcode

import "fmt"

// ScaleFormat is a configurable layout of a weighing-scale EAN-13 for one
// two-digit prefix (20–29):
//
//	PP  I…I  [K]  V…V  C
//
// PP is the prefix, I…I the PLU (PLULen digits, 4–6), K an optional internal
// price/weight check digit (CheckDigit), V…V the value (whatever is left of the
// 10 digits between the prefix and the EAN check digit C).
//
// Kind is "price" (value in rupees with ValueDecimals decimals; reported in
// paise), "weight" (value in kg with ValueDecimals decimals; reported in grams)
// or "none" (the prefix is not a scale code, e.g. it carries in-store item codes).
type ScaleFormat struct {
	Prefix        string `json:"prefix"`
	Kind          string `json:"kind"`
	PLULen        int    `json:"plu_len"`
	CheckDigit    bool   `json:"check_digit"`
	ValueDecimals int    `json:"value_decimals"`
}

// DefaultScaleFormat is the layout used for a prefix the tenant has not
// configured: "2F IIIII VVVVV C", 20–25 price in paise, 26–29 weight in grams
// (the behaviour of ParseWeighingScale).
func DefaultScaleFormat(prefix string) ScaleFormat {
	f := ScaleFormat{Prefix: prefix, Kind: "price", PLULen: 5, ValueDecimals: 2}
	if len(prefix) == 2 && prefix[1] > '5' {
		f.Kind, f.ValueDecimals = "weight", 3
	}
	return f
}

// Validate checks a format.
func (f ScaleFormat) Validate() error {
	if len(f.Prefix) != 2 || f.Prefix[0] != '2' || f.Prefix[1] < '0' || f.Prefix[1] > '9' {
		return fmt.Errorf("prefix must be 20–29")
	}
	switch f.Kind {
	case "price", "weight", "none":
	default:
		return fmt.Errorf("kind must be price, weight or none")
	}
	if f.Kind == "none" {
		return nil
	}
	if f.PLULen < 4 || f.PLULen > 6 {
		return fmt.Errorf("plu_len must be 4 to 6")
	}
	if f.valueLen() < 4 {
		return fmt.Errorf("layout leaves %d value digits (need at least 4)", f.valueLen())
	}
	max := 3
	if f.Kind == "price" {
		max = 2
	}
	if f.ValueDecimals < 0 || f.ValueDecimals > max {
		return fmt.Errorf("value_decimals must be 0 to %d for %s", max, f.Kind)
	}
	return nil
}

func (f ScaleFormat) valueLen() int {
	n := 10 - f.PLULen
	if f.CheckDigit {
		n--
	}
	return n
}

// ParseScale decodes ean with format f. value is paise (price) or grams
// (weight). ok is false for an invalid EAN-13, a different prefix, an invalid
// format or kind "none".
func ParseScale(ean string, f ScaleFormat) (plu string, value int64, kind string, ok bool) {
	if !ValidEAN13(ean) || ean[:2] != f.Prefix || f.Kind == "none" || f.Validate() != nil {
		return "", 0, "", false
	}
	plu = ean[2 : 2+f.PLULen]
	start := 2 + f.PLULen
	if f.CheckDigit {
		start++
	}
	value = digitsValue(ean[start:12])
	// price → paise (2 decimals); weight → grams (3 decimals of kg)
	target := 2
	if f.Kind == "weight" {
		target = 3
	}
	for d := f.ValueDecimals; d < target; d++ {
		value *= 10
	}
	return plu, value, f.Kind, true
}
