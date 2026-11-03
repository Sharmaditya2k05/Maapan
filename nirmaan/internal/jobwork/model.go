// Package jobwork sends goods to job workers on a delivery challan and
// receives processed goods back, under Section 143 of the CGST Act, 2017:
//
//   - inputs must come back (or be supplied from the job worker's place)
//     within 1 year, capital goods within 3 years of being sent; otherwise the
//     goods are deemed supplied by the principal on the day they were sent
//     (Sec 143(3)/(4)) and GST with interest is due. Moulds, dies, jigs,
//     fixtures and tools are exempt from the return limit (Sec 143(1) proviso).
//   - goods go out on a challan (Rule 45 with Rule 55): serial number of at
//     most 16 characters, date, GSTINs, HSN, description, quantity, taxable
//     value, tax rate. An e-way bill is needed above ₹50,000, and for an
//     inter-state movement to a job worker whatever the value (Rule 138(1)).
//   - the principal reports the movements in ITC-04: table 4 (sent) and
//     table 5A (received back). Since 1 Oct 2021 it is half-yearly when AATO
//     is above ₹5 crore and annual otherwise (quarterly before that); any
//     period can be pulled here.
package jobwork

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/units"
)

// Goods types on a challan line (ITC-04 "types of goods").
const (
	GoodsInputs  = "inputs"
	GoodsCapital = "capital_goods"
)

// Receipt line kinds.
const (
	KindProcessed = "processed" // processed goods received back
	KindReturned  = "returned"  // sent goods returned unprocessed
	KindWaste     = "waste"     // losses & waste at the job worker
)

// Challan statuses.
const (
	StatusOpen      = "open"
	StatusPartial   = "partially_received"
	StatusClosed    = "closed"
	StatusCancelled = "cancelled"
)

// EWayThresholdPaise is the e-way bill value limit (₹50,000).
const EWayThresholdPaise int64 = 50_000_00

// DueSoonDays flags a line whose return limit is this close.
const DueSoonDays = 30

// DueDate is the last day goods sent on sent must come back: 1 year for
// inputs, 3 years for capital goods; "" when exempt (moulds, dies, jigs, tools).
func DueDate(sent time.Time, goodsType string, exempt bool) string {
	if exempt {
		return ""
	}
	years := 1
	if goodsType == GoodsCapital {
		years = 3
	}
	return sent.AddDate(years, 0, 0).AddDate(0, 0, -1).Format(time.DateOnly)
}

// Line status in the pending report.
const (
	DueOK      = "ok"
	DueSoon    = "due_soon"
	DueOverdue = "overdue" // deemed supply on the challan date
	DueExempt  = "exempt"
	DueSettled = "settled"
)

// DueStatus classifies a pending line on asOf (YYYY-MM-DD) and days to its due date.
func DueStatus(due, asOf string, pending float64) (string, *int) {
	if pending <= 0 {
		return DueSettled, nil
	}
	if due == "" {
		return DueExempt, nil
	}
	d, err1 := time.Parse(time.DateOnly, due)
	a, err2 := time.Parse(time.DateOnly, asOf)
	if err1 != nil || err2 != nil {
		return DueOK, nil
	}
	days := int(d.Sub(a).Hours() / 24)
	switch {
	case days < 0:
		return DueOverdue, &days
	case days <= DueSoonDays:
		return DueSoon, &days
	}
	return DueOK, &days
}

// ChallanFacts is what the challan warnings look at.
type ChallanFacts struct {
	ChallanDate     time.Time
	PrincipalState  string
	JobWorkerState  string
	JobWorkerGSTIN  string
	TotalValuePaise int64
	EWayBillNo      string
	Lines           []FactLine
}

// FactLine is the part of a challan line the warnings look at.
type FactLine struct {
	GoodsType string
	Exempt    bool
}

// InterState reports whether the movement crosses a state boundary.
func (f ChallanFacts) InterState() bool {
	return f.PrincipalState != "" && f.JobWorkerState != "" && f.PrincipalState != f.JobWorkerState
}

