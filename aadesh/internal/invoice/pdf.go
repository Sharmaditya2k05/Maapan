package invoice

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image/png"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/go-pdf/fpdf"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/svc"
)

// Hind (Indian Type Foundry, SIL Open Font License 1.1 — fonts/OFL.txt) covers
// Latin, Devanagari and the rupee sign, so ₹ and Hindi party/item names print.
// fpdf does not shape complex scripts: Devanagari conjuncts and the pre-base
// matra (ि) print in logical order, which is legible but not typographically
// perfect.
var (
	//go:embed fonts/Hind-Regular.ttf
	fontRegular []byte
	//go:embed fonts/Hind-Bold.ttf
	fontBold []byte
)

// PrintData is everything a printed document needs besides the invoice.
type PrintData struct {
	Store    clients.Store
	Settings BillingSettings
}

// printData gathers the store (from pehchaan) and the tenant's print settings.
// A pehchaan outage still prints, from what the invoice itself carries.
func (s *Service) printData(ctx context.Context, tenantID string, inv *Invoice) PrintData {
	pd := PrintData{Settings: DefaultBillingSettings()}
	if bs, err := s.BillingSettings(ctx, tenantID); err == nil {
		pd.Settings = bs
	} else {
		s.Log.Warn("billing settings unavailable for print", "error", err.Error())
	}
	st, err := s.Dir.Store(svc.ForTenant(ctx, tenantID), tenantID, inv.StoreID)
	if err != nil {
		s.Log.Warn("store unavailable for print; printing from the invoice", "store_id", inv.StoreID, "error", err.Error())
		st = clients.Store{ID: inv.StoreID}
	}
	if st.GSTIN == "" {
		st.GSTIN = deref(inv.StoreGSTIN)
	}
	if st.StateCode == "" {
		st.StateCode = inv.StoreState
	}
	pd.Store = st
	return pd
}

// docTitle is the heading a document prints under (Rule 46 / 49 / 53).
func docTitle(inv *Invoice, composition bool) string {
	switch inv.DocType {
	case DocSaleReturn:
		return "CREDIT NOTE"
	case DocPurchaseReturn:
		return "DEBIT NOTE"
	case DocPurchase:
		if inv.ReverseCharge {
			return "PURCHASE BILL (REVERSE CHARGE)"
		}
		return "PURCHASE BILL"
	}
	if composition {
		return "BILL OF SUPPLY"
	}
	return "TAX INVOICE"
}

// Rupees formats paise as "₹1,23,456.78" (Indian grouping).
func Rupees(p int64) string {
	neg := p < 0
	if neg {
		p = -p
	}
	r, ps := p/100, p%100
	s := strconv.FormatInt(r, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	out := fmt.Sprintf("₹%s.%02d", s, ps)
	if neg {
		return "-" + out
	}
	return out
}

func amt(p int64) string { return strings.TrimPrefix(Rupees(p), "₹") }

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

func pct(bp int) string {
	if bp%100 == 0 {
		return strconv.Itoa(bp/100) + "%"
	}
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64) + "%"
}

func stateLabel(code string) string {
	if n, ok := gst.States[code]; ok {
		return n + " (" + code + ")"
	}
	return code
}

// hsnRow is one line of the HSN-wise summary.
type hsnRow struct {
	HSN                 string
	RateBP              int
	Qty                 float64
	Taxable, CGST, SGST int64
	IGST, Cess, Total   int64
}

// HSNSummary groups an invoice's lines by HSN and rate (printed under the lines,
// and the shape of GSTR-1 Table 12).
func HSNSummary(lines []Line) []hsnRow {
	idx := map[string]int{}
	var out []hsnRow
	for _, l := range lines {
		h := deref(l.HSN)
		k := h + "|" + strconv.Itoa(l.TaxRateBP)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, hsnRow{HSN: h, RateBP: l.TaxRateBP})
		}
		r := &out[i]
		r.Qty += l.Qty * unitFactorOr1(l.UnitFactor)
		r.Taxable += l.TaxablePaise
		r.CGST += l.CGSTPaise
		r.SGST += l.SGSTPaise
		r.IGST += l.IGSTPaise
		r.Cess += l.CessPaise
		r.Total += l.CGSTPaise + l.SGSTPaise + l.IGSTPaise + l.CessPaise
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].HSN != out[b].HSN {
			return out[a].HSN < out[b].HSN
		}
		return out[a].RateBP < out[b].RateBP
	})
	return out
}

