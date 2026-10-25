package returns

import (
	"context"
	"slices"

	"github.com/maapan/mool/httpx"
)

// Our registration in Maharashtra (27) and a second one that must never leak in.
const (
	ourGSTIN   = "27AAPFU0939F1ZV"
	otherGSTIN = "27AAPFU0939F2ZU"
)

type lineOpt func(*Line)

func cat(c string) lineOpt  { return func(l *Line) { l.TaxCategory = c } }
func unit(u string) lineOpt { return func(l *Line) { l.Unit = u } }
func itc(e string) lineOpt  { return func(l *Line) { l.ITCEligibility = e } }
func noTax() lineOpt {
	return func(l *Line) { l.IGSTPaise, l.CGSTPaise, l.SGSTPaise, l.TotalPaise = 0, 0, 0, l.TaxablePaise }
}
func line(hsn string, rateBP int, taxable int64, inter bool, opts ...lineOpt) Line {
	l := Line{HSN: hsn, Name: "item " + hsn, Unit: "pcs", Qty: 1, TaxRateBP: rateBP, TaxablePaise: taxable, TaxCategory: "taxable"}
	for _, o := range opts {
		o(&l)
	}
	if l.TaxCategory != "taxable" {
		l.TaxRateBP = 0
	}
	tax := taxable * int64(l.TaxRateBP) / 10000
	if inter {
		l.IGSTPaise = tax
	} else {
		l.CGSTPaise, l.SGSTPaise = tax/2, tax/2
	}
	l.TotalPaise = taxable + tax
	for _, o := range opts {
		o(&l) // noTax must run after the tax is computed
	}
	return l
}

type invOpt func(*Invoice)

func supply(c string) invOpt { return func(i *Invoice) { i.SupplyCategory = c } }
func rcm() invOpt            { return func(i *Invoice) { i.ReverseCharge = true } }
func status(s string) invOpt { return func(i *Invoice) { i.Status = s } }
func ref(id string) invOpt   { return func(i *Invoice) { i.RefInvoiceID = id } }
func store(g string) invOpt  { return func(i *Invoice) { i.StoreGSTIN = g } }
func party(st string) invOpt { return func(i *Invoice) { i.PartyState = st } }
func supInv(no, dt string) invOpt {
	return func(i *Invoice) { i.SupplierInvoiceNo, i.SupplierInvoiceDate = no, dt }
}
func shipping(port, no, dt string) invOpt {
	return func(i *Invoice) { i.PortCode, i.ShippingBillNo, i.ShippingBillDate = port, no, dt }
}

var seq int

func doc(id, number, docType, partyGSTIN, pos string, lines []Line, opts ...invOpt) Invoice {
	seq++
	i := Invoice{ID: id, Number: number, DocType: docType, PartyGSTIN: partyGSTIN, PlaceOfSupply: pos, StoreState: "27",
		StoreGSTIN: ourGSTIN, InvoiceDate: "2026-10-10", Status: "posted", Lines: lines}
	for n := range i.Lines {
		i.Lines[n].LineNo = n + 1
	}
	for _, o := range opts {
		o(&i)
	}
	for _, l := range i.Lines {
		i.TaxablePaise += l.TaxablePaise
		i.IGSTPaise += l.IGSTPaise
		i.CGSTPaise += l.CGSTPaise
		i.SGSTPaise += l.SGSTPaise
		i.CessPaise += l.CessPaise
		i.GrandTotalPaise += l.TotalPaise
	}
	if i.ReverseCharge {
		i.GrandTotalPaise = i.TaxablePaise // the recipient pays the tax
	}
	i.InterState = i.IGSTPaise > 0
	return i
}

func ls(l ...Line) []Line { return l }

