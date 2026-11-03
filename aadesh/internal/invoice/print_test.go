package invoice

import (
	"bytes"
	"compress/zlib"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"

	"github.com/maapan/aadesh/internal/clients"
)

func sampleInvoice() *Invoice {
	str := func(s string) *string { return &s }
	ack := time.Date(2026, 10, 23, 11, 5, 0, 0, IST)
	inv := &Invoice{
		ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", StoreID: "s1", DocType: DocSale, Number: "INV/26-27/00042", FY: "26-27",
		InvoiceDate: "2026-10-23", DueDate: str("2026-11-07"), PartyName: "शर्मा किराना स्टोर", PartyGSTIN: str("27AAPFU0939F1ZV"),
		PartyState: str("27"), PartyPhone: str("9876543210"), StoreState: "27", StoreGSTIN: str("27AABCU9603R1ZM"), PlaceOfSupply: "27",
		Status: "posted", PaymentStatus: "partial", TaxablePaise: 169492, CGSTPaise: 15254, SGSTPaise: 15254, RoundOffPaise: 0,
		GrandTotalPaise: 200000, PaidPaise: 50000, BalancePaise: 150000, AmountInWords: "Two Thousand Rupees Only",
		IRN: str("a5c12dca80e743321740b2a3b1cdb6c6b7d1a4b2c3d4e5f60718293a4b5c6d7e"), AckNo: str("112010036563310"), AckDate: &ack,
		SignedQR: str("eyJhbGciOiJSUzI1NiJ9.eyJkYXRhIjoiSU5WLzI2LTI3LzAwMDQyIn0.c2lnbmF0dXJl"), EInvoiceStatus: str(EInvoiceGenerated),
		CreatedBy: "u1", CreatedAt: time.Date(2026, 10, 23, 10, 0, 0, 0, IST),
		Tenders: []Tender{{Mode: "upi", AmountPaise: 50000}},
	}
	inv.Lines = []Line{
		{LineNo: 1, Name: "Parle-G 100g (पारले-जी)", HSN: str("1905"), Unit: "BOX", Qty: 2, UnitPricePaise: 48000, TaxRateBP: 1800,
			TaxCategory: "taxable", TaxablePaise: 81356, CGSTPaise: 7322, SGSTPaise: 7322, TotalPaise: 96000, UnitFactor: 48, BaseUnit: "PCS",
			BaseQty: 96, BatchNo: str("B-2026-10")},
		{LineNo: 2, Name: "Samsung Charger 25W", HSN: str("8504"), Unit: "PCS", Qty: 2, UnitPricePaise: 52000, TaxRateBP: 1800,
			TaxCategory: "taxable", TaxablePaise: 88136, CGSTPaise: 7932, SGSTPaise: 7932, TotalPaise: 104000, UnitFactor: 1, BaseQty: 2,
			Serials: []string{"SN-001", "SN-002"}},
	}
	return inv
}

func samplePrintData() PrintData {
	bs := DefaultBillingSettings()
	bs.BankName, bs.BankAccountNo, bs.BankIFSC, bs.UPIID = "HDFC Bank", "50100012345678", "HDFC0001234", "sharma@hdfc"
	bs.InvoiceTerms = "Goods once sold will not be taken back."
	return PrintData{Settings: bs, Store: clients.Store{ID: "s1", TradeName: "Maapan Mart", LegalName: "Maapan Retail Pvt Ltd",
		Address: "12 MG Road", City: "Pune", PIN: "411001", StateCode: "27", GSTIN: "27AABCU9603R1ZM", Phone: "020-1234567"}}
}

func TestInvoicePDFParsesAndCarriesNumber(t *testing.T) {
	raw, err := RenderInvoicePDF(sampleInvoice(), samplePrintData())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("%PDF-")) || !bytes.Contains(raw[len(raw)-1024:], []byte("%%EOF")) {
		t.Fatal("not a complete PDF")
	}
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("PDF does not parse: %v", err)
	}
	if r.NumPage() < 1 {
		t.Fatal("no pages")
	}
	var text strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		pg := r.Page(i)
		if pg.V.IsNull() {
			continue
		}
		s, err := pg.GetPlainText(nil)
		if err != nil {
			t.Fatalf("page %d text: %v", i, err)
		}
		text.WriteString(s)
	}
	got := strings.ReplaceAll(text.String(), "\n", "")
	for _, want := range []string{"INV/26-27/00042", "TAX INVOICE", "27AABCU9603R1ZM", "1905", "Two Thousand Rupees Only"} {
		if !strings.Contains(got, want) {
			t.Errorf("PDF text lacks %q", want)
		}
	}
	// The title metadata carries the number too (what mail clients show).
	if !bytes.Contains(raw, []byte("/Title")) {
		t.Error("no /Title in the PDF info dictionary")
	}
	// ₹ and Devanagari are drawn with the embedded Unicode font. fpdf writes
	// Identity-H text whose codes are the code points (UTF-16BE), with a
	// ToUnicode map; the text extractor above only reads single bytes, so look
	// for the code points in the decompressed page content instead.
	if !bytes.Contains(raw, []byte("/FontFile2")) || !bytes.Contains(raw, []byte("/ToUnicode")) {
		t.Fatal("the Unicode font must be embedded with a ToUnicode map")
	}
	content := inflateStreams(raw)
	for name, cp := range map[string][]byte{"₹": {0x20, 0xB9}, "श": {0x09, 0x36}, "ज": {0x09, 0x1C}} {
		if !bytes.Contains(content, cp) {
			t.Errorf("page content does not draw %s", name)
		}
	}
}

