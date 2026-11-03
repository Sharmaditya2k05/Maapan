// Package gstinsight turns kar's GSTR-2B vs purchase-books reconciliation into
// an owner-readable insight: mismatched bills, suppliers who have not reported
// our purchases (missing in 2B), documents we have not booked, and the input
// tax credit (ITC) at risk. Pure Go, no LLM; the numbers come from kar.
package gstinsight

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/mool/money"
)

// Paise reads kar's GSTN-style rupee amounts (123.45, or "123.45") as paise.
type Paise int64

// UnmarshalJSON accepts a JSON number or numeric string in rupees.
func (p *Paise) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*p = 0
		return nil
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		frac = frac[:2] // GSTN amounts carry two decimals
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil && whole != "" {
		return fmt.Errorf("amount %q: %w", string(b), err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return fmt.Errorf("amount %q: %w", string(b), err)
	}
	v := w*100 + f
	if neg {
		v = -v
	}
	*p = Paise(v)
	return nil
}

// Heads is tax by head.
type Heads struct {
	IGST Paise `json:"igst"`
	CGST Paise `json:"cgst"`
	SGST Paise `json:"sgst"`
	Cess Paise `json:"cess"`
}

// Total is all heads together.
func (h Heads) Total() int64 { return int64(h.IGST + h.CGST + h.SGST + h.Cess) }

// Doc is a supplier-reported 2B document (subset of kar's recon.Doc).
type Doc struct {
	Key           string `json:"doc_key"`
	SupplierGSTIN string `json:"supplier_gstin"`
	SupplierName  string `json:"supplier_name"`
	DocType       string `json:"doc_type"` // INV | CN | DN
	DocNo         string `json:"doc_no"`
	DocDate       string `json:"doc_date"`
	Taxable       Paise  `json:"taxable"`
	ITCAvailable  string `json:"itc_available"`
	Heads
}

// Purchase is a purchase bill in the books (subset of kar's recon.Purchase).
type Purchase struct {
	ID                string `json:"id"`
	Number            string `json:"number"`
	InvoiceDate       string `json:"invoice_date"`
	SupplierGSTIN     string `json:"supplier_gstin"`
	SupplierName      string `json:"supplier_name"`
	SupplierInvoiceNo string `json:"supplier_invoice_no,omitempty"`
	Taxable           Paise  `json:"taxable"`
	Heads
}

// Diff is one field that differs.
type Diff struct {
	Field  string `json:"field"`
	Books  any    `json:"books"`
	GSTR2B any    `json:"gstr2b"`
}

// Result is one reconciled document.
type Result struct {
	Status   string    `json:"status"` // matched | mismatch | missing_in_books | missing_in_2b
	DocKey   string    `json:"doc_key,omitempty"`
	Doc      *Doc      `json:"doc,omitempty"`
	Purchase *Purchase `json:"purchase,omitempty"`
	Diffs    []Diff    `json:"diffs,omitempty"`
}

// Bucket is a count and its tax.
type Bucket struct {
	Docs int `json:"docs"`
	Heads
}

// ITC is kar's ITC summary by IMS state.
type ITC struct {
	Accepted       Bucket `json:"accepted"`
	DeemedAccepted Bucket `json:"deemed_accepted"`
	Pending        Bucket `json:"pending"`
	Rejected       Bucket `json:"rejected"`
	NotAvailable   Bucket `json:"not_available"`
	Available      Heads  `json:"available"`
}

// Reconciliation is kar's GET /v1/returns/gstr2b response.
type Reconciliation struct {
	GSTIN   string          `json:"gstin"`
	Period  string          `json:"period"`
	Import  json.RawMessage `json:"import"`
	Counts  map[string]int  `json:"counts"`
	ITC     ITC             `json:"itc"`
	Items   []Result        `json:"items"`
	Warning string          `json:"warning,omitempty"`
}

// Supplier groups one supplier's problem documents.
type Supplier struct {
	GSTIN        string `json:"gstin"`
	Name         string `json:"name"`
	Docs         int    `json:"docs"`
	TaxablePaise int64  `json:"taxable_paise"`
	TaxPaise     int64  `json:"tax_paise"`
}

// Mismatch is one bill that differs between books and 2B.
type Mismatch struct {
	SupplierGSTIN    string   `json:"supplier_gstin"`
	SupplierName     string   `json:"supplier_name"`
	DocNo            string   `json:"doc_no"`
	DocDate          string   `json:"doc_date"`
	BooksNumber      string   `json:"books_number,omitempty"`
	Fields           []string `json:"fields"`
	BooksTaxPaise    int64    `json:"books_tax_paise"`
	GSTR2BTaxPaise   int64    `json:"gstr2b_tax_paise"`
	ExcessClaimPaise int64    `json:"excess_claim_paise"` // books tax above 2B tax (ITC at risk)
}

