package recon

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// NormaliseDocNo reduces an invoice number to the form suppliers and buyers
// most often agree on: upper case, only letters and digits (spaces, '/', '-'
// and other punctuation dropped) and no leading zeros in a run of digits.
// "inv/26-27/0042" and "INV 2627-42" both become "INV262742".
func NormaliseDocNo(s string) string {
	var b strings.Builder
	var digits []rune
	flush := func() {
		if len(digits) == 0 {
			return
		}
		i := 0
		for i < len(digits)-1 && digits[i] == '0' {
			i++
		}
		b.WriteString(string(digits[i:]))
		digits = digits[:0]
	}
	for _, c := range strings.ToUpper(s) {
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case unicode.IsLetter(c):
			flush()
			b.WriteRune(c)
		default:
			flush()
		}
	}
	flush()
	return b.String()
}

// ParsePeriod parses a return period MMYYYY into its first and last day (YYYY-MM-DD).
func ParsePeriod(p string) (from, to string, err error) {
	t, err := time.Parse("012006", p)
	if err != nil || len(p) != 6 {
		return "", "", fmt.Errorf("period must be MMYYYY, e.g. 102026")
	}
	return t.Format(time.DateOnly), t.AddDate(0, 1, -1).Format(time.DateOnly), nil
}

// fyStart returns the calendar year in which the Indian financial year of an
// ISO date starts (2027-02-10 → 2026), or 0 if the date does not parse.
func fyStart(iso string) int {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return 0
	}
	if t.Month() < time.April {
		return t.Year() - 1
	}
	return t.Year()
}

// gstnToISO turns the portal's dd-mm-yyyy into YYYY-MM-DD ("" if it does not parse).
func gstnToISO(d string) string {
	t, err := time.Parse("02-01-2006", strings.TrimSpace(d))
	if err != nil {
		if t, err = time.Parse(time.DateOnly, strings.TrimSpace(d)); err != nil {
			return ""
		}
	}
	return t.Format(time.DateOnly)
}

// isoToGSTN turns YYYY-MM-DD into dd-mm-yyyy.
func isoToGSTN(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.Format("02-01-2006")
}

// DocKey is the stable, URL-safe identity of a supplier document used by IMS
// actions: supplier GSTIN, document type, normalised number and the FY of
// the document date (suppliers restart numbering every April).
func DocKey(supplierGSTIN, docType, docNo, isoDate string) string {
	fy := "NA"
	if y := fyStart(isoDate); y > 0 {
		fy = fmt.Sprintf("%02d%02d", y%100, (y+1)%100)
	}
	return strings.ToUpper(strings.TrimSpace(supplierGSTIN)) + "_" + docType + "_" + NormaliseDocNo(docNo) + "_" + fy
}
