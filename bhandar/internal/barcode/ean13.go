// Package barcode encodes EAN-13 and Code 128 (subset B) barcodes and
// renders them as SVG. It also implements the in-store EAN-13 conventions
// used by Maapan: tenant-generated item codes in GS1's restricted-circulation
// range (prefix 20–29) and the price/weight-embedded labels printed by retail
// weighing scales.
//
// The package depends only on the standard library.
package barcode

import (
	"fmt"
	"strings"
)

const (
	// internalPrefix is the two-digit prefix of tenant-generated in-store
	// codes. GS1 reserves 20–29 for restricted circulation; Maapan uses 21
	// for its per-tenant sequence numbers.
	internalPrefix = "21"
	// maxInternalSeq is the largest sequence number that fits in the ten
	// digits between the prefix and the check digit.
	maxInternalSeq int64 = 9_999_999_999
)

// EAN13CheckDigit returns the check digit for a 12-digit string.
//
// Digits are weighted 1,3,1,3,... from the left and the check digit is
// (10 − sum mod 10) mod 10. It returns an error unless first12 is exactly 12
// ASCII digits.
func EAN13CheckDigit(first12 string) (byte, error) {
	if len(first12) != 12 || !allDigits(first12) {
		return 0, fmt.Errorf("barcode: EAN13CheckDigit: want 12 digits, got %q", first12)
	}
	sum := 0
	for i := 0; i < 12; i++ {
		d := int(first12[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return byte('0' + (10-sum%10)%10), nil
}

// ValidEAN13 reports whether s is 13 ASCII digits with a correct check digit.
func ValidEAN13(s string) bool {
	if len(s) != 13 || !allDigits(s) {
		return false
	}
	c, err := EAN13CheckDigit(s[:12])
	return err == nil && c == s[12]
}

// InternalEAN13 builds an in-store EAN-13 from the per-tenant sequence
// number n: the prefix "21" (inside GS1's 200–299 restricted-circulation
// range), n zero-padded to 10 digits, then the check digit.
//
// It returns an error if n < 0 or n > 9_999_999_999.
func InternalEAN13(n int64) (string, error) {
	if n < 0 || n > maxInternalSeq {
		return "", fmt.Errorf("barcode: InternalEAN13: sequence %d outside 0..%d", n, maxInternalSeq)
	}
	first12 := fmt.Sprintf("%s%010d", internalPrefix, n)
	c, err := EAN13CheckDigit(first12)
	if err != nil {
		// Unreachable: first12 is always 12 digits.
		return "", err
	}
	return first12 + string(c), nil
}

// IsInternal reports whether ean is a 13-digit code starting with "20".."29",
// the GS1 range reserved for in-store use. It does not verify the check
// digit; use ValidEAN13 for that.
func IsInternal(ean string) bool {
	return len(ean) == 13 && allDigits(ean) && ean[0] == '2'
}

// NormalizeCode trims surrounding white space and upper-cases code. If the
// result is a 12-digit UPC-A it is converted to its EAN-13 form by prefixing
// "0" (which leaves the check digit unchanged).
func NormalizeCode(code string) string {
	s := strings.ToUpper(strings.TrimSpace(code))
	if len(s) == 12 && allDigits(s) {
		s = "0" + s
	}
	return s
}

// ParseWeighingScale decodes the price- or weight-embedded EAN-13 printed by
// retail weighing scales.
//
// Layout (13 digits, the common Indian scale default, "2FIIIIIVVVVVC"):
//
//	2      fixed first digit (GS1 restricted circulation)
//	F      scale flag digit; "2F" is the two-digit prefix 20–29
//	IIIII  5-digit item code (PLU) as configured on the scale
//	VVVVV  5-digit value field, meaning selected by the prefix:
//	       20–25  price with two implied decimals, i.e. rupees×100 = paise
//	       26–29  net weight in grams
//	C      EAN-13 check digit
//
// It returns the PLU, the value field as an integer, kind ("price" with
// value in paise, or "weight" with value in grams) and ok=true. ok is false,
// with zero values, for anything that is not a valid 13-digit EAN-13 with
// prefix 20–29.
func ParseWeighingScale(ean string) (plu string, value int64, kind string, ok bool) {
	if !ValidEAN13(ean) || ean[0] != '2' {
		return "", 0, "", false
	}
	if ean[1] <= '5' {
		kind = "price"
	} else {
		kind = "weight"
	}
	return ean[2:7], digitsValue(ean[7:12]), kind, true
}

// allDigits reports whether s is non-empty and consists only of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// digitsValue parses a string of ASCII digits as a non-negative integer.
// The caller guarantees s consists only of digits and is short enough not
// to overflow.
func digitsValue(s string) int64 {
	var v int64
	for i := 0; i < len(s); i++ {
		v = v*10 + int64(s[i]-'0')
	}
	return v
}
