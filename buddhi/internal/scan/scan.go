// Package scan turns a photo/PDF of a supplier's bill into a *draft* purchase
// bill for aadesh. The model only reads the document; every number is
// re-checked here (GSTIN checksum, line sums, GST maths) and items are matched
// to the tenant's catalog locally. Nothing is ever posted automatically.
package scan

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/money"
)

// System is the extraction instruction (stable text, prompt-cached).
const System = `You read Indian GST purchase invoices (tax invoices / bills of supply) from photos or PDFs and transcribe them exactly.

Rules:
- Transcribe what is printed. Never invent values. Use "" for text you cannot read and 0 for numbers you cannot read, and lower "confidence" accordingly.
- Amounts are in rupees as printed (e.g. 1234.50). Quantities are numbers, without units.
- rate is the per-unit price as printed on the line; set prices_include_tax=true only if the line rates clearly include GST (MRP-style bills).
- line amount is the line's taxable value (after discount, before GST) when the bill shows it.
- gst_rate_percent is the combined GST rate for the line (CGST+SGST, or IGST), e.g. 18.
- Dates as YYYY-MM-DD. GSTINs are 15 characters, upper case.
- Include every item line in order; skip sub-total, tax and round-off rows.
- confidence is your overall confidence (0..1) that the transcription is complete and correct.`

// Prompt accompanies the attachment.
const Prompt = "Transcribe this supplier bill into the JSON schema."

// Schema is the structured-output JSON schema for an extracted bill.
var Schema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required": []string{"supplier_name", "supplier_gstin", "invoice_number", "invoice_date", "lines",
		"taxable_total", "cgst_total", "sgst_total", "igst_total", "grand_total", "prices_include_tax", "confidence", "notes"},
	"properties": map[string]any{
		"supplier_name":  map[string]any{"type": "string"},
		"supplier_gstin": map[string]any{"type": "string"},
		"invoice_number": map[string]any{"type": "string"},
		"invoice_date":   map[string]any{"type": "string", "description": "YYYY-MM-DD or empty"},
		"lines": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name", "hsn", "qty", "unit", "rate", "discount", "gst_rate_percent", "amount"},
				"properties": map[string]any{
					"name":             map[string]any{"type": "string"},
					"hsn":              map[string]any{"type": "string"},
					"qty":              map[string]any{"type": "number"},
					"unit":             map[string]any{"type": "string"},
					"rate":             map[string]any{"type": "number"},
					"discount":         map[string]any{"type": "number", "description": "line discount in rupees"},
					"gst_rate_percent": map[string]any{"type": "number"},
					"amount":           map[string]any{"type": "number", "description": "taxable value of the line in rupees"},
				},
			},
		},
		"taxable_total":      map[string]any{"type": "number"},
		"cgst_total":         map[string]any{"type": "number"},
		"sgst_total":         map[string]any{"type": "number"},
		"igst_total":         map[string]any{"type": "number"},
		"grand_total":        map[string]any{"type": "number"},
		"prices_include_tax": map[string]any{"type": "boolean"},
		"confidence":         map[string]any{"type": "number"},
		"notes":              map[string]any{"type": "string"},
	},
}

// Extracted is the model's transcription (rupees, as printed).
type Extracted struct {
	SupplierName     string          `json:"supplier_name"`
	SupplierGSTIN    string          `json:"supplier_gstin"`
	InvoiceNumber    string          `json:"invoice_number"`
	InvoiceDate      string          `json:"invoice_date"`
	Lines            []ExtractedLine `json:"lines"`
	TaxableTotal     float64         `json:"taxable_total"`
	CGSTTotal        float64         `json:"cgst_total"`
	SGSTTotal        float64         `json:"sgst_total"`
	IGSTTotal        float64         `json:"igst_total"`
	GrandTotal       float64         `json:"grand_total"`
	PricesIncludeTax bool            `json:"prices_include_tax"`
	Confidence       float64         `json:"confidence"`
	Notes            string          `json:"notes"`
}

// ExtractedLine is one transcribed line.
type ExtractedLine struct {
	Name           string  `json:"name"`
	HSN            string  `json:"hsn"`
	Qty            float64 `json:"qty"`
	Unit           string  `json:"unit"`
	Rate           float64 `json:"rate"`
	Discount       float64 `json:"discount"`
	GSTRatePercent float64 `json:"gst_rate_percent"`
	Amount         float64 `json:"amount"`
}

// CatalogItem is an item from bhandar's stock summary.
type CatalogItem struct {
	ItemID string `json:"item_id"`
	Name   string `json:"name"`
}

// DraftLine matches aadesh CreateInput.lines (plus match info the UI shows).
type DraftLine struct {
	ItemID           string  `json:"item_id,omitempty"`
	Name             string  `json:"name"`
	Unit             string  `json:"unit,omitempty"`
	HSN              string  `json:"hsn,omitempty"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"`
	TaxRateBP        int     `json:"tax_rate_bp"`
	PriceIncludesTax bool    `json:"price_includes_tax"`
}