// Warnings lists the GST obligations the challan triggers.
func Warnings(f ChallanFacts) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range f.Lines {
		key := l.GoodsType
		if l.Exempt {
			key = "exempt"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		switch key {
		case GoodsInputs:
			out = append(out, fmt.Sprintf("Inputs must be received back (or supplied from the job worker's premises) by %s (1 year); otherwise they are deemed supplied on %s and GST with interest is payable (Sec 143(3)).",
				DueDate(f.ChallanDate, GoodsInputs, false), f.ChallanDate.Format(time.DateOnly)))
		case GoodsCapital:
			out = append(out, fmt.Sprintf("Capital goods must be received back by %s (3 years); otherwise they are deemed supplied on %s (Sec 143(4)).",
				DueDate(f.ChallanDate, GoodsCapital, false), f.ChallanDate.Format(time.DateOnly)))
		case "exempt":
			out = append(out, "Moulds, dies, jigs, fixtures and tools have no return time limit (Sec 143(1) proviso), but are still reported in ITC-04.")
		}
	}
	switch {
	case f.InterState():
		out = append(out, "Inter-state movement to a job worker: an e-way bill is mandatory whatever the value (Rule 138(1) proviso).")
	case f.TotalValuePaise > EWayThresholdPaise:
		out = append(out, "Consignment value is above ₹50,000: an e-way bill is required before the goods move (Rule 138).")
	}
	if (f.InterState() || f.TotalValuePaise > EWayThresholdPaise) && strings.TrimSpace(f.EWayBillNo) == "" {
		out = append(out, "No e-way bill number recorded on this challan yet.")
	}
	if strings.TrimSpace(f.JobWorkerGSTIN) == "" {
		out = append(out, "Unregistered job worker: goods can be supplied directly from their premises only if you declare it as your additional place of business (Sec 143(1)(b)).")
	}
	out = append(out, "Report this challan in ITC-04 table 4 for the period of the challan date.")
	return out
}

// PendingLine is one challan line still (partly) with the job worker.
type PendingLine struct {
	ChallanID     string  `json:"challan_id"`
	ChallanNo     string  `json:"challan_no"`
	ChallanDate   string  `json:"challan_date"`
	JobWorkerID   string  `json:"job_worker_id"`
	JobWorkerName string  `json:"job_worker_name"`
	LineNo        int     `json:"line_no"`
	ItemID        string  `json:"item_id"`
	ItemName      string  `json:"item_name"`
	Unit          string  `json:"unit"`
	GoodsType     string  `json:"goods_type"`
	Sent          float64 `json:"sent"`
	Processed     float64 `json:"processed"` // input qty settled by processed goods
	Returned      float64 `json:"returned"`
	Waste         float64 `json:"waste"`
	Pending       float64 `json:"pending"`
	PendingValue  int64   `json:"pending_value_paise"`
	DueDate       string  `json:"due_date,omitempty"`
	DaysLeft      *int    `json:"days_left,omitempty"`
	Status        string  `json:"status"`
}

// Settle fills Pending, PendingValue and Status from the settled quantities.
func (p *PendingLine) Settle(valuePaise int64, asOf string) {
	p.Pending = units.Round3(p.Sent - p.Processed - p.Returned - p.Waste)
	if p.Pending < 0 {
		p.Pending = 0
	}
	p.PendingValue = units.MulDiv(valuePaise, units.Milli(p.Pending), units.Milli(p.Sent))
	p.Status, p.DaysLeft = DueStatus(p.DueDate, asOf, p.Pending)
}

