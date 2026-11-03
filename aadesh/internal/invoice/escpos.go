package invoice

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ESC/POS command bytes (Epson TM / every common clone).
var (
	escInit      = []byte{0x1B, 0x40}       // ESC @ — reset
	escCodePage  = []byte{0x1B, 0x74, 0x00} // ESC t 0 — PC437
	escAlignL    = []byte{0x1B, 0x61, 0x00}
	escAlignC    = []byte{0x1B, 0x61, 0x01}
	escBoldOn    = []byte{0x1B, 0x45, 0x01}
	escBoldOff   = []byte{0x1B, 0x45, 0x00}
	gsSizeDouble = []byte{0x1D, 0x21, 0x11} // GS ! — double width and height
	gsSizeNormal = []byte{0x1D, 0x21, 0x00}
	gsCut        = []byte{0x1D, 0x56, 0x42, 0x03} // GS V B — feed 3 lines and partial cut
)

// ThermalColumns is the Font A character width of a 58 mm (2") or 80 mm (3") roll.
func ThermalColumns(widthMM int) int {
	if widthMM == 58 {
		return 32
	}
	return 48
}

// thermal builds the receipt twice in lock-step: ESC/POS bytes for the printer
// and plain text for a preview.
type thermal struct {
	cols int
	raw  bytes.Buffer
	txt  strings.Builder
}

func (t *thermal) cmd(b ...[]byte) {
	for _, c := range b {
		t.raw.Write(c)
	}
}

func (t *thermal) line(s string) {
	s = asciiOnly(s)
	t.raw.WriteString(s)
	t.raw.WriteByte('\n')
	t.txt.WriteString(s)
	t.txt.WriteByte('\n')
}