// LineMatch explains how a draft line was matched to the catalog.
type LineMatch struct {
	LineNo      int     `json:"line_no"`
	BilledName  string  `json:"billed_name"`
	MatchedName string  `json:"matched_name,omitempty"`
	Score       float64 `json:"score"`
}

// DraftBody is the body to send to aadesh POST /v1/invoices once a person has
// reviewed it and picked/created the supplier party.
type DraftBody struct {
	DocType           string      `json:"doc_type"`
	PartyID           string      `json:"party_id"` // empty: the UI must select or create the supplier
	SupplierInvoiceNo string      `json:"supplier_invoice_no,omitempty"`
	InvoiceDate       string      `json:"invoice_date,omitempty"`
	Lines             []DraftLine `json:"lines"`
	Notes             string      `json:"notes,omitempty"`
}

// Supplier is what the bill says about the supplier (for party lookup/creation).
type Supplier struct {
	Name       string `json:"name"`
	GSTIN      string `json:"gstin,omitempty"`
	StateCode  string `json:"state_code,omitempty"`
	GSTINValid bool   `json:"gstin_valid"`
}

// Result is the API response of POST /v1/scan/purchase-bill.
type Result struct {
	Draft           DraftBody   `json:"draft"`
	Supplier        Supplier    `json:"supplier"`
	Matches         []LineMatch `json:"matches"`
	Confidence      float64     `json:"confidence"`
	Warnings        []string    `json:"warnings"`
	ComputedGrand   int64       `json:"computed_grand_total_paise"`
	PrintedGrand    int64       `json:"printed_grand_total_paise"`
	ModelConfidence float64     `json:"model_confidence"`
}

// MatchThreshold is the minimum similarity for auto-linking an item.
const MatchThreshold = 0.6

// Build validates the extraction and produces the draft. storeState is the
// buyer store's GST state code ("" if unknown — GST cross-check is skipped).
func Build(ext Extracted, catalog []CatalogItem, storeState string) Result {
	r := Result{ModelConfidence: clamp01(ext.Confidence), Warnings: []string{}}
	r.Supplier = Supplier{Name: strings.TrimSpace(ext.SupplierName), GSTIN: gst.Normalize(ext.SupplierGSTIN)}
	if r.Supplier.GSTIN != "" {
		if err := gst.ValidateGSTIN(r.Supplier.GSTIN); err != nil {
			r.Warnings = append(r.Warnings, "Supplier GSTIN looks wrong: "+err.Error())
		} else {
			r.Supplier.GSTINValid = true
			r.Supplier.StateCode = gst.StateCode(r.Supplier.GSTIN)
		}
	} else {
		r.Warnings = append(r.Warnings, "No supplier GSTIN found — input tax credit needs it.")
	}

	r.Draft = DraftBody{DocType: "purchase", SupplierInvoiceNo: strings.TrimSpace(ext.InvoiceNumber), Notes: "Scanned bill (AI draft)"}
	if d := strings.TrimSpace(ext.InvoiceDate); d != "" {
		if _, err := time.Parse(time.DateOnly, d); err == nil {
			r.Draft.InvoiceDate = d
		} else {
			r.Warnings = append(r.Warnings, "Invoice date unreadable: "+d)
		}
	}
	if r.Draft.SupplierInvoiceNo == "" {
		r.Warnings = append(r.Warnings, "Supplier invoice number not found.")
	}
	if len(ext.Lines) == 0 {
		r.Warnings = append(r.Warnings, "No item lines found.")
	}

	var lineSum float64
	gstLines := make([]gst.Line, 0, len(ext.Lines))
	for i, l := range ext.Lines {
		bp := int(math.Round(l.GSTRatePercent * 100))
		if !slices.Contains(gst.StandardRatesBP, bp) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Line %d: GST rate %.2f%% is not a standard slab.", i+1, l.GSTRatePercent))
		}
		if l.Qty <= 0 {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Line %d: quantity missing.", i+1))
		}
		dl := DraftLine{
			Name: strings.TrimSpace(l.Name), Unit: strings.ToUpper(strings.TrimSpace(l.Unit)), HSN: strings.TrimSpace(l.HSN),
			Qty: l.Qty, UnitPricePaise: money.FromRupees(l.Rate), DiscountPaise: money.FromRupees(l.Discount),
			TaxRateBP: bp, PriceIncludesTax: ext.PricesIncludeTax,
		}
		m := LineMatch{LineNo: i + 1, BilledName: dl.Name}
		if it, score := BestMatch(dl.Name, catalog); score >= MatchThreshold {
			dl.ItemID, m.MatchedName, m.Score = it.ItemID, it.Name, round2(score)
		} else {
			m.Score = round2(score)
			r.Warnings = append(r.Warnings, fmt.Sprintf("Line %d: %q is not in your item list — pick or create the item.", i+1, dl.Name))
		}
		r.Draft.Lines = append(r.Draft.Lines, dl)
		r.Matches = append(r.Matches, m)
		lineSum += l.Amount
		gstLines = append(gstLines, gst.Line{HSN: dl.HSN, Qty: max(dl.Qty, 0.001), UnitPricePaise: dl.UnitPricePaise,
			DiscountPaise: dl.DiscountPaise, TaxRateBP: max(bp, 0), PriceIncludesTax: dl.PriceIncludesTax})
	}

	r.PrintedGrand = money.FromRupees(ext.GrandTotal)
	if ext.TaxableTotal > 0 && math.Abs(lineSum-ext.TaxableTotal) > 1 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("Line amounts add up to %s but the bill's taxable total is %s.",
			money.Format(money.FromRupees(lineSum)), money.Format(money.FromRupees(ext.TaxableTotal))))
	}
	printedTax := ext.CGSTTotal + ext.SGSTTotal + ext.IGSTTotal
	if ext.GrandTotal > 0 && ext.TaxableTotal > 0 && math.Abs(ext.TaxableTotal+printedTax-ext.GrandTotal) > 2 {
		r.Warnings = append(r.Warnings, "Taxable total + GST does not match the printed grand total (beyond round-off).")
	}
	if len(gstLines) > 0 {
		supplierState := r.Supplier.StateCode
		if supplierState == "" {
			supplierState = storeState
		}
		if storeState != "" && gst.ValidState(storeState) && supplierState != "" {
			if sum, err := gst.Compute(gstLines, supplierState, storeState, true); err == nil {
				r.ComputedGrand = sum.GrandPaise
				if r.PrintedGrand > 0 && abs64(sum.GrandPaise-r.PrintedGrand) > 200 {
					r.Warnings = append(r.Warnings, fmt.Sprintf("Recomputed total %s differs from printed %s — check rates and quantities.",
						money.Format(sum.GrandPaise), money.Format(r.PrintedGrand)))
				}
				if (sum.IGSTPaise > 0) != (ext.IGSTTotal > 0) && printedTax > 0 {
					r.Warnings = append(r.Warnings, "Bill charges IGST/CGST+SGST differently from what the supplier's state implies.")
				}
			}
		}
	}

	// Each warning costs 7.5 points of confidence, floor at 10%.
	r.Confidence = round2(math.Max(0.1, r.ModelConfidence-0.075*float64(len(r.Warnings))))
	if len(ext.Lines) == 0 {
		r.Confidence = 0
	}
	return r
}

