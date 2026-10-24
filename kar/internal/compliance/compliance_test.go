package compliance

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/mool/gst"
)

func sampleInvoice() clients.Invoice {
	return clients.Invoice{
		ID: "inv-1", DocType: "sale", Number: "INV/26-27/00042", InvoiceDate: "2026-10-05", Status: "posted",
		PartyGSTIN: "29AAACB1234C1Z5", PlaceOfSupply: "29", StoreState: "27", InterState: true,
		TaxablePaise: 6000000, IGSTPaise: 1080000, RoundOffPaise: 0, GrandTotalPaise: 7080000,
		Lines: []clients.Line{
			{LineNo: 1, Name: "Steel Almirah", HSN: "940320", Unit: "pcs", Qty: 2, TaxRateBP: 1800,
				TaxablePaise: 6000000, IGSTPaise: 1080000, TotalPaise: 7080000},
		},
	}
}

var (
	seller = Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme Traders", Address1: "12 MG Road", Place: "Pune", Pincode: 411001, StateCode: "27", Phone: "+91 98765-43210"}
	buyer  = Party{GSTIN: "29AAACB1234C1Z5", LegalName: "Bharat Stores", Address1: "4 Church St", Place: "Bengaluru", Pincode: 560001, StateCode: "29"}
)

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
	if b.VehicleNo != "MH12AB1234" || b.TransDistance != "840" || b.TransactionType != 1 {
		t.Fatalf("transport %+v", b)
	}
	it := b.ItemList[0]
	if it.HsnCode != 940320 || it.IgstRate != 18 || it.CgstRate != 0 || it.QtyUnit != "PCS" || it.TaxableAmount != 60000 {
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
	inv.Lines[0].IGSTPaise, inv.Lines[0].CGSTPaise, inv.Lines[0].SGSTPaise, inv.Lines[0].TaxablePaise = 0, 9000, 9000, 100000
	our := Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme", Pincode: 411001, StateCode: "27"}
	walkin := Party{LegalName: "Walk-in", Pincode: 411002, StateCode: "27"}
	f, warns, below, err := BuildEWay(inv, our, walkin, Transport{TransporterID: "27AAPFU0939F1ZV"}, 5000000)
	if err != nil {
		t.Fatal(err)
	}
	b := f.BillLists[0]
	if !below || len(warns) != 1 || !strings.Contains(warns[0], "below") || !strings.Contains(warns[0], "intra-state (27)") {
		t.Fatalf("expected intra-state below-threshold warning: %v", warns)
	}
	if b.ToGstin != "URP" || b.ItemList[0].CgstRate != 9 || b.ItemList[0].SgstRate != 9 || b.TransMode != "" {
		t.Fatalf("unexpected %+v", b)
	}
}

func TestBuildEWayValidationAndVehicles(t *testing.T) {
	our := Party{GSTIN: "27AAPFU0939F1ZV", Pincode: 411001, StateCode: "27"}
	to := Party{Pincode: 560001, StateCode: "29"}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, to, Transport{TransMode: "road"}, 0); err == nil {
		t.Fatal("road without vehicle must fail")
	}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, Party{StateCode: "29"}, Transport{TransporterID: "X"}, 0); err == nil {
		t.Fatal("missing pincode must fail")
	}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, to, Transport{TransMode: "road", VehicleNo: "ABC"}, 0); err == nil {
		t.Fatal("bad vehicle number must fail")
	}
	for _, v := range []string{"22BH1234AA", "22 BH 1234 A", "TR22AB1234567", "DL1CAB1234"} {
		f, _, _, err := BuildEWay(sampleInvoice(), our, to, Transport{Mode: "1", DistanceKM: 10, VehicleNo: v}, 0)
		if err != nil {
			t.Errorf("%s: %v", v, err)
			continue
		}
		if f.BillLists[0].VehicleNo != gsp.NormalizeVehicle(v) || f.BillLists[0].TransDistance != "10" {
			t.Errorf("%s: %+v", v, f.BillLists[0])
		}
	}
	if _, _, _, err := BuildEWay(sampleInvoice(), our, to, Transport{Mode: "2", DistanceKM: 10}, 0); err == nil {
		t.Fatal("rail without transport document must fail")
	}
}

