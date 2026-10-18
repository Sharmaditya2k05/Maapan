package compliance

import (
	"strings"
	"testing"

	"github.com/taraju/kar/internal/clients"
)

func sampleInvoice() clients.Invoice {
	return clients.Invoice{
		ID: "inv-1", DocType: "sale", Number: "INV/26-27/00042", InvoiceDate: "2026-10-05", Status: "posted",
		PartyGSTIN: "29AAACB1234C1Z5", PlaceOfSupply: "29", StoreState: "27", InterState: true,
		TaxablePaise: 6000000, IGSTPaise: 1080000, RoundOffPaise: 0, GrandTotalPaise: 7080000,
		Lines: []clients.Line{
			{LineNo: 1, Name: "Steel Almirah", HSN: "9403", Unit: "pcs", Qty: 2, TaxRateBP: 1800,
				TaxablePaise: 6000000, IGSTPaise: 1080000, TotalPaise: 7080000},
		},
	}
}

func TestNICDate(t *testing.T) {
	d, err := NICDate("2026-04-01")
	if err != nil || d != "01/04/2026" {
		t.Fatalf("got %q %v", d, err)
	}
	if GSTNDate("2026-04-01") != "01-04-2026" {
		t.Fatal("gstn date")
	}
	if _, err := NICDate("01/04/2026"); err == nil {
		t.Fatal("expected error for non-ISO input")
	}
}

func TestBuildEWayOutwardInterState(t *testing.T) {
	our := Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme Traders", Address1: "12 MG Road, Pune", Place: "Pune", Pincode: 411001, StateCode: "27"}
	buyer := Party{GSTIN: "29AAACB1234C1Z5", LegalName: "Bharat Stores", Address1: "4 Church St", Place: "Bengaluru", Pincode: 560001, StateCode: "29"}
	f, warns, below, err := BuildEWay(sampleInvoice(), our, buyer,
		Transport{TransMode: "road", TransDistance: 840, VehicleNo: "MH 12 AB 1234"}, 5000000)
	if err != nil {
		t.Fatal(err)
	}
	if below || len(warns) != 0 {
		t.Fatalf("₹70,800 is above threshold: %v %v", below, warns)
	}
	b := f.BillLists[0]
	if f.Version != EWayVersion || b.SupplyType != "O" || b.DocType != "INV" || b.DocDate != "05/10/2026" {
		t.Fatalf("header %+v", b)
	}
	if b.FromStateCode != 27 || b.ToStateCode != 29 || b.ToGstin != "29AAACB1234C1Z5" || b.TransMode != "1" {
		t.Fatalf("parties %+v", b)
	}
	if b.VehicleNo != "MH12AB1234" || b.TransDistance != "840" {
		t.Fatalf("transport %+v", b)
	}
	it := b.ItemList[0]
	if it.HsnCode != 9403 || it.IgstRate != 18 || it.CgstRate != 0 || it.QtyUnit != "PCS" || it.TaxableAmount != 60000 {
		t.Fatalf("item %+v", it)
	}
	if b.TotInvValue != 70800 || b.IgstValue != 10800 {
		t.Fatalf("values %+v", b)
	}
}

func TestBuildEWayBelowThresholdAndUnregistered(t *testing.T) {
	inv := sampleInvoice()
	inv.PartyGSTIN, inv.InterState, inv.PlaceOfSupply = "", false, "27"
	inv.TaxablePaise, inv.IGSTPaise, inv.CGSTPaise, inv.SGSTPaise, inv.GrandTotalPaise = 100000, 0, 9000, 9000, 118000
	inv.Lines[0].IGSTPaise, inv.Lines[0].CGSTPaise, inv.Lines[0].SGSTPaise = 0, 9000, 9000
	our := Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme", Pincode: 411001, StateCode: "27"}
	buyer := Party{LegalName: "Walk-in", Pincode: 411002, StateCode: "27"}
	f, warns, below, err := BuildEWay(inv, our, buyer, Transport{TransporterID: "27AAPFU0939F1ZV"}, 5000000)
	if err != nil {
		t.Fatal(err)
	}
	b := f.BillLists[0]
	if !below || len(warns) != 1 || !strings.Contains(warns[0], "below") {
		t.Fatalf("expected below-threshold warning: %v", warns)
	}
	if b.ToGstin != "URP" || b.ItemList[0].CgstRate != 9 || b.ItemList[0].SgstRate != 9 || b.TransMode != "" {
		t.Fatalf("unexpected %+v", b)
	}
}

func TestBuildEWayValidation(t *testing.T) {
	our := Party{GSTIN: "27AAPFU0939F1ZV", Pincode: 411001, StateCode: "27"}
	buyer := Party{Pincode: 560001, StateCode: "29"}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, buyer, Transport{TransMode: "road"}, 0); err == nil {
		t.Fatal("road without vehicle must fail")
	}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, Party{StateCode: "29"}, Transport{TransporterID: "X"}, 0); err == nil {
		t.Fatal("missing pincode must fail")
	}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, buyer, Transport{TransMode: "road", VehicleNo: "ABC"}, 0); err == nil {
		t.Fatal("bad vehicle number must fail")
	}
}

func TestBuildEInvoice(t *testing.T) {
	seller := Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme Traders", Address1: "12 MG Road", Place: "Pune", Pincode: 411001, StateCode: "27"}
	buyer := Party{GSTIN: "29AAACB1234C1Z5", LegalName: "Bharat Stores", Address1: "4 Church St", Place: "Bengaluru", Pincode: 560001, StateCode: "29"}
	e, err := BuildEInvoice(sampleInvoice(), seller, buyer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != "1.1" || e.DocDtls.Typ != "INV" || e.DocDtls.Dt != "05/10/2026" || e.BuyerDtls.Pos != "29" {
		t.Fatalf("header %+v", e)
	}
	it := e.ItemList[0]
	if it.UnitPrice != 30000 || it.AssAmt != 60000 || it.GstRt != 18 || it.IgstAmt != 10800 || it.IsServc != "N" {
		t.Fatalf("item %+v", it)
	}
	if e.ValDtls.TotInvVal != 70800 {
		t.Fatalf("values %+v", e.ValDtls)
	}
	b2c := sampleInvoice()
	b2c.PartyGSTIN = ""
	if _, err := BuildEInvoice(b2c, seller, Party{Pincode: 560001}, nil); err == nil {
		t.Fatal("B2C must be rejected")
	}
}

func TestHelpers(t *testing.T) {
	if UQC("kg") != "KGS" || UQC("weird") != "OTH" {
		t.Fatal("uqc")
	}
	if PincodeFromAddress("Shop 4, MG Road, Pune 411001, MH") != 411001 || PincodeFromAddress("no pin") != 0 {
		t.Fatal("pincode")
	}
}