// Period is an ITC-04 return period.
type Period struct {
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ParsePeriod reads "2026-27" (annual), "2026-27-H1|H2" (half-year) or
// "2026-27-Q1..Q4" (quarter); from/to (YYYY-MM-DD) win when both are given.
func ParsePeriod(period, from, to string) (Period, error) {
	if from != "" && to != "" {
		for _, d := range []string{from, to} {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return Period{}, httpx.Validation("from/to must be YYYY-MM-DD")
			}
		}
		if to < from {
			return Period{}, httpx.Validation("to is before from")
		}
		return Period{Label: from + " to " + to, From: from, To: to}, nil
	}
	var y, y2 int
	var part string
	if n, _ := fmt.Sscanf(period, "%d-%d-%s", &y, &y2, &part); n < 2 {
		return Period{}, httpx.Validation("period must look like 2026-27, 2026-27-H1 or 2026-27-Q2 (or give from and to)")
	}
	if (y+1)%100 != y2 {
		return Period{}, httpx.Validation("period %s is not a financial year", period)
	}
	start := time.Date(y, time.April, 1, 0, 0, 0, 0, time.UTC)
	months, offset := 12, 0
	switch strings.ToUpper(part) {
	case "":
	case "H1":
		months = 6
	case "H2":
		months, offset = 6, 6
	case "Q1", "Q2", "Q3", "Q4":
		months, offset = 3, 3*int(part[1]-'1')
	default:
		return Period{}, httpx.Validation("period suffix must be H1, H2 or Q1..Q4")
	}
	f := start.AddDate(0, offset, 0)
	t := f.AddDate(0, months, -1)
	return Period{Label: period, From: f.Format(time.DateOnly), To: t.Format(time.DateOnly)}, nil
}

// Table4Row is ITC-04 table 4: goods sent to a job worker.
type Table4Row struct {
	JobWorkerGSTIN string  `json:"jw_gstin"`
	JobWorkerState string  `json:"jw_state"`
	ChallanNo      string  `json:"challan_no"`
	ChallanDate    string  `json:"challan_date"`
	GoodsType      string  `json:"goods_type"`
	Description    string  `json:"description"`
	HSN            string  `json:"hsn"`
	UQC            string  `json:"uqc"`
	Qty            float64 `json:"qty"`
	TaxablePaise   int64   `json:"taxable_value_paise"`
	IGSTRateBP     int     `json:"igst_rate_bp"`
	CGSTRateBP     int     `json:"cgst_rate_bp"`
	SGSTRateBP     int     `json:"sgst_rate_bp"`
	CessRateBP     int     `json:"cess_rate_bp"`
}

// Table5ARow is ITC-04 table 5A: goods received back from a job worker.
type Table5ARow struct {
	JobWorkerGSTIN  string  `json:"jw_gstin"`
	JobWorkerState  string  `json:"jw_state"`
	OrigChallanNo   string  `json:"original_challan_no"`
	OrigChallanDate string  `json:"original_challan_date"`
	JWChallanNo     string  `json:"jw_challan_no"`
	JWChallanDate   string  `json:"jw_challan_date"`
	NatureOfJobWork string  `json:"nature_of_job_work"`
	Description     string  `json:"description"`
	UQC             string  `json:"uqc"`
	Qty             float64 `json:"qty"`
	LossesUQC       string  `json:"losses_uqc"`
	LossesQty       float64 `json:"losses_qty"`
	ReceiptNo       string  `json:"receipt_no"`
	ReceiptDate     string  `json:"receipt_date"`
}

// TaxSplit puts a rate on IGST (inter-state) or half each on CGST/SGST.
func TaxSplit(rateBP int, interState bool) (igst, cgst, sgst int) {
	if interState {
		return rateBP, 0, 0
	}
	return 0, rateBP / 2, rateBP - rateBP/2
}

func rupees(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	return fmt.Sprintf("%s%d.%02d", sign, p/100, p%100)
}

func pct(bp int) string {
	if bp == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d.%02d", bp/100, bp%100)
	return strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".0")
}

func qtyStr(q float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", q), "0"), ".")
}

func goodsLabel(t string) string {
	if t == GoodsCapital {
		return "Capital Goods"
	}
	return "Inputs"
}