// RenderInvoicePDF draws an A4 invoice/bill/note.
func RenderInvoicePDF(inv *Invoice, pd PrintData) ([]byte, error) {
	f := fpdf.New("P", "mm", "A4", "")
	f.AddUTF8FontFromBytes("hind", "", fontRegular)
	f.AddUTF8FontFromBytes("hind", "B", fontBold)
	if err := f.Error(); err != nil {
		return nil, err
	}
	title := docTitle(inv, pd.Store.Composition)
	f.SetTitle(title+" "+inv.Number, true)
	f.SetSubject(inv.Number, true)
	f.SetAuthor(firstNonEmpty(pd.Store.LegalName, pd.Store.TradeName, pd.Store.Name), true)
	f.SetCreator("Maapan aadesh", false)
	f.SetCreationDate(inv.CreatedAt)
	f.SetModificationDate(inv.CreatedAt)
	f.SetMargins(10, 10, 10)
	f.SetAutoPageBreak(true, 18)
	f.AliasNbPages("{nb}")
	f.SetFooterFunc(func() {
		f.SetY(-13)
		f.SetFont("hind", "", 7.5)
		f.SetTextColor(110, 110, 110)
		f.CellFormat(95, 5, "This is a computer-generated document. "+inv.Number, "", 0, "L", false, 0, "")
		f.CellFormat(95, 5, fmt.Sprintf("Page %d of {nb}", f.PageNo()), "", 0, "R", false, 0, "")
		f.SetTextColor(0, 0, 0)
	})
	f.AddPage()
	const W = 190.0

	// Heading.
	f.SetFont("hind", "B", 14)
	f.CellFormat(W, 7, title, "", 1, "C", false, 0, "")
	if inv.Status == "cancelled" {
		f.SetFont("hind", "B", 10)
		f.SetTextColor(200, 0, 0)
		f.CellFormat(W, 5, "CANCELLED", "", 1, "C", false, 0, "")
		f.SetTextColor(0, 0, 0)
	}
	f.Ln(1)

	// Supplier block (left) and document details (right). The QR goes top right when e-invoiced.
	topY := f.GetY()
	qrSize := 0.0
	if inv.SignedQR != nil && *inv.SignedQR != "" {
		if img, err := qrPNG(*inv.SignedQR); err == nil {
			f.RegisterImageOptionsReader("irnqr", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(img))
			qrSize = 32
			f.ImageOptions("irnqr", 10+W-qrSize, topY, qrSize, qrSize, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
		}
	}
	st := pd.Store
	f.SetFont("hind", "B", 12)
	f.MultiCell(110, 6, firstNonEmpty(st.TradeName, st.LegalName, st.Name, "—"), "", "L", false)
	f.SetFont("hind", "", 8.5)
	if st.LegalName != "" && st.TradeName != "" && st.LegalName != st.TradeName {
		f.MultiCell(110, 4.2, st.LegalName, "", "L", false)
	}
	addr := strings.TrimSpace(strings.Join(nonEmpty(st.Address, strings.TrimSpace(st.City+" "+st.PIN)), ", "))
	if addr != "" {
		f.MultiCell(110, 4.2, addr, "", "L", false)
	}
	if st.StateCode != "" {
		f.MultiCell(110, 4.2, "State: "+stateLabel(st.StateCode), "", "L", false)
	}
	if st.Phone != "" {
		f.MultiCell(110, 4.2, "Phone: "+st.Phone, "", "L", false)
	}
	f.SetFont("hind", "B", 9)
	if st.GSTIN != "" {
		f.MultiCell(110, 4.6, "GSTIN: "+st.GSTIN, "", "L", false)
	} else {
		f.MultiCell(110, 4.6, "Unregistered", "", "L", false)
	}
	leftBottom := f.GetY()

	detailX := 10 + 112.0
	detailW := W - 112 - qrSize - 1
	if detailW < 40 {
		detailW = 40
	}
	f.SetXY(detailX, topY)
	kv := func(k, v string) {
		if v == "" {
			return
		}
		f.SetX(detailX)
		f.SetFont("hind", "", 8.5)
		f.CellFormat(24, 4.6, k, "", 0, "L", false, 0, "")
		f.SetFont("hind", "B", 8.5)
		f.MultiCell(detailW-24, 4.6, v, "", "L", false)
	}
	numLabel := "Invoice No"
	if IsNote(inv.DocType) {
		numLabel = "Note No"
	} else if inv.DocType == DocPurchase {
		numLabel = "Bill No"
	}
	kv(numLabel, inv.Number)
	kv("Date", humanDate(inv.InvoiceDate))
	kv("Due date", humanDate(deref(inv.DueDate)))
	kv("Supplier bill", strings.TrimSpace(deref(inv.SupplierInvoiceNo)+" "+humanDate(deref(inv.SupplierInvoiceDate))))
	kv("Place of supply", stateLabel(inv.PlaceOfSupply))
	if inv.ReverseCharge {
		kv("Reverse charge", "Yes")
	} else {
		kv("Reverse charge", "No")
	}
	if inv.EWBNo != nil {
		kv("e-Way bill", *inv.EWBNo)
	}
	rightBottom := f.GetY()
	y := maxf(leftBottom, rightBottom, topY+qrSize) + 1

	// IRN band.
	if inv.IRN != nil && *inv.IRN != "" {
		f.SetXY(10, y)
		f.SetFont("hind", "", 7.5)
		irn := "IRN: " + *inv.IRN
		if inv.AckNo != nil {
			irn += "   Ack No: " + *inv.AckNo
		}
		if inv.AckDate != nil {
			irn += "   Ack Date: " + inv.AckDate.In(IST).Format("02-01-2006 15:04")
		}
		if deref(inv.EInvoiceStatus) == EInvoiceCancelled {
			irn += "   (IRN cancelled)"
		}
		f.MultiCell(W, 4, irn, "TB", "L", false)
		y = f.GetY() + 1
	}

	// Party.
	f.SetXY(10, y)
	f.SetFillColor(240, 240, 240)
	f.SetFont("hind", "B", 8.5)
	partyLabel := "Bill to"
	if IsPurchaseDoc(inv.DocType) {
		partyLabel = "Supplier"
	}
	f.CellFormat(W, 5, partyLabel, "1", 1, "L", true, 0, "")
	f.SetFont("hind", "B", 10)
	f.MultiCell(W, 5, inv.PartyName, "LR", "L", false)
	f.SetFont("hind", "", 8.5)
	var pl []string
	if inv.PartyGSTIN != nil {
		pl = append(pl, "GSTIN: "+*inv.PartyGSTIN)
	}
	if inv.PartyState != nil {
		pl = append(pl, "State: "+stateLabel(*inv.PartyState))
	}
	if inv.PartyPhone != nil {
		pl = append(pl, "Phone: "+*inv.PartyPhone)
	}
	f.MultiCell(W, 4.6, strings.Join(pl, "    "), "LRB", "L", false)
	f.Ln(2)

	// Lines.
	type col struct {
		h     string
		w     float64
		align string
	}
	cols := []col{{"#", 7, "C"}, {"Description", 61, "L"}, {"HSN/SAC", 16, "C"}, {"Qty", 18, "R"}, {"Rate", 18, "R"},
		{"Disc", 14, "R"}, {"Taxable", 20, "R"}, {"GST", 11, "C"}, {"Amount", 25, "R"}}
	header := func() {
		f.SetFont("hind", "B", 8)
		f.SetFillColor(230, 230, 230)
		for i, c := range cols {
			ln := 0
			if i == len(cols)-1 {
				ln = 1
			}
			f.CellFormat(c.w, 6, c.h, "1", ln, "C", true, 0, "")
		}
	}
	header()
	f.SetFont("hind", "", 8)
	for _, l := range inv.Lines {
		desc := l.Name
		var extra []string
		if l.BatchNo != nil {
			extra = append(extra, "Batch "+*l.BatchNo)
		}
		if l.UnitFactor > 1 && l.BaseUnit != "" {
			extra = append(extra, fmt.Sprintf("1 %s = %s %s", l.Unit, fmtQty(l.UnitFactor), l.BaseUnit))
		}
		if len(l.Serials) > 0 {
			extra = append(extra, "S/N "+strings.Join(l.Serials, ", "))
		}
		if len(extra) > 0 {
			desc += "\n" + strings.Join(extra, " · ")
		}
		lines := f.SplitText(desc, cols[1].w-2)
		h := 5.0 * float64(len(lines))
		if h < 6 {
			h = 6
		}
		if f.GetY()+h > 297-20 {
			f.AddPage()
			header()
			f.SetFont("hind", "", 8)
		}
		x0, y0 := f.GetX(), f.GetY()
		vals := []string{strconv.Itoa(l.LineNo), "", deref(l.HSN), fmtQty(l.Qty) + " " + l.Unit, amt(l.UnitPricePaise),
			amt(l.DiscountPaise), amt(l.TaxablePaise), pct(l.TaxRateBP), amt(l.TotalPaise)}
		x := x0
		for i, c := range cols {
			f.Rect(x, y0, c.w, h, "D")
			if i == 1 {
				f.SetXY(x+1, y0+0.5)
				f.MultiCell(c.w-2, 5, desc, "", "L", false)
			} else {
				f.SetXY(x, y0)
				f.CellFormat(c.w, 6, vals[i], "", 0, c.align, false, 0, "")
			}
			x += c.w
		}
		f.SetXY(x0, y0+h)
	}
	f.Ln(2)

	// HSN summary.
	if f.GetY() > 297-70 {
		f.AddPage()
	}
	f.SetFont("hind", "B", 8.5)
	f.CellFormat(W, 5, "HSN/SAC summary", "", 1, "L", false, 0, "")
	hcols := []col{{"HSN/SAC", 26, "C"}, {"Rate", 14, "C"}, {"Taxable value", 30, "R"}, {"CGST", 26, "R"}, {"SGST/UTGST", 26, "R"},
		{"IGST", 26, "R"}, {"Cess", 18, "R"}, {"Total tax", 24, "R"}}
	f.SetFont("hind", "B", 8)
	f.SetFillColor(230, 230, 230)
	for i, c := range hcols {
		ln := 0
		if i == len(hcols)-1 {
			ln = 1
		}
		f.CellFormat(c.w, 5.5, c.h, "1", ln, "C", true, 0, "")
	}
	f.SetFont("hind", "", 8)
	for _, r := range HSNSummary(inv.Lines) {
		vals := []string{firstNonEmpty(r.HSN, "—"), pct(r.RateBP), amt(r.Taxable), amt(r.CGST), amt(r.SGST), amt(r.IGST), amt(r.Cess), amt(r.Total)}
		for i, c := range hcols {
			ln := 0
			if i == len(hcols)-1 {
				ln = 1
			}
			f.CellFormat(c.w, 5.5, vals[i], "1", ln, c.align, false, 0, "")
		}
	}
	f.Ln(2)

	// Totals (right) and words / bank (left).
	if f.GetY() > 297-75 {
		f.AddPage()
	}
	blockY := f.GetY()
	tx := 10 + W - 70.0
	f.SetXY(tx, blockY)
	tot := func(k string, v int64, bold bool, always bool) {
		if v == 0 && !always {
			return
		}
		f.SetX(tx)
		style := ""
		if bold {
			style = "B"
		}
		f.SetFont("hind", style, 9)
		f.CellFormat(40, 5.5, k, "1", 0, "L", false, 0, "")
		f.CellFormat(30, 5.5, Rupees(v), "1", 1, "R", false, 0, "")
	}
	tot("Taxable value", inv.TaxablePaise, false, true)
	if inv.InvoiceDiscountPaise > 0 {
		tot("Invoice discount", -inv.InvoiceDiscountPaise, false, false)
	}
	tot("CGST", inv.CGSTPaise, false, false)
	tot("SGST/UTGST", inv.SGSTPaise, false, false)
	tot("IGST", inv.IGSTPaise, false, false)
	tot("Cess", inv.CessPaise, false, false)
	tot("TCS", inv.TCSPaise, false, false)
	tot("Round off", inv.RoundOffPaise, false, false)
	tot("Grand total", inv.GrandTotalPaise, true, true)
	if inv.DocType == DocSale && inv.PaidPaise > 0 {
		tot("Received", inv.PaidPaise, false, false)
		tot("Balance due", inv.BalancePaise, true, true)
	}
	totalsBottom := f.GetY()

	f.SetXY(10, blockY)
	lw := W - 74
	f.SetFont("hind", "B", 8.5)
	f.MultiCell(lw, 4.6, "Amount in words", "", "L", false)
	f.SetFont("hind", "", 8.5)
	f.MultiCell(lw, 4.6, inv.AmountInWords, "", "L", false)
	if inv.ReverseCharge {
		f.MultiCell(lw, 4.6, "Tax payable on reverse charge: Yes", "", "L", false)
	}
	f.Ln(1)
	bs := pd.Settings
	f.SetFont("hind", "B", 8.5)
	f.MultiCell(lw, 4.6, "Bank details", "", "L", false)
	f.SetFont("hind", "", 8.5)
	if bs.BankAccountNo == "" && bs.UPIID == "" {
		f.SetTextColor(120, 120, 120)
		f.MultiCell(lw, 4.6, "Bank: ____________   A/c No: ____________   IFSC: ____________", "", "L", false)
		f.SetTextColor(0, 0, 0)
	} else {
		var b []string
		if bs.BankAccountName != "" {
			b = append(b, "A/c name: "+bs.BankAccountName)
		}
		if bs.BankName != "" {
			b = append(b, "Bank: "+strings.TrimSpace(bs.BankName+" "+bs.BankBranch))
		}
		if bs.BankAccountNo != "" {
			b = append(b, "A/c No: "+bs.BankAccountNo)
		}
		if bs.BankIFSC != "" {
			b = append(b, "IFSC: "+bs.BankIFSC)
		}
		if bs.UPIID != "" {
			b = append(b, "UPI: "+bs.UPIID)
		}
		f.MultiCell(lw, 4.6, strings.Join(b, "\n"), "", "L", false)
	}
	if bs.InvoiceTerms != "" {
		f.Ln(1)
		f.SetFont("hind", "B", 8.5)
		f.MultiCell(lw, 4.6, "Terms & conditions", "", "L", false)
		f.SetFont("hind", "", 8)
		f.MultiCell(lw, 4.2, bs.InvoiceTerms, "", "L", false)
	}
	if inv.Notes != nil {
		f.Ln(1)
		f.SetFont("hind", "", 8)
		f.MultiCell(lw, 4.2, "Notes: "+*inv.Notes, "", "L", false)
	}
	y = maxf(f.GetY(), totalsBottom) + 6
	if y > 297-35 {
		f.AddPage()
		y = f.GetY()
	}
	f.SetXY(10+W-70, y)
	f.SetFont("hind", "B", 8.5)
	f.MultiCell(70, 4.6, "For "+firstNonEmpty(st.LegalName, st.TradeName, st.Name, ""), "", "R", false)
	f.SetXY(10+W-70, y+16)
	f.SetFont("hind", "", 8)
	f.MultiCell(70, 4.6, "Authorised signatory", "", "R", false)
	if bs.InvoiceFooter != "" {
		f.SetXY(10, y+24)
		f.SetFont("hind", "", 8)
		f.MultiCell(W, 4.2, bs.InvoiceFooter, "", "C", false)
	}

	var buf bytes.Buffer
	if err := f.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// qrPNG encodes text (the IRP's signed QR JWT) as a PNG QR code.
func qrPNG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M, qr.Auto)
	if err != nil {
		return nil, err
	}
	code, err = barcode.Scale(code, 360, 360)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, code); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func humanDate(s string) string {
	if s == "" {
		return ""
	}
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d.Format("02-01-2006")
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func maxf(v ...float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

// PDFFileName is the download name of an invoice PDF ("INV-26-27-00001.pdf").
func PDFFileName(number string) string {
	n := strings.NewReplacer("/", "-", "\\", "-", " ", "", "\"", "").Replace(number)
	if n == "" {
		n = "invoice"
	}
	return n + ".pdf"
}