// inflateStreams concatenates every Flate stream of a PDF, decompressed.
func inflateStreams(raw []byte) []byte {
	var out []byte
	rest := raw
	for {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			return out
		}
		rest = rest[i+len("stream"):]
		rest = bytes.TrimLeft(rest, string([]byte{13, 10}))
		j := bytes.Index(rest, []byte("endstream"))
		if j < 0 {
			return out
		}
		if zr, err := zlib.NewReader(bytes.NewReader(rest[:j])); err == nil {
			b, _ := io.ReadAll(zr)
			out = append(out, b...)
		}
		rest = rest[j+len("endstream"):]
	}
}

func TestInvoicePDFWithoutStoreOrIRN(t *testing.T) {
	inv := sampleInvoice()
	inv.IRN, inv.SignedQR, inv.AckNo, inv.AckDate = nil, nil, nil, nil
	for i := 0; i < 60; i++ { // force a page break
		l := inv.Lines[1]
		l.LineNo = i + 3
		inv.Lines = append(inv.Lines, l)
	}
	raw, err := RenderInvoicePDF(inv, PrintData{Settings: DefaultBillingSettings(), Store: clients.Store{GSTIN: "27AABCU9603R1ZM"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || r.NumPage() < 2 {
		t.Fatalf("long invoice must span pages: %v", err)
	}
}

func TestHSNSummary(t *testing.T) {
	rows := HSNSummary(sampleInvoice().Lines)
	if len(rows) != 2 || rows[0].HSN != "1905" || rows[0].Qty != 96 || rows[0].Total != 14644 || rows[1].HSN != "8504" {
		t.Fatalf("%+v", rows)
	}
}

func TestRupees(t *testing.T) {
	for in, want := range map[int64]string{0: "₹0.00", 5: "₹0.05", 123456789: "₹12,34,567.89", 100000: "₹1,000.00", -2550: "-₹25.50"} {
		if got := Rupees(in); got != want {
			t.Errorf("Rupees(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestThermalReceipt(t *testing.T) {
	inv := sampleInvoice()
	inv.SignedQR = nil
	for _, width := range []int{58, 80} {
		raw, text := ThermalReceipt(inv, samplePrintData(), width)
		cols := ThermalColumns(width)
		if !bytes.HasPrefix(raw, escInit) || !bytes.HasSuffix(raw, gsCut) {
			t.Fatalf("%dmm: must start with ESC @ and end with a cut", width)
		}
		if !bytes.Contains(raw, []byte("INV/26-27/00042")) || !strings.Contains(text, "TOTAL") {
			t.Fatalf("%dmm: number/total missing:\n%s", width, text)
		}
		for _, ln := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if strings.HasPrefix(ln, "[QR") {
				continue
			}
			if len(ln) > cols {
				t.Fatalf("%dmm: line wider than %d columns: %q", width, cols, ln)
			}
		}
		for _, b := range raw {
			if b >= 0x80 {
				t.Fatalf("%dmm: non-ASCII byte 0x%x sent to a PC437 printer", width, b)
			}
		}
		// A balance with a UPI id prints a pay-by-UPI QR (GS ( k).
		if !bytes.Contains(raw, []byte{0x1D, 0x28, 0x6B}) || !bytes.Contains(raw, []byte("upi://pay?")) {
			t.Fatalf("%dmm: UPI QR missing", width)
		}
	}
}

func TestPDFLinkSigning(t *testing.T) {
	key := PDFLinkKey("service-secret")
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, IST)
	id := "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"
	q := SignPDFLink(key, id, now.Add(PDFLinkTTL))
	if !VerifyPDFLink(key, id, q.Get("exp"), q.Get("sig"), now) {
		t.Fatal("fresh link must verify")
	}
	if VerifyPDFLink(key, id, q.Get("exp"), q.Get("sig"), now.Add(PDFLinkTTL+time.Second)) {
		t.Fatal("expired link must fail")
	}
	if VerifyPDFLink(key, "0b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", q.Get("exp"), q.Get("sig"), now) {
		t.Fatal("link for another invoice must fail")
	}
	if VerifyPDFLink(PDFLinkKey("other"), id, q.Get("exp"), q.Get("sig"), now) {
		t.Fatal("another key must fail")
	}
	far := SignPDFLink(key, id, now.Add(30*24*time.Hour))
	if VerifyPDFLink(key, id, far.Get("exp"), far.Get("sig"), now) {
		t.Fatal("links beyond the maximum ttl must fail")
	}
	if string(PDFLinkKey("service-secret")) == "service-secret" {
		t.Fatal("the key must be derived, not the raw secret")
	}
}

func TestRoundTotal(t *testing.T) {
	cases := []struct {
		total      int64
		mode       string
		step       int64
		want, diff int64
	}{
		{10050, RoundNearest, 100, 10100, 50},
		{10049, RoundNearest, 100, 10000, -49},
		{10001, RoundUp, 100, 10100, 99},
		{10099, RoundDown, 100, 10000, -99},
		{10024, RoundNearest, 50, 10000, -24},
		{10025, RoundNearest, 50, 10050, 25},
		{10003, RoundNearest, 10, 10000, -3},
		{10003, RoundNone, 100, 10003, 0},
		{10000, RoundUp, 100, 10000, 0},
		{-10050, RoundDown, 100, -10100, -50},
	}
	for _, c := range cases {
		got, diff := RoundTotal(c.total, c.mode, c.step)
		if got != c.want || diff != c.diff {
			t.Errorf("RoundTotal(%d,%s,%d) = %d,%d want %d,%d", c.total, c.mode, c.step, got, diff, c.want, c.diff)
		}
	}
	bad := BillingSettings{RoundOffMode: "banker"}
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown mode must fail")
	}
	bad = BillingSettings{RoundOffStepPaise: 25}
	if err := bad.Validate(); err == nil {
		t.Fatal("odd step must fail")
	}
}

func TestUnitFactorAndFEFO(t *testing.T) {
	it := clients.Item{Name: "Parle-G", Unit: "PCS"}
	it.AltUnits = append(it.AltUnits, struct {
		Unit   string  `json:"unit"`
		Factor float64 `json:"factor"`
	}{Unit: "BOX", Factor: 48})
	if f, err := unitFactor(it, "box"); err != nil || f != 48 {
		t.Fatalf("BOX: %v %v", f, err)
	}
	if f, err := unitFactor(it, "PCS"); err != nil || f != 1 {
		t.Fatalf("PCS: %v %v", f, err)
	}
	if _, err := unitFactor(it, "CASE"); err == nil || !strings.Contains(err.Error(), "BOX") {
		t.Fatalf("unknown unit must list the known ones: %v", err)
	}
	b := []clients.Batch{{BatchNo: "OLD", ExpiryDate: "2026-10-01", Qty: 100}, {BatchNo: "B2", ExpiryDate: "2027-03-01", Qty: 10},
		{BatchNo: "B1", ExpiryDate: "2026-12-01", Qty: 5}, {BatchNo: "NOEXP", Qty: 100}}
	if got := fefo(b, "2026-10-23", 4); got != "B1" {
		t.Fatalf("FEFO must skip expired and pick the earliest expiry: %s", got)
	}
	if got := fefo(b, "2026-10-23", 8); got != "B2" {
		t.Fatalf("FEFO must skip a batch that is too small: %s", got)
	}
	if got := fefo(b, "2026-10-23", 50); got != "NOEXP" {
		t.Fatalf("no-expiry batch last: %s", got)
	}
	if got := fefo(b, "2026-10-23", 500); got != "" {
		t.Fatalf("nothing fits: %s", got)
	}
}

func TestSerialCounts(t *testing.T) {
	l := LineInput{Name: "Phone", Serials: []string{" a1 ", "A2"}}
	l.Qty, l.factor = 2, 1
	if err := checkSerialShape(0, &l); err != nil || l.Serials[0] != "A1" {
		t.Fatalf("serials are trimmed and upper-cased: %v %v", l.Serials, err)
	}
	if err := checkSerialCounts([]LineInput{l}); err != nil {
		t.Fatal(err)
	}
	l.Qty = 3
	if err := checkSerialCounts([]LineInput{l}); err == nil {
		t.Fatal("3 units with 2 serials must fail")
	}
	dup := []LineInput{{Name: "Phone", Serials: []string{"A1"}}, {Name: "Phone", Serials: []string{"A1"}}}
	if err := checkSerialsUnique(dup); err == nil {
		t.Fatal("the same serial twice must fail")
	}
}

func TestSerialGone(t *testing.T) {
	for st, want := range map[string]bool{"sold": true, "returned_to_vendor": true, "written_off": true, "in_stock": false, "unknown": false} {
		if serialGone(st) != want {
			t.Errorf("%s: want %v", st, want)
		}
	}
}
