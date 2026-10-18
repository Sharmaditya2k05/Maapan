// Package mapping knows the target fields of each import kind and suggests
// which spreadsheet column feeds which field, by fuzzy-matching headers such
// as MargBooks' "Party Name", "GSTIN No", "Opening Bal", "Purc Rate".
package mapping

import (
	"sort"
	"strings"
	"unicode"
)

// Import kinds.
const (
	KindParties         = "parties"
	KindItems           = "items"
	KindOpeningBalances = "opening_balances"
	KindOpeningStock    = "opening_stock"
)

// Field is one target field of an import kind.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Aliases  []string `json:"-"`
	Example  string   `json:"example"`
}

// Fields lists the target fields per kind, in template column order.
var Fields = map[string][]Field{
	KindParties: {
		{Key: "name", Label: "Party Name", Required: true, Example: "Sharma General Store",
			Aliases: []string{"partyname", "name", "accountname", "ledgername", "customername", "suppliername", "party", "account", "ledger", "particulars"}},
		{Key: "code", Label: "Party Code", Example: "C0001",
			Aliases: []string{"code", "partycode", "accountcode", "ledgercode", "alias", "acode", "custcode"}},
		{Key: "type", Label: "Group", Example: "Sundry Debtors",
			Aliases: []string{"group", "accountgroup", "undergroup", "under", "partytype", "type", "groupname"}},
		{Key: "phone", Label: "Mobile", Example: "9876543210",
			Aliases: []string{"mobile", "mobileno", "phone", "phoneno", "contact", "contactno", "mob", "mobilenumber", "telephone"}},
		{Key: "gstin", Label: "GSTIN No", Example: "27AAPFU0939F1ZV",
			Aliases: []string{"gstin", "gstinno", "gstno", "gst", "gstnumber", "gstinuin", "gstregno"}},
		{Key: "state", Label: "State", Example: "Maharashtra",
			Aliases: []string{"state", "statename", "statecode", "st"}},
		{Key: "address", Label: "Address", Example: "12 MG Road, Pune 411001",
			Aliases: []string{"address", "address1", "addr", "add1", "add", "fulladdress", "billingaddress"}},
		{Key: "opening_balance", Label: "Opening Bal", Example: "1,200.50 Dr",
			Aliases: []string{"openingbal", "openingbalance", "opbal", "opening", "balance", "opbalance", "opbalamt", "closingbalance"}},
		{Key: "dr_cr", Label: "Dr/Cr", Example: "Dr",
			Aliases: []string{"drcr", "balancetype", "baltype", "crdr", "side"}},
	},
	KindItems: {
		{Key: "name", Label: "Item Name", Required: true, Example: "Parle-G 100g",
			Aliases: []string{"itemname", "productname", "name", "item", "product", "description", "itemdescription", "particulars"}},
		{Key: "code", Label: "Item Code", Example: "I0001",
			Aliases: []string{"code", "itemcode", "productcode", "alias", "icode"}},
		{Key: "sku", Label: "SKU", Example: "PG-100", Aliases: []string{"sku", "skucode", "partno", "partnumber"}},
		{Key: "barcode", Label: "Barcode", Example: "8901719110016",
			Aliases: []string{"barcode", "ean", "upc", "barcodeno", "eancode"}},
		{Key: "hsn", Label: "HSN Code", Example: "1905",
			Aliases: []string{"hsn", "hsncode", "hsnsac", "hsnsaccode", "sac", "hsnno"}},
		{Key: "unit", Label: "Unit", Example: "PCS",
			Aliases: []string{"unit", "uom", "units", "unitname", "primaryunit"}},
		{Key: "sale_price", Label: "Sale Rate", Example: "10.00",
			Aliases: []string{"salerate", "saleprice", "sellingprice", "rate", "srate", "salesrate", "sprice"}},
		{Key: "mrp", Label: "MRP", Example: "10.00", Aliases: []string{"mrp", "maxretailprice"}},
		{Key: "purchase_price", Label: "Purc Rate", Example: "8.50",
			Aliases: []string{"purcrate", "purchaserate", "purchaseprice", "prate", "costprice", "cost", "pprice", "purrate"}},
		{Key: "tax_rate", Label: "GST %", Example: "5",
			Aliases: []string{"gst", "gstrate", "taxrate", "tax", "igst", "igstrate", "gstpercent", "taxpercent"}},
		{Key: "reorder_level", Label: "Reorder Level", Example: "24",
			Aliases: []string{"reorderlevel", "minstock", "minimumstock", "reorder", "minqty", "rol"}},
	},
	KindOpeningBalances: {
		{Key: "account_code", Label: "Account Code", Example: "CASH",
			Aliases: []string{"accountcode", "ledgercode", "acccode", "glcode"}},
		{Key: "party_ref", Label: "Party Code", Example: "C0001",
			Aliases: []string{"partycode", "partyref", "code", "party", "partyname"}},
		{Key: "amount", Label: "Amount", Required: true, Example: "1,200.50",
			Aliases: []string{"amount", "balance", "openingbal", "openingbalance", "opbal", "amt"}},
		{Key: "dr_cr", Label: "Dr/Cr", Example: "Dr", Aliases: []string{"drcr", "balancetype", "baltype", "crdr", "side", "type"}},
	},
	KindOpeningStock: {
		{Key: "item_ref", Label: "Item Code", Example: "I0001",
			Aliases: []string{"itemcode", "code", "productcode", "alias", "icode"}},
		{Key: "item_id", Label: "Item ID", Example: "", Aliases: []string{"itemid"}},
		{Key: "qty", Label: "Qty", Required: true, Example: "120",
			Aliases: []string{"qty", "quantity", "stock", "closingstock", "openingstock", "balqty", "stockqty", "clstock"}},
		{Key: "unit_cost", Label: "Purc Rate", Example: "8.50",
			Aliases: []string{"rate", "cost", "purcrate", "unitcost", "purchaserate", "costprice"}},
		{Key: "batch_no", Label: "Batch", Example: "B2601", Aliases: []string{"batch", "batchno", "lot", "lotno", "batchnumber"}},
		{Key: "expiry_date", Label: "Expiry", Example: "12/27",
			Aliases: []string{"expiry", "exp", "expirydate", "expdate", "expirydate"}},
	},
}