// Card is the dashboard KPI card.
type Card struct {
	Title    string `json:"title"`
	Value    string `json:"value"`
	Severity string `json:"severity"` // ok | info | warning | critical
	Text     string `json:"text"`
}

// Insight is GET /v1/insights/gst.
type Insight struct {
	GSTIN    string         `json:"gstin"`
	Period   string         `json:"period"`
	Imported bool           `json:"gstr2b_imported"`
	Counts   map[string]int `json:"counts"`

	// ITC at risk: tax on bills the supplier has not reported (missing in 2B)
	// plus tax booked above what 2B shows on mismatched bills.
	ITCAtRiskPaise      int64 `json:"itc_at_risk_paise"`
	MissingIn2BTaxPaise int64 `json:"missing_in_2b_tax_paise"`
	MismatchExcessPaise int64 `json:"mismatch_excess_paise"`
	// ITC reported by suppliers that the books do not have yet (unclaimed).
	UnbookedTaxPaise int64 `json:"missing_in_books_tax_paise"`
	// From kar's IMS summary.
	AvailableITCPaise    int64 `json:"itc_available_paise"`
	PendingITCPaise      int64 `json:"itc_pending_paise"`
	RejectedITCPaise     int64 `json:"itc_rejected_paise"`
	NotAvailableITCPaise int64 `json:"itc_not_available_paise"`

	MissingIn2BSuppliers    []Supplier `json:"missing_in_2b_suppliers"`
	MissingInBooksSuppliers []Supplier `json:"missing_in_books_suppliers"`
	Mismatches              []Mismatch `json:"mismatches"`
	Highlights              []string   `json:"highlights"`
	Card                    Card       `json:"card"`
	Warnings                []string   `json:"warnings"`
	ComputedAt              time.Time  `json:"computed_at"`
}

// TopN caps the supplier and mismatch lists.
const TopN = 10

// Severity thresholds on ITC at risk.
const (
	warnAtPaise     = 5_000_00  // ₹5,000
	criticalAtPaise = 50_000_00 // ₹50,000
)

// Summarise builds the insight from kar's reconciliation.
func Summarise(rec Reconciliation, now time.Time) Insight {
	in := Insight{
		GSTIN: rec.GSTIN, Period: rec.Period, Imported: len(rec.Import) > 0 && string(rec.Import) != "null",
		Counts: rec.Counts, MissingIn2BSuppliers: []Supplier{}, MissingInBooksSuppliers: []Supplier{},
		Mismatches: []Mismatch{}, Highlights: []string{}, Warnings: []string{}, ComputedAt: now.UTC(),
	}
	if in.Counts == nil {
		in.Counts = map[string]int{}
	}
	if rec.Warning != "" {
		in.Warnings = append(in.Warnings, rec.Warning)
	}
	missing2B := map[string]*Supplier{}
	missingBooks := map[string]*Supplier{}
	add := func(m map[string]*Supplier, gstin, name string, taxable, tax int64) {
		s := m[gstin]
		if s == nil {
			s = &Supplier{GSTIN: gstin, Name: name}
			m[gstin] = s
		}
		if s.Name == "" {
			s.Name = name
		}
		s.Docs++
		s.TaxablePaise += taxable
		s.TaxPaise += tax
	}
	for _, it := range rec.Items {
		switch it.Status {
		case "missing_in_2b":
			if p := it.Purchase; p != nil {
				tax := p.Total()
				in.MissingIn2BTaxPaise += tax
				add(missing2B, p.SupplierGSTIN, p.SupplierName, int64(p.Taxable), tax)
			}
		case "missing_in_books":
			if d := it.Doc; d != nil {
				tax := sign(d) * d.Total()
				in.UnbookedTaxPaise += tax
				add(missingBooks, d.SupplierGSTIN, d.SupplierName, sign(d)*int64(d.Taxable), tax)
			}
		case "mismatch":
			if it.Doc == nil || it.Purchase == nil {
				continue
			}
			m := Mismatch{SupplierGSTIN: it.Doc.SupplierGSTIN, SupplierName: firstNonEmpty(it.Doc.SupplierName, it.Purchase.SupplierName),
				DocNo: it.Doc.DocNo, DocDate: it.Doc.DocDate, BooksNumber: it.Purchase.Number,
				BooksTaxPaise: it.Purchase.Total(), GSTR2BTaxPaise: sign(it.Doc) * it.Doc.Total(), Fields: []string{}}
			for _, d := range it.Diffs {
				m.Fields = append(m.Fields, d.Field)
			}
			if ex := m.BooksTaxPaise - m.GSTR2BTaxPaise; ex > 0 {
				m.ExcessClaimPaise = ex
				in.MismatchExcessPaise += ex
			}
			in.Mismatches = append(in.Mismatches, m)
		}
	}
	in.ITCAtRiskPaise = in.MissingIn2BTaxPaise + in.MismatchExcessPaise
	in.AvailableITCPaise = rec.ITC.Available.Total()
	in.PendingITCPaise = rec.ITC.Pending.Total()
	in.RejectedITCPaise = rec.ITC.Rejected.Total()
	in.NotAvailableITCPaise = rec.ITC.NotAvailable.Total()

	in.MissingIn2BSuppliers = topSuppliers(missing2B)
	in.MissingInBooksSuppliers = topSuppliers(missingBooks)
	sort.SliceStable(in.Mismatches, func(i, j int) bool {
		return in.Mismatches[i].ExcessClaimPaise > in.Mismatches[j].ExcessClaimPaise
	})
	if len(in.Mismatches) > TopN {
		in.Mismatches = in.Mismatches[:TopN]
	}
	in.Highlights = highlights(in)
	in.Card = card(in)
	return in
}

