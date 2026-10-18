// Package compliance builds statutory JSON payloads from Taraju invoices:
// NIC e-Way Bill (bulk JSON v1.0.0621) and IRP e-Invoice (INV-01 schema v1.1).
// Everything here is pure — no I/O — so it is unit tested directly.
package compliance

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// splitAddress breaks an address into two lines of at most 120 characters (NIC limit).
func splitAddress(a string) (string, string) {
	a = strings.TrimSpace(a)
	if len(a) <= 120 {
		return a, ""
	}
	cut := strings.LastIndex(a[:120], ",")
	if cut < 40 {
		cut = 120
	}
	rest := strings.TrimSpace(strings.TrimPrefix(a[cut:], ","))
	if len(rest) > 120 {
		rest = rest[:120]
	}
	return strings.TrimSpace(a[:cut]), rest
}
