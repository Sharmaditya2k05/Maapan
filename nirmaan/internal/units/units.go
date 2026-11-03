// Package units converts quantities between a component's unit and the
// item's base (stock) unit, and holds nirmaan's integer money arithmetic.
//
// Conversion order: the base unit itself → the item's alternate units from
// bhandar (1 ALT = factor × BASE) → the standard metric/count table (KG↔GM,
// LTR↔ML, MTR↔CM, DOZ↔PCS …). Anything else is an error, never a silent 1:1.
package units

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

// ItemUnits is what nirmaan knows about one item's units.
type ItemUnits struct {
	Base string             // stock unit in bhandar
	Alts map[string]float64 // alternate unit → base units per 1 alternate
}

// Catalog is ItemUnits keyed by item id.
type Catalog map[string]ItemUnits

// aliases maps common spellings and GST UQC codes onto one canonical code.
var aliases = map[string]string{
	"KGS": "KG", "KILO": "KG", "KILOGRAM": "KG",
	"G": "GM", "GMS": "GM", "GRM": "GM", "GRAM": "GM", "GRAMS": "GM",
	"MGS": "MG",
	"L":   "LTR", "LT": "LTR", "LITRE": "LTR", "LITER": "LTR", "LTRS": "LTR",
	"MLT": "ML", "MLS": "ML",
	"KLR": "KL",
	"M":   "MTR", "MTRS": "MTR", "METER": "MTR", "METRE": "MTR",
	"CMS": "CM", "MMS": "MM", "KME": "KM",
	"NOS": "PCS", "PC": "PCS", "PIECE": "PCS", "PIECES": "PCS", "UNT": "PCS", "UNITS": "PCS",
	"DZN": "DOZ", "GRS": "GROSS",
	"MT": "TON", "TONNE": "TON", "TONS": "TON", "QTLS": "QTL",
}

// standard: canonical unit → (dimension, size in the dimension's smallest unit).
var standard = map[string]struct {
	dim  string
	size float64
}{
	"MG": {"mass", 0.001}, "GM": {"mass", 1}, "KG": {"mass", 1000}, "QTL": {"mass", 100000}, "TON": {"mass", 1000000},
	"ML": {"volume", 1}, "LTR": {"volume", 1000}, "KL": {"volume", 1000000},
	"MM": {"length", 1}, "CM": {"length", 10}, "MTR": {"length", 1000}, "KM": {"length", 1000000},
	"PCS": {"count", 1}, "DOZ": {"count", 12}, "GROSS": {"count", 144},
}

// Norm upper-cases, trims and canonicalises a unit code ("" stays "").
func Norm(u string) string {
	u = strings.ToUpper(strings.TrimSpace(u))
	if a, ok := aliases[u]; ok {
		return a
	}
	return u
}

// Factor returns how many base units one `unit` is for an item.
func Factor(unit string, iu ItemUnits) (float64, error) {
	u, base := Norm(unit), Norm(iu.Base)
	if u == "" || base == "" || u == base {
		return 1, nil
	}
	for alt, f := range iu.Alts {
		if Norm(alt) == u && f > 0 {
			return f, nil
		}
	}
	su, ok1 := standard[u]
	sb, ok2 := standard[base]
	if ok1 && ok2 && su.dim == sb.dim {
		return su.size / sb.size, nil
	}
	// unit is a standard multiple of one of the item's alternates (e.g. DOZ of BOX is not, but GM of a 1-KG PKT is)
	if ok1 {
		for alt, f := range iu.Alts {
			if sa, ok := standard[Norm(alt)]; ok && sa.dim == su.dim && f > 0 {
				return su.size / sa.size * f, nil
			}
		}
	}
	return 0, fmt.Errorf("cannot convert %s to the item's base unit %s; add %s as an alternate unit in bhandar", u, base, u)
}

// FactorFor looks the item up in c; an item c does not know is taken to be
// counted in its own unit (factor 1) so pure costing without bhandar still works.
func (c Catalog) FactorFor(itemID, unit string) (float64, string, error) {
	iu, ok := c[itemID]
	if !ok {
		return 1, Norm(unit), nil
	}
	f, err := Factor(unit, iu)
	return f, Norm(iu.Base), err
}

// UQC maps a unit onto the GST Unit Quantity Code used in returns (ITC-04).
func UQC(unit string) string {
	switch Norm(unit) {
	case "KG":
		return "KGS"
	case "GM":
		return "GMS"
	case "ML":
		return "MLT"
	case "LTR":
		return "LTR"
	case "KL":
		return "KLR"
	case "MTR":
		return "MTR"
	case "CM":
		return "CMS"
	case "KM":
		return "KME"
	case "PCS":
		return "PCS"
	case "DOZ":
		return "DOZ"
	case "GROSS":
		return "GRS"
	case "TON":
		return "TON"
	case "QTL":
		return "QTL"
	case "BOX":
		return "BOX"
	case "NOS":
		return "NOS"
	case "BAG", "BAL", "BDL", "BKL", "BOU", "BTL", "BUN", "CAN", "CBM", "CCM", "CTN", "DRM", "GGK", "GYD", "KLR", "MLT", "PAC", "PRS", "ROL", "SET", "SQF", "SQM", "SQY", "TBS", "TGM", "THD", "TUB", "UGS", "UNT", "YDS":
		return Norm(unit)
	}
	return "OTH"
}

// Milli turns a quantity into thousandths (all quantities are kept to 3 decimals).
func Milli(q float64) int64 { return int64(math.Round(q * 1000)) }

// FromMilli is the inverse of Milli.
func FromMilli(m int64) float64 { return float64(m) / 1000 }

// Round3 rounds a quantity to 3 decimals.
func Round3(q float64) float64 { return math.Round(q*1000) / 1000 }

// MulDiv returns round(a×b÷c), half away from zero, exactly (128-bit safe).
func MulDiv(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	d := big.NewInt(c)
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	r2 := new(big.Int).Abs(r)
	r2.Mul(r2, big.NewInt(2))
	if r2.Cmp(new(big.Int).Abs(d)) >= 0 {
		if (n.Sign() < 0) != (d.Sign() < 0) {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.Int64()
}

// Extend is the value in paise of qty (any unit) at ratePaise per unit.
func Extend(qty float64, ratePaise int64) int64 { return MulDiv(Milli(qty), ratePaise, 1000) }

// Rate is total paise ÷ qty, rounded to a paisa (0 when qty is 0).
func Rate(totalPaise int64, qty float64) int64 {
	m := Milli(qty)
	if m == 0 {
		return 0
	}
	return MulDiv(totalPaise, 1000, m)
}