// fixture is one month of a Maharashtra business covering every GSTR-1 and
// GSTR-3B bucket. Amounts are paise; expected values are worked out in the tests.
func fixture() []Invoice {
	return []Invoice{
		// B2B regular, inter-state, two rates + an exempt line (Table 8 INTRB2B)
		doc("inv1", "INV/26-27/00001", "sale", "29AAACB1234C1Z5", "29",
			ls(line("9403", 1800, 100000, true), line("1905", 500, 20000, true), line("0713", 0, 5000, true, cat("exempt")))),
		// B2B reverse charge (4B), intra-state service
		doc("inv2", "INV/26-27/00002", "sale", "27AABCU9603R1ZM", "27", ls(line("9965", 500, 200000, false)), rcm()),
		// SEZ without payment (6B, SEWOP)
		doc("inv3", "INV/26-27/00003", "sale", "29AAGCS1234A1Z1", "29", ls(line("8471", 1800, 500000, true, noTax())),
			supply("sez_without_payment")),
		// Deemed export (6C, DE)
		doc("inv4", "INV/26-27/00004", "sale", "24AAACD1234E1Z2", "24", ls(line("8471", 1800, 300000, true)), supply("deemed_export")),
		// B2CL: unregistered, inter-state, above ₹1,00,000
		doc("inv5", "INV/26-27/00005", "sale", "", "07", ls(line("8528", 1800, 15000000, true))),
		// B2CS inter-state, small
		doc("inv6", "INV/26-27/00006", "sale", "", "07", ls(line("8528", 1800, 50000, true))),
		// B2CS intra-state + exempt milk (Table 8 INTRAB2C)
		doc("inv7", "INV/26-27/00007", "sale", "", "27", ls(line("1905", 500, 10000, false), line("0401", 0, 7000, false, cat("exempt"), unit("ltr")))),
		doc("inv8", "INV/26-27/00008", "sale", "", "27", ls(line("1905", 500, 99900, false)), status("cancelled")),
		// INV/26-27/00009 was never issued (burned offline block) → Table 13 gap
		// Exports with and without payment (6A)
		doc("inv10", "INV/26-27/00010", "sale", "", "96", ls(line("6109", 500, 400000, true)), supply("export_with_payment"),
			shipping("INBOM4", "1234567", "2026-10-12")),
		doc("inv11", "INV/26-27/00011", "sale", "", "96", ls(line("6109", 500, 300000, true, noTax())), supply("export_without_payment")),
		// Registered buyer, only nil-rated and non-GST lines → Table 8 INTRAB2B, no B2B entry
		doc("inv12", "INV/26-27/00012", "sale", "27AAACB1111C1Z1", "27",
			ls(line("1006", 0, 50000, false, cat("nil_rated"), unit("kg")), line("2710", 0, 30000, false, cat("non_gst"), unit("ltr")))),

		// Credit notes
		doc("cn1", "CN/26-27/00001", "sale_return", "29AAACB1234C1Z5", "29", ls(line("1905", 500, 20000, true)), ref("inv1")), // CDNR
		doc("cn2", "CN/26-27/00002", "sale_return", "", "27", ls(line("1905", 500, 4000, false)), ref("inv7")),                // B2CS netting
		doc("cn3", "CN/26-27/00003", "sale_return", "", "07", ls(line("8528", 1800, 1000000, true)), ref("inv5")),             // CDNUR B2CL
		doc("cn4", "CN/26-27/00004", "sale_return", "", "07", ls(line("8528", 1800, 10000, true)), ref("inv6")),               // B2CS netting
		doc("cn5", "CN/26-27/00005", "sale_return", "", "96", ls(line("6109", 500, 100000, true)), ref("inv10"),
			supply("export_with_payment")), // CDNUR EXPWP

		// Purchases (GSTR-3B only)
		doc("p1", "PUR/26-27/00001", "purchase", "27AAPFU1111A1Z1", "27", ls(
			line("1905", 500, 100000, false),
			line("9403", 1800, 50000, false, itc("ineligible_17_5")),
			line("1006", 0, 6000, false, cat("nil_rated"))), supInv("st-42", "2026-10-04"), party("27")),
		doc("p2", "PUR/26-27/00002", "purchase", "27AABCG2222B1Z2", "27", ls(line("9965", 500, 200000, false)), rcm(),
			supInv("GTA 118", "2026-10-07"), party("27")),
		doc("p3", "PUR/26-27/00003", "purchase", "29AAACB1234C1Z5", "27", ls(
			line("8528", 1800, 1000000, true),
			line("0401", 0, 2000, true, cat("exempt")),
			line("2710", 0, 3000, true, cat("non_gst"))), supInv("BE/26-27/901", "2026-10-09"), party("29")),
		doc("p4", "DN/26-27/00001", "purchase_return", "29AAACB1234C1Z5", "27", ls(line("8528", 1800, 100000, true)), ref("p3"), party("29")),

		// Another GSTIN of the same business: excluded from both returns.
		doc("x1", "INV/26-27/00001", "sale", "", "27", ls(line("1905", 500, 777700, false)), store(otherGSTIN)),
	}
}

// fakeSource serves a fixed invoice list like aadesh would, in small pages.
type fakeSource struct {
	invs  []Invoice
	calls int
}

func (f *fakeSource) EachInvoice(_ context.Context, q InvoiceQuery, fn func(Invoice) error) error {
	f.calls++
	for _, inv := range f.invs {
		if q.GSTIN != "" && inv.StoreGSTIN != q.GSTIN && inv.StoreGSTIN != otherGSTIN {
			continue // aadesh filters by store_gstin; let the other GSTIN through to prove the client-side guard
		}
		if len(q.DocTypes) > 0 && !slices.Contains(q.DocTypes, inv.DocType) {
			continue
		}
		if inv.InvoiceDate < q.From || inv.InvoiceDate > q.To {
			continue
		}
		c := inv
		if !q.IncludeLines {
			c.Lines = nil
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSource) Invoice(_ context.Context, id string) (Invoice, error) {
	for _, inv := range f.invs {
		if inv.ID == id {
			return inv, nil
		}
	}
	return Invoice{}, httpx.NotFound("invoice %s not found", id)
}

type fakeRegs []Registration

func (f fakeRegs) Registrations(context.Context, string) ([]Registration, error) { return f, nil }

var regs = fakeRegs{
	{ID: "r1", GSTIN: ourGSTIN, StateCode: "27", IsPrimary: true, Status: "active"},
	{ID: "r2", GSTIN: otherGSTIN, StateCode: "27", Status: "active"},
	{ID: "r3", GSTIN: "", StateCode: "29", Status: "active"}, // unregistered branch
}