func TestBuildEWayShipToAndDispatch(t *testing.T) {
	our := Party{GSTIN: "27AAPFU0939F1ZV", LegalName: "Acme", Address1: "12 MG Road", Place: "Pune", Pincode: 411001, StateCode: "27"}
	tr := Transport{Mode: "1", DistanceKM: 150, VehicleNo: "MH12AB1234",
		ShipTo: &Location{GSTIN: "29AAACB1234C1Z5", Name: "Bharat Godown", Address1: "Plot 7, KIADB", Place: "Hubli", Pincode: 580030, StateCode: "29"}}
	f, _, _, err := BuildEWay(sampleInvoice(), our, buyer, tr, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := f.BillLists[0]
	if b.TransactionType != 2 || b.ToPincode != 580030 || b.ToPlace != "Hubli" || b.ShipToTradeName != "Bharat Godown" || b.ToGstin != buyer.GSTIN {
		t.Fatalf("ship-to %+v", b)
	}
	tr.DispatchFrom = &Location{Name: "Acme Warehouse", Address1: "Gat 12, Chakan", Place: "Chakan", Pincode: 410501, StateCode: "27"}
	f, _, _, err = BuildEWay(sampleInvoice(), our, buyer, tr, 0)
	if err != nil || f.BillLists[0].TransactionType != 4 || f.BillLists[0].FromPincode != 410501 {
		t.Fatalf("both: %v %+v", err, f.BillLists[0])
	}
}

func TestThresholdTableAndConsignmentValue(t *testing.T) {
	if EWayThreshold("27", "27", 5000000) != 10000000 || EWayThreshold("27", "29", 5000000) != 5000000 ||
		EWayThreshold("29", "29", 5000000) != 5000000 || EWayThreshold("07", "07", 0) != 10000000 || EWayThreshold("24", "29", 0) != DefaultEWayThresholdPaise {
		t.Fatal("threshold table")
	}
	for _, s := range []string{"07", "08", "10", "19", "27", "33"} {
		if IntraStateThresholdPaise[s] != 10000000 {
			t.Errorf("state %s", s)
		}
	}
	inv := sampleInvoice()
	inv.Lines = append(inv.Lines,
		clients.Line{LineNo: 2, Name: "Fresh milk", HSN: "040110", TaxCategory: gst.CatExempt, TaxablePaise: 900000, TotalPaise: 900000},
		clients.Line{LineNo: 3, Name: "Installation", HSN: "998729", TaxablePaise: 100000, IGSTPaise: 18000, TotalPaise: 118000})
	if v := ConsignmentValuePaise(inv); v != 7080000 {
		t.Fatalf("consignment value excludes exempt goods and services: %d", v)
	}
	// Intra-state Maharashtra ₹70,800 is below the ₹1 lakh state limit.
	mh := sampleInvoice()
	mh.InterState, mh.PlaceOfSupply = false, "27"
	_, warns, below, err := BuildEWay(mh, seller, Party{GSTIN: "27AAACB1234C1Z1", Pincode: 411002, StateCode: "27"},
		Transport{Mode: "1", DistanceKM: 20, VehicleNo: "MH12AB1234"}, 5000000)
	if err != nil || !below || len(warns) != 1 {
		t.Fatalf("MH intra-state: %v %v %v", err, below, warns)
	}
}

func TestCheckDocAge(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, gsp.IST)
	if err := CheckDocAge("2026-04-27", now); err != nil {
		t.Fatal(err)
	}
	if err := CheckDocAge("2026-04-20", now); err == nil || !strings.Contains(err.Error(), "180") {
		t.Fatalf("180-day rule: %v", err)
	}
}

