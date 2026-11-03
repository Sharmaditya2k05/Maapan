package scan

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/maapan/mool/money"
)

// MaxAlternatives is how many runner-up items a match offers the picker.
const MaxAlternatives = 3

// minAlternativeScore keeps obviously unrelated items out of the picker.
const minAlternativeScore = 0.3

// MatchInput is a billed line to match without a scan (POST /v1/scan/match-items),
// e.g. after a person corrected a line name in the draft.
type MatchInput struct {
	Name             string  `json:"name"`
	HSN              string  `json:"hsn"`
	Unit             string  `json:"unit"`
	Rate             float64 `json:"rate"` // rupees per unit as billed
	GSTRatePercent   float64 `json:"gst_rate_percent"`
	PricesIncludeTax bool    `json:"prices_include_tax"`
}

// MatchLines matches free-standing lines against the item master.
func MatchLines(lines []MatchInput, catalog []CatalogItem) []LineMatch {
	out := make([]LineMatch, 0, len(lines))
	for i, l := range lines {
		m, _ := matchLine(i+1, ExtractedLine{Name: l.Name, HSN: l.HSN, Unit: l.Unit, Rate: l.Rate, GSTRatePercent: l.GSTRatePercent},
			l.PricesIncludeTax, catalog)
		out = append(out, m)
	}
	return out
}

// Shortlist returns the item ids worth looking up in full (HSN, unit, price,
// rate) for these billed names: the best few name matches per line. Barcodes
// returns digit runs printed on the lines that may be EAN/UPC codes.
func Shortlist(names []string, catalog []CatalogItem, perLine int) (ids, barcodes []string) {
	seen := map[string]bool{}
	seenCode := map[string]bool{}
	for _, name := range names {
		n := normalize(name)
		type sc struct {
			id string
			s  float64
		}
		var scored []sc
		for _, c := range catalog {
			if s := Similarity(n, normalize(c.Name)); s >= minAlternativeScore {
				scored = append(scored, sc{c.ItemID, s})
			}
		}
		sort.SliceStable(scored, func(i, j int) bool { return scored[i].s > scored[j].s })
		for i := 0; i < len(scored) && i < perLine; i++ {
			if !seen[scored[i].id] {
				seen[scored[i].id] = true
				ids = append(ids, scored[i].id)
			}
		}
		for _, tok := range strings.Fields(name) {
			if isBarcode(tok) && !seenCode[tok] {
				seenCode[tok] = true
				barcodes = append(barcodes, tok)
			}
		}
	}
	return ids, barcodes
}

func isBarcode(s string) bool {
	if len(s) < 8 || len(s) > 14 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Merge overlays enriched items onto the catalog (by id) and appends any that
// were only found by barcode.
func Merge(catalog, enriched []CatalogItem) []CatalogItem {
	byID := map[string]int{}
	out := make([]CatalogItem, len(catalog))
	copy(out, catalog)
	for i, c := range out {
		byID[c.ItemID] = i
	}
	for _, e := range enriched {
		e.Enriched = true
		if i, ok := byID[e.ItemID]; ok {
			if e.Name == "" {
				e.Name = out[i].Name
			}
			out[i] = e
			continue
		}
		byID[e.ItemID] = len(out)
		out = append(out, e)
	}
	return out
}

// matchLine scores every catalog item for one billed line. Name similarity is
// the base; a SKU/barcode printed on the line is decisive; for items with full
// master data, HSN, unit and last purchase price nudge the score.
func matchLine(lineNo int, l ExtractedLine, inclTax bool, catalog []CatalogItem) (LineMatch, *CatalogItem) {
	name := strings.TrimSpace(l.Name)
	m := LineMatch{LineNo: lineNo, BilledName: name, Method: "none", Alternatives: []Candidate{}}
	if name == "" || len(catalog) == 0 {
		return m, nil
	}
	n := normalize(name)
	tokens := map[string]bool{}
	for _, t := range strings.Fields(strings.ToUpper(name)) {
		tokens[strings.Trim(t, ".,;:()[]#")] = true
	}
	rate := money.FromRupees(l.Rate)
	cands := make([]Candidate, 0, len(catalog))
	for _, c := range catalog {
		if (c.SKU != "" && tokens[strings.ToUpper(c.SKU)]) || (c.Barcode != "" && tokens[c.Barcode]) {
			cands = append(cands, Candidate{ItemID: c.ItemID, Name: c.Name, Score: 1, Reasons: []string{"SKU/barcode printed on the bill"}, item: c})
			continue
		}
		s, reasons := attributeScore(Similarity(n, normalize(c.Name)), l, rate, inclTax, c)
		cands = append(cands, Candidate{ItemID: c.ItemID, Name: c.Name, Score: round2(s), Reasons: reasons, item: c})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	best := cands[0]
	m.Score = best.Score
	var matched *CatalogItem
	if best.Score >= MatchThreshold {
		it := best.item
		matched = &it
		m.ItemID, m.MatchedName, m.Reasons = best.ItemID, best.Name, best.Reasons
		m.Method = "name"
		if best.Score == 1 && len(best.Reasons) > 0 && strings.HasPrefix(best.Reasons[0], "SKU") {
			m.Method = "identifier"
		}
		cands = cands[1:]
	}
	for _, c := range cands {
		if len(m.Alternatives) == MaxAlternatives || c.Score < minAlternativeScore {
			break
		}
		m.Alternatives = append(m.Alternatives, c)
	}
	return m, matched
}

func attributeScore(base float64, l ExtractedLine, ratePaise int64, inclTax bool, c CatalogItem) (float64, []string) {
	if !c.Enriched {
		return base, nil
	}
	s := base
	var reasons []string
	hsn := digits(l.HSN)
	if mh := digits(c.HSN); len(hsn) >= 4 && len(mh) >= 4 {
		if hsn[:4] == mh[:4] {
			s += 0.1
			reasons = append(reasons, "same HSN "+mh[:4])
		} else {
			s -= 0.15
			reasons = append(reasons, fmt.Sprintf("HSN differs (bill %s, item %s)", hsn, mh))
		}
	}
	if u := strings.TrimSpace(l.Unit); u != "" && c.Unit != "" && strings.EqualFold(u, c.Unit) {
		s += 0.05
		reasons = append(reasons, "same unit")
	}
	if !inclTax && ratePaise > 0 && c.PurchasePricePaise > 0 {
		diff := math.Abs(float64(ratePaise-c.PurchasePricePaise)) / float64(c.PurchasePricePaise)
		switch {
		case diff <= 0.15:
			s += 0.05
			reasons = append(reasons, "rate close to last purchase price")
		case diff >= 0.5:
			s -= 0.05
			reasons = append(reasons, "rate far from last purchase price")
		}
	}
	return math.Min(1, math.Max(0, s)), reasons
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
