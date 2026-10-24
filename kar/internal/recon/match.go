package recon

import (
	"sort"
	"strings"
)

// Match statuses.
const (
	StatusMatched        = "matched"
	StatusMismatch       = "mismatch"
	StatusMissingInBooks = "missing_in_books" // in 2B, no purchase bill
	StatusMissingIn2B    = "missing_in_2b"    // purchase bill the supplier has not reported
)

// Tolerance is the per-field amount difference still treated as equal (₹1).
const Tolerance Rs = 100

// Purchase is a purchase bill (doc_type purchase) or debit note to a supplier
// (purchase_return) from the books.
type Purchase struct {
	ID                  string `json:"id"`
	DocType             string `json:"doc_type"` // purchase | purchase_return
	Number              string `json:"number"`   // our document number
	InvoiceDate         string `json:"invoice_date"`
	SupplierGSTIN       string `json:"supplier_gstin"`
	SupplierName        string `json:"supplier_name"`
	SupplierInvoiceNo   string `json:"supplier_invoice_no,omitempty"`
	SupplierInvoiceDate string `json:"supplier_invoice_date,omitempty"`
	ReverseCharge       bool   `json:"reverse_charge,omitempty"`
	Taxable             Rs     `json:"taxable"`
	IGST                Rs     `json:"igst"`
	CGST                Rs     `json:"cgst"`
	SGST                Rs     `json:"sgst"`
	Cess                Rs     `json:"cess"`
}

// Diff is one field that differs beyond tolerance.
type Diff struct {
	Field  string `json:"field"`
	Books  any    `json:"books"`
	GSTR2B any    `json:"gstr2b"`
}