// wrap prints s over as many lines as it needs.
func (t *thermal) wrap(s string) {
	s = asciiOnly(s)
	for len(s) > t.cols {
		cut := strings.LastIndex(s[:t.cols], " ")
		if cut <= 0 {
			cut = t.cols
		}
		t.line(s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		t.line(s)
	}
}

func (t *thermal) center(s string) {
	t.cmd(escAlignC)
	t.wrap(s)
	t.cmd(escAlignL)
}

// lr prints left text and right text on one line, the left one cut to fit.
func (t *thermal) lr(left, right string) {
	left, right = asciiOnly(left), asciiOnly(right)
	room := t.cols - len(right) - 1
	if room < 1 {
		t.line(right)
		return
	}
	if len(left) > room {
		left = left[:room]
	}
	t.line(left + strings.Repeat(" ", t.cols-len(left)-len(right)) + right)
}

func (t *thermal) rule(ch string) { t.line(strings.Repeat(ch, t.cols)) }

// qr prints a QR code (GS ( k, model 2) — on the printer only; the preview shows a placeholder.
func (t *thermal) qr(data string, label string) {
	if data == "" || len(data) > 1800 {
		return
	}
	size := byte(5)
	if t.cols == 32 {
		size = 4
	}
	if len(data) > 400 {
		size = 3
	}
	t.cmd(escAlignC)
	t.raw.Write([]byte{0x1D, 0x28, 0x6B, 0x04, 0x00, 0x31, 0x41, 0x32, 0x00}) // model 2
	t.raw.Write([]byte{0x1D, 0x28, 0x6B, 0x03, 0x00, 0x31, 0x43, size})       // module size
	t.raw.Write([]byte{0x1D, 0x28, 0x6B, 0x03, 0x00, 0x31, 0x45, 0x31})       // error correction M
	n := len(data) + 3
	t.raw.Write([]byte{0x1D, 0x28, 0x6B, byte(n % 256), byte(n / 256), 0x31, 0x50, 0x30})
	t.raw.WriteString(data)
	t.raw.Write([]byte{0x1D, 0x28, 0x6B, 0x03, 0x00, 0x31, 0x51, 0x30}) // print
	t.raw.WriteByte('\n')
	t.txt.WriteString("[QR: " + label + "]\n")
	if label != "" {
		t.line(label)
	}
	t.cmd(escAlignL)
}

// asciiOnly keeps what a PC437 printer prints: ₹ becomes "Rs", other non-ASCII
// characters (Devanagari names included — most thermal printers have no
// Indic font) become '?'.
func asciiOnly(s string) string {
	if isASCII(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '₹':
			b.WriteString("Rs")
		case r == '–' || r == '—':
			b.WriteByte('-')
		case r == '·':
			b.WriteByte('.')
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// ThermalReceipt renders a sales bill for a 2"/3" roll: ESC/POS bytes and the
// same receipt as plain text.
func ThermalReceipt(inv *Invoice, pd PrintData, widthMM int) (escpos []byte, text string) {
	t := &thermal{cols: ThermalColumns(widthMM)}
	st := pd.Store
	t.cmd(escInit, escCodePage, escAlignC, escBoldOn, gsSizeDouble)
	t.wrap(firstNonEmpty(st.TradeName, st.LegalName, st.Name, "Bill"))
	t.cmd(gsSizeNormal, escBoldOff, escAlignL)
	if st.LegalName != "" && st.TradeName != "" && st.LegalName != st.TradeName {
		t.center(st.LegalName)
	}
	if a := strings.Join(nonEmpty(st.Address, strings.TrimSpace(st.City+" "+st.PIN)), ", "); a != "" {
		t.center(a)
	}
	if st.Phone != "" {
		t.center("Ph: " + st.Phone)
	}
	if st.GSTIN != "" {
		t.center("GSTIN: " + st.GSTIN)
	}
	t.cmd(escBoldOn)
	t.center(docTitle(inv, st.Composition))
	t.cmd(escBoldOff)
	if inv.Status == "cancelled" {
		t.center("*** CANCELLED ***")
	}
	t.rule("-")
	t.lr("No: "+inv.Number, humanDate(inv.InvoiceDate))
	if inv.PartyName != "" && inv.PartyName != "Cash Sale" {
		t.wrap("To: " + inv.PartyName)
	}
	if inv.PartyGSTIN != nil {
		t.wrap("GSTIN: " + *inv.PartyGSTIN)
	}
	t.rule("-")
	t.lr("Item", "Amount")
	t.rule("-")
	for _, l := range inv.Lines {
		t.wrap(l.Name)
		detail := fmt.Sprintf("  %s %s x %s", fmtQty(l.Qty), l.Unit, amt(l.UnitPricePaise))
		if l.DiscountPaise > 0 {
			detail += " -" + amt(l.DiscountPaise)
		}
		t.lr(detail, amt(l.TotalPaise))
		if l.BatchNo != nil {
			t.line("  Batch " + *l.BatchNo)
		}
		for _, sn := range l.Serials {
			t.line("  S/N " + sn)
		}
	}
	t.rule("-")
	t.lr("Taxable value", amt(inv.TaxablePaise))
	if inv.CGSTPaise > 0 {
		t.lr("CGST", amt(inv.CGSTPaise))
	}
	if inv.SGSTPaise > 0 {
		t.lr("SGST", amt(inv.SGSTPaise))
	}
	if inv.IGSTPaise > 0 {
		t.lr("IGST", amt(inv.IGSTPaise))
	}
	if inv.CessPaise > 0 {
		t.lr("Cess", amt(inv.CessPaise))
	}
	if inv.TCSPaise > 0 {
		t.lr("TCS", amt(inv.TCSPaise))
	}
	if inv.RoundOffPaise != 0 {
		t.lr("Round off", amt(inv.RoundOffPaise))
	}
	t.cmd(escBoldOn, []byte{0x1D, 0x21, 0x01}) // double height
	t.lr("TOTAL", "Rs "+amt(inv.GrandTotalPaise))
	t.cmd(gsSizeNormal, escBoldOff)
	for _, td := range inv.Tenders {
		t.lr(strings.ToUpper(td.Mode[:1])+td.Mode[1:], amt(td.AmountPaise))
	}
	if inv.ChangePaise > 0 {
		t.lr("Change", amt(inv.ChangePaise))
	}
	if inv.BalancePaise > 0 && inv.DocType == DocSale {
		t.lr("Balance due", amt(inv.BalancePaise))
	}
	t.rule("-")
	// GST summary by rate (Rule 46 asks for the rate and amount of tax).
	rates := HSNSummary(inv.Lines)
	byRate := map[int][2]int64{}
	var order []int
	for _, r := range rates {
		v, ok := byRate[r.RateBP]
		if !ok {
			order = append(order, r.RateBP)
		}
		byRate[r.RateBP] = [2]int64{v[0] + r.Taxable, v[1] + r.Total}
	}
	if len(order) > 0 {
		t.lr("GST rate   Taxable", "Tax")
		for _, bp := range order {
			v := byRate[bp]
			t.lr(fmt.Sprintf("%-9s  %s", pct(bp), amt(v[0])), amt(v[1]))
		}
		t.rule("-")
	}
	t.wrap(inv.AmountInWords)
	if inv.IRN != nil {
		t.wrap("IRN: " + *inv.IRN)
		if inv.AckNo != nil {
			t.wrap("Ack: " + *inv.AckNo)
		}
	}
	if inv.SignedQR != nil && *inv.SignedQR != "" {
		t.qr(*inv.SignedQR, "e-Invoice QR")
	} else if pd.Settings.UPIID != "" && inv.BalancePaise > 0 && inv.DocType == DocSale && inv.Status == "posted" {
		q := url.Values{"pa": {pd.Settings.UPIID}, "pn": {asciiOnly(firstNonEmpty(st.TradeName, st.LegalName, st.Name))},
			"am": {strconv.FormatFloat(float64(inv.BalancePaise)/100, 'f', 2, 64)}, "cu": {"INR"}, "tn": {inv.Number}}
		t.qr("upi://pay?"+q.Encode(), "Scan to pay "+amt(inv.BalancePaise)+" by UPI")
	}
	if pd.Settings.InvoiceFooter != "" {
		t.center(pd.Settings.InvoiceFooter)
	} else {
		t.center("Thank you! Visit again.")
	}
	t.cmd(gsCut)
	return t.raw.Bytes(), t.txt.String()
}