// WriteTable4CSV writes table 4 in the column order of the ITC-04 offline tool.
func WriteTable4CSV(w io.Writer, rows []Table4Row) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"GSTIN of Job Worker", "State (if job worker unregistered)", "Challan Number", "Challan Date",
		"Types of Goods", "Description of Goods", "HSN", "Unique Quantity Code (UQC)", "Quantity", "Taxable Value",
		"Integrated Tax Rate (%)", "Central Tax Rate (%)", "State/UT Tax Rate (%)", "Cess"})
	for _, r := range rows {
		state := ""
		if r.JobWorkerGSTIN == "" {
			state = r.JobWorkerState
		}
		if err := cw.Write([]string{r.JobWorkerGSTIN, state, r.ChallanNo, ddmmyyyy(r.ChallanDate), goodsLabel(r.GoodsType),
			r.Description, r.HSN, r.UQC, qtyStr(r.Qty), rupees(r.TaxablePaise), pct(r.IGSTRateBP), pct(r.CGSTRateBP),
			pct(r.SGSTRateBP), pct(r.CessRateBP)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteTable5ACSV writes table 5A in the column order of the ITC-04 offline tool.
func WriteTable5ACSV(w io.Writer, rows []Table5ARow) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"GSTIN of Job Worker", "State (if job worker unregistered)", "Original Challan Number",
		"Original Challan Date", "Challan Number issued by job worker", "Challan Date issued by job worker",
		"Nature of Job Work done", "Description of Goods", "Unique Quantity Code (UQC)", "Quantity",
		"Losses & Wastes UQC", "Losses & Wastes Quantity"})
	for _, r := range rows {
		state := ""
		if r.JobWorkerGSTIN == "" {
			state = r.JobWorkerState
		}
		jwNo, jwDate := r.JWChallanNo, ddmmyyyy(r.JWChallanDate)
		if jwNo == "" { // no document from the job worker: our receipt note stands in
			jwNo, jwDate = r.ReceiptNo, ddmmyyyy(r.ReceiptDate)
		}
		losses := ""
		if r.LossesQty > 0 {
			losses = qtyStr(r.LossesQty)
		}
		if err := cw.Write([]string{r.JobWorkerGSTIN, state, r.OrigChallanNo, ddmmyyyy(r.OrigChallanDate), jwNo, jwDate,
			r.NatureOfJobWork, r.Description, r.UQC, qtyStr(r.Qty), r.LossesUQC, losses}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func ddmmyyyy(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.Format("02-01-2006")
}

// ReceivedLine is one received line as stored, for building table 5A.
type ReceivedLine struct {
	Table5ARow
	ChallanLineNo int
	Kind          string
	ReceiptID     string
}

// BuildTable5A turns received lines into 5A rows, folding each waste line into
// the losses columns of a row for the same receipt and challan line.
func BuildTable5A(lines []ReceivedLine) []Table5ARow {
	type key struct {
		receipt string
		line    int
	}
	var out []Table5ARow
	idx := map[key]int{}
	var wastes []ReceivedLine
	for _, l := range lines {
		if l.Kind == KindWaste {
			wastes = append(wastes, l)
			continue
		}
		row := l.Table5ARow
		k := key{l.ReceiptID, l.ChallanLineNo}
		if _, ok := idx[k]; !ok {
			idx[k] = len(out)
		}
		out = append(out, row)
	}
	for _, w := range wastes {
		k := key{w.ReceiptID, w.ChallanLineNo}
		if i, ok := idx[k]; ok {
			out[i].LossesUQC, out[i].LossesQty = w.UQC, units.Round3(out[i].LossesQty+w.Qty)
			continue
		}
		row := w.Table5ARow
		row.LossesUQC, row.LossesQty, row.Qty = w.UQC, w.Qty, 0
		idx[k] = len(out)
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReceiptDate != out[j].ReceiptDate {
			return out[i].ReceiptDate < out[j].ReceiptDate
		}
		return out[i].ReceiptNo < out[j].ReceiptNo
	})
	return out
}

// Allocate splits total over weights in proportion, exactly (largest remainder).
func Allocate(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	sum := new(big.Int)
	for _, w := range weights {
		sum.Add(sum, big.NewInt(w))
	}
	if sum.Sign() <= 0 {
		if len(out) > 0 {
			out[0] = total
		}
		return out
	}
	type rem struct {
		i int
		r *big.Int
	}
	rems := make([]rem, len(weights))
	var given int64
	for i, w := range weights {
		q, r := new(big.Int).QuoRem(new(big.Int).Mul(big.NewInt(total), big.NewInt(w)), sum, new(big.Int))
		out[i] = q.Int64()
		given += out[i]
		rems[i] = rem{i, r}
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r.Cmp(rems[b].r) > 0 })
	for k := 0; given < total && k < len(rems); k++ {
		out[rems[k].i]++
		given++
	}
	return out
}