// BestMatch returns the catalog item most similar to name.
func BestMatch(name string, catalog []CatalogItem) (CatalogItem, float64) {
	var best CatalogItem
	bestScore := 0.0
	n := normalize(name)
	if n == "" {
		return best, 0
	}
	for _, c := range catalog {
		if s := Similarity(n, normalize(c.Name)); s > bestScore {
			best, bestScore = c, s
		}
	}
	return best, bestScore
}

// Similarity blends token overlap (robust to word order: "Salt Tata 1kg") with
// edit-distance ratio (robust to OCR typos: "Parle-G" vs "Parle G").
func Similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ta, tb := strings.Fields(a), strings.Fields(b)
	counts := map[string]int{}
	for _, t := range tb {
		counts[t]++
	}
	inter := 0
	for _, t := range ta {
		if counts[t] > 0 {
			inter++
			counts[t]--
		}
	}
	union := len(ta) + len(tb) - inter
	// Billed names are often shorter than catalog names ("Parle G 100g" vs
	// "Parle-G Biscuit 100g"), so containment counts as much as Jaccard.
	jacc := 0.5*float64(inter)/float64(union) + 0.5*float64(inter)/float64(min(len(ta), len(tb)))
	// Edit distance on word-sorted strings so word order doesn't count as typos.
	sa, sb := sortedJoin(ta), sortedJoin(tb)
	lev := 1 - float64(levenshtein(sa, sb))/float64(max(len([]rune(sa)), len([]rune(sb))))
	return 0.5*jacc + 0.5*lev
}

// normalize lower-cases, drops punctuation and splits digits from letters
// ("1kg" -> "1 kg") so pack sizes compare equal however they are printed.
func normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := ' '
	for _, r := range s {
		isDigit := r >= '0' && r <= '9'
		isWord := (r >= 'a' && r <= 'z') || r > 127
		switch {
		case isDigit && (prev >= 'a' && prev <= 'z' || prev > 127):
			b.WriteRune(' ')
		case isWord && prev >= '0' && prev <= '9':
			b.WriteRune(' ')
		}
		if isDigit || isWord {
			b.WriteRune(r)
			prev = r
		} else {
			b.WriteRune(' ')
			prev = ' '
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func sortedJoin(tokens []string) string {
	c := slices.Clone(tokens)
	slices.Sort(c)
	return strings.Join(c, " ")
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
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
	return prev[len(rb)]
}

func clamp01(x float64) float64 { return math.Min(1, math.Max(0, x)) }
func round2(x float64) float64  { return math.Round(x*100) / 100 }
func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
