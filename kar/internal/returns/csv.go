package returns

import (
	"encoding/csv"
	"fmt"
	"io"
)

func f2(v float64) string { return fmt.Sprintf("%.2f", v) }

// WriteGSTR1CSV flattens GSTR-1 into one row per invoice/rate (or bucket) for
// accountants who review in Excel.
func WriteGSTR1CSV(w io.Writer, g GSTR1) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"section", "gstin_or_pos", "number", "date", "invoice_value", "rate", "taxable", "igst", "cgst", "sgst", "cess"})
	row := func(sec, key, num, date string, val float64, d ItemDet) {
		_ = cw.Write([]string{sec, key, num, date, f2(val), f2(d.Rt), f2(d.Txval), f2(d.Iamt), f2(d.Camt), f2(d.Samt), f2(d.Csamt)})
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
		row("b2cs", b.Pos, b.SplyTy, "", 0, ItemDet{Rt: b.Rt, Txval: b.Txval, Iamt: b.Iamt, Camt: b.Camt, Samt: b.Samt, Csamt: b.Csamt})
	}
	for _, c := range g.CDNR {
		for _, n := range c.Nt {
			for _, it := range n.Itms {
				row("cdnr", c.Ctin, n.NtNum, n.NtDt, n.Val, it.ItmDet)
			}
		}
	}
	for _, h := range g.HSN.Data {
		row("hsn", h.HsnSc, h.Uqc, f2(h.Qty), 0, ItemDet{Rt: h.Rt, Txval: h.Txval, Iamt: h.Iamt, Camt: h.Camt, Samt: h.Samt, Csamt: h.Csamt})
	}
	cw.Flush()
	return cw.Error()
}

// WriteGSTR3BCSV writes the 3B tables as rows.
func WriteGSTR3BCSV(w io.Writer, g GSTR3B) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"table", "description", "taxable", "igst", "cgst", "sgst", "cess"})
	h := func(t, d string, x Head) {
		_ = cw.Write([]string{t, d, f2(x.Txval), f2(x.Iamt), f2(x.Camt), f2(x.Samt), f2(x.Csamt)})
	}
	h("3.1(a)", "Outward taxable supplies", g.SupDetails.OsupDet)
	h("3.1(c)", "Nil rated / exempt", g.SupDetails.OsupNilExmp)
	for _, u := range g.InterSup.UnregDetails {
		h("3.2", "Inter-state to unregistered, POS "+u.Pos, Head{Txval: u.Txval, Iamt: u.Iamt})
	}
	h("4(A)(5)", "All other ITC", g.ItcElg.ItcNet)
	c := g.Computation
	t := func(d string, x Taxes) { h("6.1", d, Head{Iamt: x.IGST, Camt: x.CGST, Samt: x.SGST, Csamt: x.Cess}) }
	t("Tax liability", c.Liability)
	t("ITC utilised", c.ITCUtilised)
	t("Payable in cash", c.CashPayable)
	t("ITC carried forward", c.ITCCarryFwd)
	cw.Flush()
	return cw.Error()
}