func TestBuildEInvoiceB2B(t *testing.T) {
	e, err := BuildEInvoice(sampleInvoice(), seller, buyer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != "1.1" || e.DocDtls.Typ != "INV" || e.DocDtls.Dt != "05/10/2026" || e.BuyerDtls.Pos != "29" ||
		e.TranDtls.SupTyp != "B2B" || e.TranDtls.RegRev != "N" || e.TranDtls.IgstOnIntra != "N" {
		t.Fatalf("header %+v", e)
	}
	it := e.ItemList[0]
	if it.UnitPrice != 30000 || it.AssAmt != 60000 || it.GstRt != 18 || it.IgstAmt != 10800 || it.IsServc != "N" || it.TotItemVal != 70800 {
		t.Fatalf("item %+v", it)
	}
	if e.ValDtls.TotInvVal != 70800 || e.ValDtls.AssVal != 60000 || e.RefDtls != nil {
		t.Fatalf("values %+v", e.ValDtls)
	}
	if e.SellerDtls.Ph != "9876543210" {
		t.Fatalf("phone must be digits only: %q", e.SellerDtls.Ph)
	}
	b2c := sampleInvoice()
	b2c.PartyGSTIN = ""
	if _, err := BuildEInvoice(b2c, seller, Party{Pincode: 560001}, nil); err == nil {
		t.Fatal("B2C must be rejected")
	}
}

func TestBuildEInvoiceHSNRules(t *testing.T) {
	inv := sampleInvoice()
	inv.Lines[0].HSN = "9403"
	_, err := BuildEInvoice(inv, seller, buyer, nil)
	if err == nil || !strings.Contains(err.Error(), "4 digits") || !strings.Contains(err.Error(), "at least 6") {
		t.Fatalf("4-digit HSN: %v", err)
	}
	for hsn, ok := range map[string]bool{"94032010": true, "940320": true, "99831": false, "94O320": false, "": false} {
		inv.Lines[0].HSN = hsn
		if _, err := BuildEInvoice(inv, seller, buyer, nil); (err == nil) != ok {
			t.Errorf("HSN %q: %v", hsn, err)
		}
	}
}

func TestBuildEInvoiceSupplyTypesAndRCM(t *testing.T) {
	cases := map[string]string{"": "B2B", gst.SupplySEZWithPayment: "SEZWP", gst.SupplySEZWithoutPayment: "SEZWOP",
		gst.SupplyExportWithPayment: "EXPWP", gst.SupplyExportWithoutPayment: "EXPWOP", gst.SupplyDeemedExport: "DEXP"}
	for cat, want := range cases {
		inv := sampleInvoice()
		inv.SupplyCategory = cat
		if strings.HasSuffix(cat, "without_payment") {
			inv.IGSTPaise, inv.Lines[0].IGSTPaise, inv.GrandTotalPaise = 0, 0, 6000000
		}
		b := buyer
		if strings.HasPrefix(cat, "export_") {
			inv.PartyGSTIN = ""
			b = Party{LegalName: "Global Imports LLC", Address1: "1 Harbour Rd", Place: "Dubai"}
		}
		e, err := BuildEInvoice(inv, seller, b, nil)
		if err != nil {
			t.Errorf("%q: %v", cat, err)
			continue
		}
		if e.TranDtls.SupTyp != want {
			t.Errorf("%q: SupTyp %s, want %s", cat, e.TranDtls.SupTyp, want)
		}
		if strings.HasPrefix(want, "EXP") && (e.BuyerDtls.Gstin != "URP" || e.BuyerDtls.Stcd != "96" || e.BuyerDtls.Pos != "96" || e.BuyerDtls.Pin != 999999) {
			t.Errorf("%q: export buyer %+v", cat, e.BuyerDtls)
		}
	}
	if _, err := SupplyType("weird"); err == nil {
		t.Fatal("unknown category must fail")
	}
	inv := sampleInvoice()
	inv.ReverseCharge = true
	e, err := BuildEInvoice(inv, seller, buyer, nil)
	if err != nil || e.TranDtls.RegRev != "Y" {
		t.Fatalf("RCM: %v %+v", err, e.TranDtls)
	}
	// SEZ unit in the seller's own state: IGST on an intra-state supply.
	sez := sampleInvoice()
	sez.SupplyCategory, sez.PlaceOfSupply = gst.SupplySEZWithPayment, "27"
	e, err = BuildEInvoice(sez, seller, Party{GSTIN: "27AAACB1234C1Z1", LegalName: "SEZ Unit", Address1: "SEEPZ", Place: "Mumbai", Pincode: 400096, StateCode: "27"}, nil)
	if err != nil || e.TranDtls.IgstOnIntra != "Y" {
		t.Fatalf("SEZ intra: %v %+v", err, e.TranDtls)
	}
}

func TestBuildEInvoiceNotes(t *testing.T) {
	orig := sampleInvoice()
	cn := sampleInvoice()
	cn.DocType, cn.Number, cn.RefInvoiceID = "sale_return", "CN/26-27/00003", orig.ID
	if _, err := BuildEInvoice(cn, seller, buyer, nil); err == nil {
		t.Fatal("credit note without the original must fail")
	}
	e, err := BuildEInvoice(cn, seller, buyer, &orig)
	if err != nil {
		t.Fatal(err)
	}
	if e.DocDtls.Typ != "CRN" || e.RefDtls == nil || e.RefDtls.PrecDocDtls[0].InvNo != "INV/26-27/00042" || e.RefDtls.PrecDocDtls[0].InvDt != "05/10/2026" {
		t.Fatalf("CRN %+v %+v", e.DocDtls, e.RefDtls)
	}
	dn := cn
	dn.DocType, dn.Number = "purchase_return", "DN/26-27/00001"
	e, err = BuildEInvoice(dn, seller, buyer, &orig)
	if err != nil || e.DocDtls.Typ != "DBN" {
		t.Fatalf("DBN: %v %+v", err, e.DocDtls)
	}
	p := sampleInvoice()
	p.DocType = "purchase"
	if _, err := BuildEInvoice(p, seller, buyer, nil); err == nil {
		t.Fatal("purchases are not e-invoiced by the buyer")
	}
}

func TestBuildEInvoiceAddressAndUTF8(t *testing.T) {
	long := "Shop No. 14, Ground Floor, Shree Ganesh Commercial Complex, Near Old Bus Stand, Opposite Municipal Garden, Station Road, Shivajinagar, Pune"
	s := seller
	s.Address1 = long
	s.LegalName = strings.Repeat("श्री गणेश ट्रेडर्स ", 10) // 180 Devanagari characters
	e, err := BuildEInvoice(sampleInvoice(), s, buyer, nil)
	if err != nil {
		t.Fatal(err)
	}
	sd := e.SellerDtls
	if utf8.RuneCountInString(sd.Addr1) > 100 || sd.Addr2 == "" || utf8.RuneCountInString(sd.Addr2) > 100 {
		t.Fatalf("address split: %q | %q", sd.Addr1, sd.Addr2)
	}
	if !strings.HasSuffix(sd.Addr1, "Near Old Bus Stand") || !strings.HasPrefix(sd.Addr2, "Opposite Municipal Garden") {
		t.Fatalf("split at a comma: %q | %q", sd.Addr1, sd.Addr2)
	}
	if !utf8.ValidString(sd.LglNm) || utf8.RuneCountInString(sd.LglNm) > 100 {
		t.Fatalf("legal name must be cut on a character boundary: %q", sd.LglNm)
	}
	if got := Truncate("नमस्ते दुनिया", 5); got != "नमस्त" || !utf8.ValidString(got) {
		t.Fatalf("Truncate = %q", got)
	}
	a1, a2 := splitAddress(strings.Repeat("क", 250))
	if !utf8.ValidString(a1) || !utf8.ValidString(a2) || utf8.RuneCountInString(a1) != 100 || utf8.RuneCountInString(a2) != 100 {
		t.Fatalf("hard split: %d %d", utf8.RuneCountInString(a1), utf8.RuneCountInString(a2))
	}
	// Loc keeps house / sector numbers.
	s = seller
	s.Place = ""
	s.Address1 = "Plot 4, Sector 15, Noida 201301"
	e, err = BuildEInvoice(sampleInvoice(), s, buyer, nil)
	if err != nil || e.SellerDtls.Loc != "Noida" {
		t.Fatalf("loc: %v %q", err, e.SellerDtls.Loc)
	}
	s.Address1 = "Plot 4, Sector 15, 201301"
	if e, _ := BuildEInvoice(sampleInvoice(), s, buyer, nil); e.SellerDtls.Loc != "Sector 15" {
		t.Fatalf("loc must not strip the sector number: %q", e.SellerDtls.Loc)
	}
}

func TestEInvoiceApplicable(t *testing.T) {
	inv := sampleInvoice()
	if ok, _ := EInvoiceApplicable(true, inv); !ok {
		t.Fatal("B2B on an applicable registration")
	}
	if ok, reason := EInvoiceApplicable(false, inv); ok || !strings.Contains(reason, "5 crore") {
		t.Fatalf("registration not applicable: %s", reason)
	}
	b2c := inv
	b2c.PartyGSTIN = ""
	if ok, _ := EInvoiceApplicable(true, b2c); ok {
		t.Fatal("B2C is never e-invoiced")
	}
	exp := b2c
	exp.SupplyCategory = gst.SupplyExportWithoutPayment
	if ok, _ := EInvoiceApplicable(true, exp); !ok {
		t.Fatal("exports are e-invoiced without a GSTIN")
	}
	p := inv
	p.DocType = "purchase"
	if ok, _ := EInvoiceApplicable(true, p); ok {
		t.Fatal("purchases are not")
	}
}

func TestHelpers(t *testing.T) {
	if UQC("kg") != "KGS" || UQC("weird") != "OTH" {
		t.Fatal("uqc")
	}
	if PincodeFromAddress("Shop 4, MG Road, Pune 411001, MH") != 411001 || PincodeFromAddress("no pin") != 0 {
		t.Fatal("pincode")
	}
	for in, want := range map[string]string{"+91 98765 43210": "9876543210", "098765 43210": "9876543210", "020-2567 8901": "2025678901",
		"12345": "", "": "", "+1 (415) 555-0100": "14155550100"} {
		if got := Phone(in); got != want {
			t.Errorf("Phone(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"12 MG Road, Shivajinagar, Pune, Maharashtra, 411005": "Pune",
		"4 Church St, Bengaluru 560001":                       "Bengaluru",
		"House 21, Sector 15":                                 "Sector 15",
		"":                                                    "",
	} {
		if got := PlaceFromAddress(in); got != want {
			t.Errorf("PlaceFromAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
