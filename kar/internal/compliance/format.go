// Package compliance builds statutory JSON payloads from Maapan invoices —
// NIC e-Way Bill (v1.0.0621) and IRP e-Invoice (INV-01 schema v1.1) — and runs
// their lifecycle against a gsp.Client (Engine: generate, cancel, Part-B,
// extend, events and outbox). The builders and rule tables (format.go,
// einvoice.go, eway.go, rules.go) are pure and unit tested directly; engine*.go
// does the I/O.
package compliance

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maapan/mool/gst"
)

// Party is one side of a movement of goods (seller/buyer, consignor/consignee).
type Party struct {
	GSTIN     string `json:"gstin"`
	LegalName string `json:"legal_name"`
	TradeName string `json:"trade_name"`
	Address1  string `json:"address1"`
	Address2  string `json:"address2"`
	Place     string `json:"place"`
	Pincode   int    `json:"pincode"`
	StateCode string `json:"state_code"`
	Phone     string `json:"phone"`
}

// Rupees converts paise to a 2-decimal rupee number for statutory JSON.
func Rupees(p int64) float64 { return float64(p) / 100 }

// round to n decimals (avoids 0.1+0.2 artefacts in JSON).
func round(v float64, n int) float64 {
	f := math.Pow(10, float64(n))
	return math.Round(v*f) / f
}

// RateFromBP converts basis points to a percentage (1800 → 18).
func RateFromBP(bp int) float64 { return float64(bp) / 100 }

// NICDate turns YYYY-MM-DD into dd/mm/yyyy (NIC and IRP format).
func NICDate(iso string) (string, error) {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return "", err
	}
	return t.Format("02/01/2006"), nil
}

// GSTNDate turns YYYY-MM-DD into dd-mm-yyyy (GSTR offline-tool format).
func GSTNDate(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return t.Format("02-01-2006")
}

var uqc = map[string]string{
	"PCS": "PCS", "PC": "PCS", "PIECE": "PCS", "NOS": "NOS", "NO": "NOS", "UNIT": "UNT", "UNT": "UNT",
	"KG": "KGS", "KGS": "KGS", "KILOGRAM": "KGS", "G": "GMS", "GM": "GMS", "GMS": "GMS", "GRAM": "GMS",
	"L": "LTR", "LT": "LTR", "LTR": "LTR", "LITRE": "LTR", "ML": "MLT", "MLT": "MLT",
	"M": "MTR", "MTR": "MTR", "METER": "MTR", "BOX": "BOX", "BAG": "BAG", "BTL": "BTL", "BOTTLE": "BTL",
	"PAC": "PAC", "PACK": "PAC", "PKT": "PAC", "DOZ": "DOZ", "DOZEN": "DOZ", "SET": "SET", "PRS": "PRS",
	"PAIR": "PRS", "ROL": "ROL", "ROLL": "ROL", "TON": "TON", "QTL": "QTL", "CTN": "CTN", "CARTON": "CTN",
}

// UQC maps a free-text unit to the GST Unit Quantity Code ("OTH" if unknown).
func UQC(unit string) string {
	if c, ok := uqc[strings.ToUpper(strings.TrimSpace(unit))]; ok {
		return c
	}
	return "OTH"
}

var pinPattern = regexp.MustCompile(`\b[1-9][0-9]{5}\b`)

// PincodeFromAddress finds a 6-digit Indian PIN code in free text (0 if none).
func PincodeFromAddress(addr string) int {
	m := pinPattern.FindAllString(addr, -1)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(m[len(m)-1])
	return n
}

// IsService reports whether an HSN/SAC code is a service (SAC chapter 99).
func IsService(hsn string) bool { return strings.HasPrefix(strings.TrimSpace(hsn), "99") }

func stateInt(code string) int { n, _ := strconv.Atoi(code); return n }

func hsnInt(code string) int { n, _ := strconv.Atoi(strings.TrimSpace(code)); return n }

// AddrLimit is the IRP / NIC limit for one address line.
const AddrLimit = 100

// Truncate cuts s to at most n characters (runes, not bytes), so Devanagari
// and other multi-byte text is never split mid-character.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}

func truncate(s string, n int) string { return Truncate(s, n) }

// splitAddress breaks an address into two lines of at most AddrLimit
// characters, preferring a comma near the limit; anything beyond two lines
// is cut. Lengths are counted in characters.
func splitAddress(a string) (string, string) {
	a = strings.Join(strings.Fields(a), " ")
	r := []rune(a)
	if len(r) <= AddrLimit {
		return a, ""
	}
	cut := -1
	for i := AddrLimit; i > AddrLimit*2/5; i-- {
		if r[i] == ',' {
			cut = i
			break
		}
	}
	if cut < 0 {
		for i := AddrLimit; i > AddrLimit*2/5; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
	}
	if cut < 0 {
		cut = AddrLimit
	}
	first := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(r[:cut])), ","))
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(r[cut:])), ","))
	return Truncate(first, AddrLimit), Truncate(rest, AddrLimit)
}

var nonDigit = regexp.MustCompile(`[^0-9]`)

// Phone returns the digits of a phone number in the IRP's 6-12 digit form:
// "+91 98765-43210" → "9876543210". Anything that cannot be made 6-12 digits
// is dropped (the field is optional) rather than rejected by the IRP.
func Phone(p string) string {
	d := nonDigit.ReplaceAllString(p, "")
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	case len(d) == 13 && strings.HasPrefix(d, "091"):
		d = d[3:]
	}
	if len(d) < 6 || len(d) > 12 {
		return ""
	}
	return d
}

var trailingPin = regexp.MustCompile(`[\s-]*[1-9][0-9]{5}$`)

// PlaceFromAddress guesses the city: the last comma-separated part that is
// not a PIN code or a state name, with a trailing PIN code removed. Other
// numbers stay ("Sector 15" is a place, not "Sector").
func PlaceFromAddress(addr string) string {
	parts := strings.Split(addr, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p == "" || pinOnly.MatchString(p) || isStateName(p) {
			continue
		}
		return strings.TrimSpace(trailingPin.ReplaceAllString(p, ""))
	}
	return ""
}

var pinOnly = regexp.MustCompile(`^(PIN|Pin|pin)?[\s:-]*[1-9][0-9]{5}$`)

func isStateName(s string) bool {
	for _, n := range gst.States {
		if strings.EqualFold(n, s) {
			return true
		}
	}
	return false
}
