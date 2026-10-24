package returns

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
)

// GSTR1Sections are the CSV sections of GSTR-1 (?section=).
var GSTR1Sections = []string{"b2b", "b2cl", "b2cs", "cdnr", "cdnur", "exp", "nil", "hsn_b2b", "hsn_b2c", "docs"}

func rt(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func qty(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// WriteGSTR1SectionCSV writes one section with columns following the GSTN
// offline-tool Excel template of that section.
func WriteGSTR1SectionCSV(w io.Writer, g GSTR1, section string) error {
	cw := csv.NewWriter(w)
	put := func(cols ...string) { _ = cw.Write(cols) }
	s := func(r Rs) string { return r.String() }
	switch section {
	case "b2b":
		put("GSTIN/UIN of Recipient", "Invoice Number", "Invoice date", "Invoice Value", "Place Of Supply", "Reverse Charge",
			"Invoice Type", "Rate", "Taxable Value", "Integrated Tax", "Central Tax", "State/UT Tax", "Cess Amount")
		for _, b := range g.B2B {
			for _, inv := range b.Inv {
				for _, it := range inv.Itms {
					d := it.ItmDet
					put(b.Ctin, inv.Inum, inv.Idt, s(inv.Val), inv.Pos, inv.Rchrg, inv.InvTyp, rt(d.Rt), s(d.Txval), s(d.Iamt), s(d.Camt), s(d.Samt), s(d.Csamt))
				}
			}
		}
	case "b2cl":
		put("Invoice Number", "Invoice date", "Invoice Value", "Place Of Supply", "Rate", "Taxable Value", "Integrated Tax", "Cess Amount")
		for _, b := range g.B2CL {
			for _, inv := range b.Inv {
				for _, it := range inv.Itms {
					d := it.ItmDet
					put(inv.Inum, inv.Idt, s(inv.Val), b.Pos, rt(d.Rt), s(d.Txval), s(d.Iamt), s(d.Csamt))
				}
			}
		}
	case "b2cs":
		put("Type", "Supply Type", "Place Of Supply", "Rate", "Taxable Value", "Integrated Tax", "Central Tax", "State/UT Tax", "Cess Amount")
		for _, b := range g.B2CS {
			put(b.Typ, b.SplyTy, b.Pos, rt(b.Rt), s(b.Txval), s(b.Iamt), s(b.Camt), s(b.Samt), s(b.Csamt))
		}
	case "cdnr":
		put("GSTIN/UIN of Recipient", "Note Number", "Note Date", "Note Type", "Place Of Supply", "Reverse Charge", "Note Supply Type",
			"Note Value", "Rate", "Taxable Value", "Integrated Tax", "Central Tax", "State/UT Tax", "Cess Amount")
		for _, c := range g.CDNR {
			for _, n := range c.Nt {
				for _, it := range n.Itms {
					d := it.ItmDet
					put(c.Ctin, n.NtNum, n.NtDt, n.Ntty, n.Pos, n.Rchrg, n.InvTyp, s(n.Val), rt(d.Rt), s(d.Txval), s(d.Iamt), s(d.Camt), s(d.Samt), s(d.Csamt))
				}
			}
		}
	case "cdnur":
		put("UR Type", "Note Number", "Note Date", "Note Type", "Place Of Supply", "Note Value", "Rate", "Taxable Value", "Integrated Tax", "Cess Amount")
		for _, n := range g.CDNUR {
			for _, it := range n.Itms {
				d := it.ItmDet
				put(n.Typ, n.NtNum, n.NtDt, n.Ntty, n.Pos, s(n.Val), rt(d.Rt), s(d.Txval), s(d.Iamt), s(d.Csamt))
			}
		}
	case "exp":
		put("Export Type", "Invoice Number", "Invoice date", "Invoice Value", "Port Code", "Shipping Bill Number", "Shipping Bill Date",
			"Rate", "Taxable Value", "Integrated Tax", "Cess Amount")
		for _, e := range g.EXP {
			for _, inv := range e.Inv {
				for _, it := range inv.Itms {
					put(e.ExpTyp, inv.Inum, inv.Idt, s(inv.Val), inv.Sbpcode, inv.Sbnum, inv.Sbdt, rt(it.Rt), s(it.Txval), s(it.Iamt), s(it.Csamt))
				}
			}
		}
	case "nil":
		put("Description", "Nil Rated Supplies", "Exempted (other than nil rated/non GST supply)", "Non-GST Supplies")
		label := map[string]string{"INTRB2B": "Inter-State supplies to registered persons", "INTRAB2B": "Intra-State supplies to registered persons",
			"INTRB2C": "Inter-State supplies to unregistered persons", "INTRAB2C": "Intra-State supplies to unregistered persons"}
		for _, n := range g.Nil.Inv {
			put(label[n.SplyTy], s(n.NilAmt), s(n.ExptAmt), s(n.NgsupAmt))
		}
	case "hsn_b2b", "hsn_b2c":
		rows := g.HSN.B2B
		if section == "hsn_b2c" {
			rows = g.HSN.B2C
		}
		put("HSN", "Description", "UQC", "Total Quantity", "Rate", "Taxable Value", "Integrated Tax Amount", "Central Tax Amount", "State/UT Tax Amount", "Cess Amount")
		for _, h := range rows {
			put(h.HsnSc, h.Desc, h.Uqc, qty(h.Qty), rt(h.Rt), s(h.Txval), s(h.Iamt), s(h.Camt), s(h.Samt), s(h.Csamt))
		}
	case "docs":
		put("Nature of Document", "Sr. No. From", "Sr. No. To", "Total Number", "Cancelled", "Net Issued")
		for _, d := range g.DocIssue.DocDet {
			for _, r := range d.Docs {
				put(d.DocTyp, r.From, r.To, strconv.Itoa(r.Totnum), strconv.Itoa(r.Cancel), strconv.Itoa(r.NetIssue))
			}
		}
	default:
		return fmt.Errorf("unknown section %q", section)
	}
	cw.Flush()
	return cw.Error()
}

// WriteGSTR1CSV writes every section into one file, one row per document and
// rate (or bucket), led by a section column — the review sheet accountants use.
func WriteGSTR1CSV(w io.Writer, g GSTR1) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"section", "gstin_or_pos", "number", "date", "invoice_value", "rate", "taxable", "igst", "cgst", "sgst", "cess"})
	s := func(r Rs) string { return r.String() }
	row := func(sec, key, num, date string, val Rs, d ItmDet) {
		_ = cw.Write([]string{sec, key, num, date, s(val), rt(d.Rt), s(d.Txval), s(d.Iamt), s(d.Camt), s(d.Samt), s(d.Csamt)})
	}
	for _, b := range g.B2B {
		for _, inv := range b.Inv {
			for _, it := range inv.Itms {
				row("b2b", b.Ctin, inv.Inum, inv.Idt, inv.Val, it.ItmDet)
			}
		}
	}
	for _, b := range g.B2CL {
		for _, inv := range b.Inv {
			for _, it := range inv.Itms {
				row("b2cl", b.Pos, inv.Inum, inv.Idt, inv.Val, it.ItmDet)
			}
		}
	}
	for _, b := range g.B2CS {
		row("b2cs", b.Pos, b.SplyTy, "", 0, ItmDet{Rt: b.Rt, Txval: b.Txval, Iamt: b.Iamt, Camt: b.Camt, Samt: b.Samt, Csamt: b.Csamt})
	}
	for _, c := range g.CDNR {
		for _, n := range c.Nt {
			for _, it := range n.Itms {
				row("cdnr", c.Ctin, n.NtNum, n.NtDt, n.Val, it.ItmDet)
			}
		}
	}
	for _, n := range g.CDNUR {
		for _, it := range n.Itms {
			row("cdnur", n.Typ+" "+n.Pos, n.NtNum, n.NtDt, n.Val, it.ItmDet)
		}
	}
	for _, e := range g.EXP {
		for _, inv := range e.Inv {
			for _, it := range inv.Itms {
				row("exp", e.ExpTyp, inv.Inum, inv.Idt, inv.Val, ItmDet{Rt: it.Rt, Txval: it.Txval, Iamt: it.Iamt, Csamt: it.Csamt})
			}
		}
	}
	for _, n := range g.Nil.Inv {
		row("nil", n.SplyTy, "exempt", "", 0, ItmDet{Txval: n.ExptAmt})
		row("nil", n.SplyTy, "nil_rated", "", 0, ItmDet{Txval: n.NilAmt})
		row("nil", n.SplyTy, "non_gst", "", 0, ItmDet{Txval: n.NgsupAmt})
	}
	for _, part := range []struct {
		sec  string
		rows []HSNRow
	}{{"hsn_b2b", g.HSN.B2B}, {"hsn_b2c", g.HSN.B2C}} {
		for _, h := range part.rows {
			row(part.sec, h.HsnSc, h.Uqc, qty(h.Qty), 0, ItmDet{Rt: h.Rt, Txval: h.Txval, Iamt: h.Iamt, Camt: h.Camt, Samt: h.Samt, Csamt: h.Csamt})
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteGSTR3BCSV writes the 3B tables and the payable table as rows.
func WriteGSTR3BCSV(w io.Writer, g GSTR3B) error {
	cw := csv.NewWriter(w)
	s := func(r Rs) string { return r.String() }
	_ = cw.Write([]string{"table", "description", "taxable", "igst", "cgst", "sgst", "cess"})
	h := func(t, d string, x Head) {
		_ = cw.Write([]string{t, d, s(x.Txval), s(x.Iamt), s(x.Camt), s(x.Samt), s(x.Csamt)})
	}
	t := func(tb, d string, x TaxHead) {
		h(tb, d, Head{Iamt: x.Iamt, Camt: x.Camt, Samt: x.Samt, Csamt: x.Csamt})
	}
	sd := g.SupDetails
	h("3.1(a)", "Outward taxable supplies (other than zero rated, nil rated and exempted)", sd.OsupDet)
	h("3.1(b)", "Outward taxable supplies (zero rated)", sd.OsupZero)
	h("3.1(c)", "Other outward supplies (nil rated, exempted)", sd.OsupNilExmp)
	h("3.1(d)", "Inward supplies (liable to reverse charge)", sd.IsupRev)
	h("3.1(e)", "Non-GST outward supplies", sd.OsupNongst)
	for _, u := range g.InterSup.UnregDetails {
		h("3.2", "Supplies to unregistered persons, POS "+u.Pos, Head{Txval: u.Txval, Iamt: u.Iamt})
	}
	desc := map[string]string{"IMPG": "4(A)(1) Import of goods", "IMPS": "4(A)(2) Import of services",
		"ISRC": "4(A)(3) Inward supplies liable to reverse charge", "ISD": "4(A)(4) Inward supplies from ISD", "OTH": "4(A)(5) All other ITC"}
	for _, r := range g.ItcElg.ItcAvl {
		t("4(A)", desc[r.Ty], r.TaxHead)
	}
	rev := map[string]string{"RUL": "4(B)(1) As per rules 38, 42 & 43 and section 17(5)", "OTH": "4(B)(2) Others"}
	for _, r := range g.ItcElg.ItcRev {
		t("4(B)", rev[r.Ty], r.TaxHead)
	}
	t("4(C)", "Net ITC available", g.ItcElg.ItcNet)
	inelg := map[string]string{"RUL": "4(D)(1) ITC reclaimed (reversed under 4(B)(2) earlier)", "OTH": "4(D)(2) Ineligible under 16(4) & POS rules"}
	for _, r := range g.ItcElg.ItcInelg {
		t("4(D)", inelg[r.Ty], r.TaxHead)
	}
	for _, r := range g.InwardSup.IsupDetails {
		label := "From composition, exempt and nil rated supply"
		if r.Ty == "NONGST" {
			label = "Non-GST supply"
		}
		_ = cw.Write([]string{"5", label + " (inter / intra)", "", s(r.Inter), s(r.Intra), "", ""})
	}
	c := g.Computation
	tx := func(d string, x Taxes) { h("6.1", d, Head{Iamt: x.IGST, Camt: x.CGST, Samt: x.SGST, Csamt: x.Cess}) }
	tx("Tax liability (forward charge)", c.Liability)
	tx("Opening ITC", c.OpeningITC)
	tx("Current ITC (4C)", c.CurrentITC)
	tx("ITC utilised (by credit head)", c.ITCUtilised)
	tx("Payable in cash (forward charge)", c.CashPayable)
	tx("Reverse charge (cash only)", c.ReverseCharge)
	tx("Total cash payable", c.TotalCash)
	tx("ITC carried forward", c.ITCCarryFwd)
	cw.Flush()
	return cw.Error()
}