func sign(d *Doc) int64 {
	if d.DocType == "CN" {
		return -1
	}
	return 1
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func topSuppliers(m map[string]*Supplier) []Supplier {
	out := make([]Supplier, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaxPaise != out[j].TaxPaise {
			return out[i].TaxPaise > out[j].TaxPaise
		}
		return out[i].GSTIN < out[j].GSTIN
	})
	if len(out) > TopN {
		out = out[:TopN]
	}
	return out
}

func highlights(in Insight) []string {
	var h []string
	if !in.Imported {
		h = append(h, "GSTR-2B for "+in.Period+" is not imported yet — import or fetch it in the GST centre to check your input tax credit.")
		return h
	}
	if n := in.Counts["missing_in_2b"]; n > 0 {
		s := fmt.Sprintf("%d purchase bill(s) worth %s GST are not in your 2B", n, money.Format(in.MissingIn2BTaxPaise))
		if len(in.MissingIn2BSuppliers) > 0 {
			top := in.MissingIn2BSuppliers[0]
			s += fmt.Sprintf("; biggest: %s (%d bill(s), %s)", firstNonEmpty(top.Name, top.GSTIN), top.Docs, money.Format(top.TaxPaise))
		}
		h = append(h, s+" — ask these suppliers to file their GSTR-1.")
	}
	if n := in.Counts["mismatch"]; n > 0 {
		s := fmt.Sprintf("%d bill(s) differ between your books and 2B", n)
		if in.MismatchExcessPaise > 0 {
			s += fmt.Sprintf(", with %s more GST in your books than the supplier reported", money.Format(in.MismatchExcessPaise))
		}
		h = append(h, s+".")
	}
	if n := in.Counts["missing_in_books"]; n > 0 {
		h = append(h, fmt.Sprintf("%d supplier document(s) with %s GST are in 2B but not in your books — book them or reject them in IMS.",
			n, money.Format(in.UnbookedTaxPaise)))
	}
	if in.PendingITCPaise > 0 {
		h = append(h, fmt.Sprintf("%s ITC is kept pending in IMS.", money.Format(in.PendingITCPaise)))
	}
	if len(h) == 0 {
		h = append(h, "Every purchase bill matches GSTR-2B for "+in.Period+".")
	}
	return h
}

func card(in Insight) Card {
	c := Card{Title: "GST input credit (2B) " + in.Period, Value: money.Format(in.ITCAtRiskPaise) + " at risk"}
	switch {
	case !in.Imported:
		c.Severity, c.Value = "info", "2B not imported"
	case in.ITCAtRiskPaise >= criticalAtPaise:
		c.Severity = "critical"
	case in.ITCAtRiskPaise >= warnAtPaise:
		c.Severity = "warning"
	case in.ITCAtRiskPaise > 0 || in.Counts["mismatch"] > 0 || in.Counts["missing_in_books"] > 0:
		c.Severity = "info"
	default:
		c.Severity, c.Value = "ok", "All matched"
	}
	if len(in.Highlights) > 0 {
		c.Text = in.Highlights[0]
	}
	return c
}

// PreviousPeriod is the GST return period (MMYYYY) before now in IST; 2B for a
// month is generated on the 14th of the next, so it is the usual default.
func PreviousPeriod(now time.Time) string {
	ist := now.In(time.FixedZone("IST", 5*3600+1800))
	first := time.Date(ist.Year(), ist.Month(), 1, 0, 0, 0, 0, ist.Location())
	return first.AddDate(0, -1, 0).Format("012006")
}

// ValidPeriod checks MMYYYY.
func ValidPeriod(p string) bool {
	if len(p) != 6 {
		return false
	}
	_, err := time.Parse("012006", p)
	return err == nil
}
