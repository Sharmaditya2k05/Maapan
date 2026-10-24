package returns

import "strings"

// uqcMap maps free-text units to GSTN Unit Quantity Codes. GSTR-1 Table 12
// accepts only the UQC list; "PCS" is not on it, so pieces are NOS.
var uqcMap = map[string]string{
	"PCS": "NOS", "PC": "NOS", "PIECE": "NOS", "PIECES": "NOS", "NOS": "NOS", "NO": "NOS", "EA": "NOS", "EACH": "NOS",
	"UNIT": "UNT", "UNITS": "UNT", "UNT": "UNT",
	"KG": "KGS", "KGS": "KGS", "KILO": "KGS", "KILOGRAM": "KGS",
	"G": "GMS", "GM": "GMS", "GMS": "GMS", "GRAM": "GMS", "GRAMS": "GMS",
	"L": "LTR", "LT": "LTR", "LTR": "LTR", "LITRE": "LTR", "LITER": "LTR",
	"ML": "MLT", "MLT": "MLT", "MILLILITRE": "MLT",
	"M": "MTR", "MTR": "MTR", "METER": "MTR", "METRE": "MTR",
	"CM": "CMS", "CMS": "CMS", "SQM": "SQM", "SQFT": "SQF", "SQF": "SQF",
	"BOX": "BOX", "BAG": "BAG", "BTL": "BTL", "BOTTLE": "BTL", "CAN": "CAN", "TIN": "CAN",
	"PAC": "PAC", "PACK": "PAC", "PKT": "PAC", "PACKET": "PAC",
	"DOZ": "DOZ", "DOZEN": "DOZ", "SET": "SET", "SETS": "SET", "PRS": "PRS", "PAIR": "PRS", "PAIRS": "PRS",
	"ROL": "ROL", "ROLL": "ROL", "TON": "TON", "TONNE": "TON", "MT": "MTS", "QTL": "QTL", "QUINTAL": "QTL",
	"CTN": "CTN", "CARTON": "CTN", "BDL": "BDL", "BUNDLE": "BDL", "TUB": "TUB", "TBS": "TBS", "TABLET": "TBS",
	"BUN": "BUN", "BUNCH": "BUN", "GGK": "GGK", "GRS": "GRS", "GROSS": "GRS", "KLR": "KLR", "KME": "KME", "YDS": "YDS",
	"CBM": "CBM", "CCM": "CCM", "DRM": "DRM", "DRUM": "DRM", "THD": "THD", "TGM": "TGM", "UGS": "UGS", "BKL": "BKL",
}

// UQC maps a unit to its GST Unit Quantity Code ("OTH" when unknown).
func UQC(unit string) string {
	if c, ok := uqcMap[strings.ToUpper(strings.TrimSpace(unit))]; ok {
		return c
	}
	return "OTH"
}