// Result is the reconciliation of one document.
type Result struct {
	Status   string    `json:"status"`
	DocKey   string    `json:"doc_key,omitempty"`
	Doc      *Doc      `json:"doc,omitempty"`
	Purchase *Purchase `json:"purchase,omitempty"`
	Diffs    []Diff    `json:"diffs,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
	IMS      *Action   `json:"ims,omitempty"`
}

// MatchInput is everything matching needs.
type MatchInput struct {
	Docs []Doc
	// Purchases are the candidate bills: a window around the period so a bill
	// booked a month late still matches.
	Purchases []Purchase
	// From/To bound the period; purchases dated inside it that match nothing
	// become missing_in_2b.
	From, To string
	// ReportedElsewhere holds supplierGSTIN|normalised number of documents
	// imported in other periods' 2B; such bills are not missing_in_2b.
	ReportedElsewhere map[string]bool
}

func refKey(gstin, no string) string { return strings.ToUpper(gstin) + "|" + NormaliseDocNo(no) }

// Match pairs 2B documents with purchase bills: supplier GSTIN + normalised
// supplier invoice number, then compares the date (same financial year) and
// taxable value and each tax head with ₹1 tolerance. Supplier credit notes
// are paired with our purchase returns by number, else by supplier and amounts.
func Match(in MatchInput) []Result {
	bills := map[string][]int{}   // purchase bills by supplier + supplier invoice no
	returns := map[string][]int{} // purchase returns by supplier + our number / supplier ref
	bySupplierReturn := map[string][]int{}
	for i, p := range in.Purchases {
		if p.SupplierGSTIN == "" {
			continue
		}
		switch p.DocType {
		case "purchase":
			no := p.SupplierInvoiceNo
			if no == "" {
				no = p.Number
			}
			k := refKey(p.SupplierGSTIN, no)
			bills[k] = append(bills[k], i)
		case "purchase_return":
			for _, no := range []string{p.SupplierInvoiceNo, p.Number} {
				if no != "" {
					k := refKey(p.SupplierGSTIN, no)
					returns[k] = append(returns[k], i)
				}
			}
			g := strings.ToUpper(p.SupplierGSTIN)
			bySupplierReturn[g] = append(bySupplierReturn[g], i)
		}
	}
	used := make([]bool, len(in.Purchases))
	take := func(idx []int) (int, int) {
		free := -1
		n := 0
		for _, i := range idx {
			if !used[i] {
				n++
				if free < 0 {
					free = i
				}
			}
		}
		return free, n
	}

	out := make([]Result, 0, len(in.Docs))
	for di := range in.Docs {
		doc := in.Docs[di]
		r := Result{DocKey: doc.Key, Doc: &doc}
		k := refKey(doc.SupplierGSTIN, doc.DocNo)
		pi, n := -1, 0
		byAmount := false
		if doc.DocType == DocCreditNote {
			pi, n = take(returns[k])
			if pi < 0 {
				for _, i := range bySupplierReturn[doc.SupplierGSTIN] {
					if !used[i] && len(amountDiffs(in.Purchases[i], doc)) == 0 {
						pi, n, byAmount = i, 1, true
						break
					}
				}
			}
		} else {
			pi, n = take(bills[k])
		}
		if pi < 0 {
			r.Status = StatusMissingInBooks
			out = append(out, r)
			continue
		}
		used[pi] = true
		p := in.Purchases[pi]
		r.Purchase = &p
		if n > 1 {
			r.Notes = append(r.Notes, "more than one bill in the books carries this supplier invoice number; check for a duplicate entry")
		}
		if byAmount {
			r.Notes = append(r.Notes, "credit note paired with a purchase return by supplier and amounts (numbers differ)")
		}
		r.Diffs = amountDiffs(p, doc)
		bd := p.SupplierInvoiceDate
		if bd == "" {
			bd = p.InvoiceDate
		}
		if bd != doc.DocDate {
			if fyStart(bd) != fyStart(doc.DocDate) {
				r.Diffs = append(r.Diffs, Diff{Field: "date", Books: bd, GSTR2B: doc.DocDate})
			} else {
				r.Notes = append(r.Notes, "document date differs ("+bd+" in books, "+doc.DocDate+" in 2B) within the same financial year")
			}
		}
		if p.ReverseCharge != doc.ReverseCharge {
			r.Diffs = append(r.Diffs, Diff{Field: "reverse_charge", Books: p.ReverseCharge, GSTR2B: doc.ReverseCharge})
		}
		r.Status = StatusMatched
		if len(r.Diffs) > 0 {
			r.Status = StatusMismatch
		}
		out = append(out, r)
	}

	for i, p := range in.Purchases {
		if used[i] || p.SupplierGSTIN == "" || p.InvoiceDate < in.From || p.InvoiceDate > in.To {
			continue
		}
		no := p.SupplierInvoiceNo
		if no == "" {
			no = p.Number
		}
		if in.ReportedElsewhere[refKey(p.SupplierGSTIN, no)] {
			continue
		}
		p := p
		out = append(out, Result{Status: StatusMissingIn2B, Purchase: &p})
	}
	sort.SliceStable(out, func(i, j int) bool { return statusRank[out[i].Status] < statusRank[out[j].Status] })
	return out
}

var statusRank = map[string]int{StatusMismatch: 0, StatusMissingInBooks: 1, StatusMissingIn2B: 2, StatusMatched: 3}

func amountDiffs(p Purchase, d Doc) []Diff {
	var out []Diff
	cmp := func(field string, books, twoB Rs) {
		if diff := books - twoB; diff > Tolerance || diff < -Tolerance {
			out = append(out, Diff{Field: field, Books: books, GSTR2B: twoB})
		}
	}
	cmp("taxable", p.Taxable, d.Taxable)
	cmp("igst", p.IGST, d.IGST)
	cmp("cgst", p.CGST, d.CGST)
	cmp("sgst", p.SGST, d.SGST)
	cmp("cess", p.Cess, d.Cess)
	return out
}

// Counts tallies results by status.
func Counts(rs []Result) map[string]int {
	c := map[string]int{StatusMatched: 0, StatusMismatch: 0, StatusMissingInBooks: 0, StatusMissingIn2B: 0}
	for _, r := range rs {
		c[r.Status]++
	}
	return c
}