// ValidKind reports whether kind is supported.
func ValidKind(kind string) bool { _, ok := Fields[kind]; return ok }

// Normalize lower-cases and strips everything but letters and digits.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// score rates how well a header matches a field (0..1).
func score(header string, f Field) float64 {
	h := Normalize(header)
	if h == "" {
		return 0
	}
	best := 0.0
	for _, a := range append([]string{Normalize(f.Key), Normalize(f.Label)}, f.Aliases...) {
		var s float64
		switch {
		case h == a:
			s = 1
		case len(a) >= 4 && strings.HasPrefix(h, a), len(h) >= 4 && strings.HasPrefix(a, h):
			s = 0.85
		case len(a) >= 4 && strings.Contains(h, a):
			s = 0.75
		default:
			s = similarity(h, a)
		}
		if s > best {
			best = s
		}
	}
	return best
}

// Suggest maps field keys to header text. Each column feeds at most one field;
// the strongest matches win. Matches below 0.7 are left unmapped.
func Suggest(kind string, headers []string) map[string]string {
	type cand struct {
		field string
		col   int
		s     float64
	}
	var cands []cand
	for _, f := range Fields[kind] {
		for i, h := range headers {
			if s := score(h, f); s >= 0.7 {
				cands = append(cands, cand{f.Key, i, s})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].s > cands[j].s })
	out := map[string]string{}
	usedCol := map[int]bool{}
	for _, c := range cands {
		if _, done := out[c.field]; done || usedCol[c.col] {
			continue
		}
		out[c.field] = headers[c.col]
		usedCol[c.col] = true
	}
	return out
}

// DetectHeaderRow finds the header in the first rows of a sheet: MargBooks
// exports put the company name and report title above it. It returns the
// 1-based row number with the most recognised headers (needs at least 2),
// falling back to the first non-empty row.
func DetectHeaderRow(kind string, rows [][]string) int {
	best, bestN, firstNonEmpty := 0, 1, 0
	for i, r := range rows {
		nonEmpty := 0
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				nonEmpty++
			}
		}
		if nonEmpty == 0 {
			continue
		}
		if firstNonEmpty == 0 {
			firstNonEmpty = i + 1
		}
		if n := len(Suggest(kind, r)); n > bestN {
			best, bestN = i+1, n
		}
	}
	if best == 0 {
		return max(firstNonEmpty, 1)
	}
	return best
}

// Resolve turns a field→header mapping into field→column index for the header row.
// Unknown headers are reported.
func Resolve(m map[string]string, headers []string) (map[string]int, []string) {
	idx := map[string]int{}
	byNorm := map[string]int{}
	for i, h := range headers {
		if n := Normalize(h); n != "" {
			if _, dup := byNorm[n]; !dup {
				byNorm[n] = i
			}
		}
	}
	var missing []string
	for field, h := range m {
		if strings.TrimSpace(h) == "" {
			continue
		}
		if i, ok := byNorm[Normalize(h)]; ok {
			idx[field] = i
		} else {
			missing = append(missing, h)
		}
	}
	sort.Strings(missing)
	return idx, missing
}

// MissingRequired lists required fields absent from the mapping.
func MissingRequired(kind string, m map[string]string) []string {
	var out []string
	for _, f := range Fields[kind] {
		if f.Required && strings.TrimSpace(m[f.Key]) == "" {
			out = append(out, f.Key)
		}
	}
	return out
}

// similarity is 1 - levenshtein/maxLen.
func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}
