package returns

import (
	"bytes"
	"strings"
	"testing"

	"github.com/taraju/kar/internal/clients"
)

func line(hsn string, rate int, taxable int64, inter bool) clients.Line {
	tax := taxable * int64(rate) / 10000
	l := clients.Line{HSN: hsn, Name: "item " + hsn, Unit: "pcs", Qty: 1, TaxRateBP: rate, TaxablePaise: taxable}
	if inter {
		l.IGSTPaise = tax
	} else {
		l.CGSTPaise, l.SGSTPaise = tax/2, tax/2
	}
	l.TotalPaise = taxable + tax
	return l
}

func inv(num, docType, gstin, pos string, lines ...clients.Line) clients.Invoice {
	i := clients.Invoice{Number: num, DocType: docType, PartyGSTIN: gstin, PlaceOfSupply: pos,
		InvoiceDate: "2026-10-10", Status: "posted", Lines: lines}
	for _, l := range lines {
		i.TaxablePaise += l.TaxablePaise
		i.GrandTotalPaise += l.TotalPaise
		i.IGSTPaise += l.IGSTPaise
		i.CGSTPaise += l.CGSTPaise
		i.SGSTPaise += l.SGSTPaise
	}
	return i
}

func TestPeriod(t *testing.T) {
	f, to, err := Period("022028")
	if err != nil || f != "2028-02-01" || to != "2028-02-29" {
		t.Fatalf("%s %s %v", f, to, err)
	}
	if _, _, err := Period("2026-10"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGSTR1Bucketing(t *testing.T) {
	cancelled := inv("INV/26-27/00004", "sale", "", "27")
	cancelled.Status = "cancelled"
	invs := []clients.Invoice{
		// B2B: two rates on one invoice
		inv("INV/26-27/00001", "sale", "29AAACB1234C1Z5", "29", line("9403", 1800, 100000, true), line("1905", 500, 20000, true)),
		// B2CL: unregistered, inter-state, above ₹1,00,000
		inv("INV/26-27/00002", "sale", "", "07", line("8528", 1800, 15000000, true)),
		// B2CS: unregistered, inter-state but small
		inv("INV/26-27/00003", "sale", "", "07", line("8528", 1800, 50000, true)),
		// B2CS intra
		inv("INV/26-27/00005", "sale", "", "27", line("1905", 500, 10000, false)),
		cancelled,
		// CDNR: credit note to the registered buyer
		inv("CN/26-27/00001", "sale_return", "29AAACB1234C1Z5", "29", line("1905", 500, 20000, true)),
		// B2CS-netted credit note to walk-in buyer
		inv("CN/26-27/00002", "sale_return", "", "27", line("1905", 500, 4000, false)),
		// purchases are ignored by GSTR-1
		inv("PUR/26-27/00001", "purchase", "27AAPFU0939F1ZV", "27", line("1905", 500, 99999, false)),
	}
	g := BuildGSTR1("27AAPFU0939F1ZV", "102026", "27", 10000000, invs)

	if len(g.B2B) != 1 || len(g.B2B[0].Inv) != 1 || len(g.B2B[0].Inv[0].Itms) != 2 {
		t.Fatalf("b2b %+v", g.B2B)
	}
	if g.B2B[0].Inv[0].Itms[0].ItmDet.Rt != 5 || g.B2B[0].Inv[0].Idt != "10-10-2026" {
		t.Fatalf("b2b rates should be sorted ascending: %+v", g.B2B[0].Inv[0])
	}
	if len(g.B2CL) != 1 || g.B2CL[0].Pos != "07" {
		t.Fatalf("b2cl %+v", g.B2CL)
	}
	if len(g.B2CS) != 2 {
		t.Fatalf("b2cs %+v", g.B2CS)
	}
	intra := g.B2CS[1]                                                     // pos 27 sorts after 07
	if intra.Pos != "27" || intra.SplyTy != "INTRA" || intra.Txval != 60 { // 100 - 40 credit note
		t.Fatalf("b2cs intra %+v", intra)
	}
	if g.B2CS[0].SplyTy != "INTER" || g.B2CS[0].Txval != 500 {
		t.Fatalf("b2cs inter %+v", g.B2CS[0])
	}
	if len(g.CDNR) != 1 || g.CDNR[0].Nt[0].Ntty != "C" {
		t.Fatalf("cdnr %+v", g.CDNR)
	}
	// HSN 1905 @5%: 200 + 100 - 200 - 40 = 60
	var found bool
	for _, h := range g.HSN.Data {
		if h.HsnSc == "1905" && h.Rt == 5 {
			found = true
			if h.Txval != 60 || h.Qty != 0 || h.Uqc != "PCS" {
				t.Fatalf("hsn 1905 %+v", h)
			}
		}
	}
	if !found {
		t.Fatal("hsn 1905 missing")
	}
	dd := g.DocIssue.DocDet
	if len(dd) != 2 || dd[0].DocNum != 1 || dd[0].Docs[0].Totnum != 5 || dd[0].Docs[0].Cancel != 1 ||
		dd[0].Docs[0].From != "INV/26-27/00001" || dd[0].Docs[0].To != "INV/26-27/00005" {
		t.Fatalf("doc summary %+v", dd)
	}

	var buf bytes.Buffer
	if err := WriteGSTR1CSV(&buf, g); err != nil || !strings.Contains(buf.String(), "b2b,29AAACB1234C1Z5,INV/26-27/00001") {
		t.Fatalf("csv: %v\n%s", err, buf.String())
	}
}

func TestGSTR3BAndSetOff(t *testing.T) {
	invs := []clients.Invoice{
		inv("INV1", "sale", "", "27", line("1905", 1800, 100000, false)),       // CGST 9000 SGST 9000
		inv("INV2", "sale", "", "07", line("8528", 1800, 50000, true)),         // IGST 9000, 3.2 unreg
		inv("INV3", "sale", "", "27", line("0401", 0, 7000, false)),            // nil rated
		inv("PUR1", "purchase", "27X", "27", line("8528", 1800, 150000, true)), // ITC IGST 27000
	}
	g := BuildGSTR3B("27AAPFU0939F1ZV", "102026", "27", invs)
	if g.SupDetails.OsupDet.Txval != 1500 || g.SupDetails.OsupDet.Camt != 90 || g.SupDetails.OsupDet.Iamt != 90 {
		t.Fatalf("3.1a %+v", g.SupDetails.OsupDet)
	}
	if g.SupDetails.OsupNilExmp.Txval != 70 {
		t.Fatalf("3.1c %+v", g.SupDetails.OsupNilExmp)
	}
	if len(g.InterSup.UnregDetails) != 1 || g.InterSup.UnregDetails[0].Pos != "07" {
		t.Fatalf("3.2 %+v", g.InterSup.UnregDetails)
	}
	// IGST credit 270 covers IGST 90, CGST 90, SGST 90 exactly.
	c := g.Computation
	if c.CashPayable != (Taxes{}) || c.ITCCarryFwd != (Taxes{}) || c.ITCUtilised.IGST != 270 {
		t.Fatalf("computation %+v", c)
	}
}

func TestSetOffNoCrossCGSTSGST(t *testing.T) {
	// liability: IGST 0, CGST 100, SGST 100 ; credit: CGST 300 only
	c := setOff([4]int64{0, 10000, 10000, 0}, [4]int64{0, 30000, 0, 0})
	if c.CashPayable.SGST != 100 || c.CashPayable.CGST != 0 || c.ITCCarryFwd.CGST != 200 {
		t.Fatalf("CGST credit must not pay SGST: %+v", c)
	}
	// SGST credit flows to IGST after SGST.
	c = setOff([4]int64{5000, 0, 1000, 0}, [4]int64{0, 0, 4000, 0})
	if c.CashPayable.IGST != 20 || c.ITCUtilised.SGST != 40 {
		t.Fatalf("%+v", c)
	}
}
