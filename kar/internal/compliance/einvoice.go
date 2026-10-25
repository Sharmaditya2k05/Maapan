package compliance

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/maapan/kar/internal/clients"
	"github.com/maapan/mool/gst"
)

// EInvoice is the IRP INV-01 schema v1.1 payload (fields Maapan populates).
type EInvoice struct {
	Version    string         `json:"Version"`
	TranDtls   EInvTran       `json:"TranDtls"`
	DocDtls    EInvDoc        `json:"DocDtls"`
	SellerDtls EInvParty      `json:"SellerDtls"`
	BuyerDtls  EInvBuyer      `json:"BuyerDtls"`
	ItemList   []EInvItem     `json:"ItemList"`
	ValDtls    EInvValues     `json:"ValDtls"`
	RefDtls    *EInvReference `json:"RefDtls,omitempty"`
	ExpDtls    *EInvExport    `json:"ExpDtls,omitempty"`
}

type EInvTran struct {
	TaxSch      string `json:"TaxSch"`
	SupTyp      string `json:"SupTyp"`
	RegRev      string `json:"RegRev"`
	IgstOnIntra string `json:"IgstOnIntra"`
}

type EInvDoc struct {
	Typ string `json:"Typ"` // INV | CRN | DBN
	No  string `json:"No"`
	Dt  string `json:"Dt"`
}

type EInvParty struct {
	Gstin string `json:"Gstin"`
	LglNm string `json:"LglNm"`
	TrdNm string `json:"TrdNm,omitempty"`
	Addr1 string `json:"Addr1"`
	Addr2 string `json:"Addr2,omitempty"`
	Loc   string `json:"Loc"`
	Pin   int    `json:"Pin"`
	Stcd  string `json:"Stcd"`
	Ph    string `json:"Ph,omitempty"`
}

type EInvBuyer struct {
	EInvParty
	Pos string `json:"Pos"`
}

type EInvItem struct {
	SlNo          string  `json:"SlNo"`
	PrdDesc       string  `json:"PrdDesc"`
	IsServc       string  `json:"IsServc"`
	HsnCd         string  `json:"HsnCd"`
	Qty           float64 `json:"Qty"`
	Unit          string  `json:"Unit,omitempty"`
	UnitPrice     float64 `json:"UnitPrice"`
	TotAmt        float64 `json:"TotAmt"`
	Discount      float64 `json:"Discount"`
	AssAmt        float64 `json:"AssAmt"`
	GstRt         float64 `json:"GstRt"`
	IgstAmt       float64 `json:"IgstAmt"`
	CgstAmt       float64 `json:"CgstAmt"`
	SgstAmt       float64 `json:"SgstAmt"`
	CesRt         float64 `json:"CesRt"`
	CesAmt        float64 `json:"CesAmt"`
	CesNonAdvlAmt float64 `json:"CesNonAdvlAmt"`
	TotItemVal    float64 `json:"TotItemVal"`
}

type EInvValues struct {
	AssVal    float64 `json:"AssVal"`
	CgstVal   float64 `json:"CgstVal"`
	SgstVal   float64 `json:"SgstVal"`
	IgstVal   float64 `json:"IgstVal"`
	CesVal    float64 `json:"CesVal"`
	Discount  float64 `json:"Discount"`
	OthChrg   float64 `json:"OthChrg"`
	RndOffAmt float64 `json:"RndOffAmt"`
	TotInvVal float64 `json:"TotInvVal"`
}

type EInvReference struct {
	PrecDocDtls []EInvPrecDoc `json:"PrecDocDtls"`
}

type EInvPrecDoc struct {
	InvNo string `json:"InvNo"`
	InvDt string `json:"InvDt"`
}

// EInvExport is ExpDtls (optional for exports).
type EInvExport struct {
	ShipBNo string `json:"ShipBNo,omitempty"`
	ShipBDt string `json:"ShipBDt,omitempty"`
	Port    string `json:"Port,omitempty"`
	ForCur  string `json:"ForCur,omitempty"`
	CntCode string `json:"CntCode,omitempty"`
}

// EInvoiceOptions carries what the invoice itself does not hold.
type EInvoiceOptions struct {
	Original *clients.Invoice // the invoice a credit/debit note adjusts
	Export   *EInvExport      // shipping bill / country for exports
}

// IRP document types per aadesh doc type: sales → INV, sales returns (credit
// notes) → CRN, purchase returns raised as debit notes → DBN.
var irpDocType = map[string]string{"sale": "INV", "sale_return": "CRN", "purchase_return": "DBN"}

// IRPDocType maps an aadesh doc type to INV/CRN/DBN ("" when not e-invoiceable).
func IRPDocType(docType string) string { return irpDocType[docType] }

// SupplyType maps aadesh's supply_category to the IRP SupTyp.
func SupplyType(category string) (string, error) {
	switch category {
	case "", "regular", "b2b":
		return "B2B", nil
	case gst.SupplySEZWithPayment:
		return "SEZWP", nil
	case gst.SupplySEZWithoutPayment:
		return "SEZWOP", nil
	case gst.SupplyExportWithPayment:
		return "EXPWP", nil
	case gst.SupplyExportWithoutPayment:
		return "EXPWOP", nil
	case gst.SupplyDeemedExport:
		return "DEXP", nil
	}
	return "", fmt.Errorf("unknown supply_category %q", category)
}

// ZeroRatedOrDeemed reports whether a supply category is export / SEZ / deemed export.
func ZeroRatedOrDeemed(category string) bool {
	return strings.HasPrefix(category, "sez_") || strings.HasPrefix(category, "export_") || category == gst.SupplyDeemedExport
}

// EInvoiceApplicable decides whether an invoice must be registered on the IRP:
// the registration is e-invoice applicable (AATO above ₹5 crore, set by the
// owner) AND the buyer is registered or the supply is export / SEZ / deemed
// export. B2C supplies never are. The reason explains a "no".
func EInvoiceApplicable(registrationApplicable bool, inv clients.Invoice) (bool, string) {
	if IRPDocType(inv.DocType) == "" {
		return false, fmt.Sprintf("%s documents are not e-invoiced by the issuer", inv.DocType)
	}
	if !registrationApplicable {
		return false, "e-invoicing is not enabled for this registration (aggregate turnover up to ₹5 crore)"
	}
	if inv.BillOfSupply {
		return false, "bills of supply (composition) are not e-invoiced"
	}
	if ZeroRatedOrDeemed(inv.SupplyCategory) {
		return true, ""
	}
	if gst.Normalize(inv.PartyGSTIN) == "" {
		return false, "B2C invoices (buyer without GSTIN) are not e-invoiced"
	}
	return true, ""
}

var hsnDigits = regexp.MustCompile(`^[0-9]+$`)

// CheckHSN enforces the IRP rule: 6 or 8 digits (a 4-digit HSN is rejected).
func CheckHSN(lineNo int, name, hsn string) error {
	h := strings.TrimSpace(hsn)
	switch {
	case h == "":
		return fmt.Errorf("line %d (%s) has no HSN/SAC code; the IRP needs 6 or 8 digits", lineNo, name)
	case !hsnDigits.MatchString(h):
		return fmt.Errorf("line %d (%s): HSN/SAC %q must contain digits only", lineNo, name, h)
	case len(h) == 4:
		return fmt.Errorf("line %d (%s): HSN/SAC %s has 4 digits; e-invoices need at least 6 (e.g. %s00 or the exact 6/8-digit code) — update the item master", lineNo, name, h, h)
	case len(h) != 6 && len(h) != 8:
		return fmt.Errorf("line %d (%s): HSN/SAC %s must have 6 or 8 digits", lineNo, name, h)
	}
	return nil
}

// BuildEInvoice maps a sale (INV), credit note (CRN) or debit note (DBN) to
// IRP JSON. orig is the original invoice of a note (may be nil).
func BuildEInvoice(inv clients.Invoice, seller, buyer Party, orig *clients.Invoice) (EInvoice, error) {
	return BuildEInvoiceWith(inv, seller, buyer, EInvoiceOptions{Original: orig})
}

// BuildEInvoiceWith is BuildEInvoice with export details.
func BuildEInvoiceWith(inv clients.Invoice, seller, buyer Party, o EInvoiceOptions) (EInvoice, error) {
	if inv.Status != "" && inv.Status != "posted" {
		return EInvoice{}, errors.New("only posted invoices can be registered")
	}
	typ := IRPDocType(inv.DocType)
	if typ == "" {
		return EInvoice{}, fmt.Errorf("e-invoicing applies to sales, credit notes and debit notes, not %s", inv.DocType)
	}
	supTyp, err := SupplyType(inv.SupplyCategory)
	if err != nil {
		return EInvoice{}, err
	}
	export := strings.HasPrefix(supTyp, "EXP")
	if seller.GSTIN == "" || gst.ValidateGSTIN(seller.GSTIN) != nil {
		return EInvoice{}, errors.New("the store's registration has no valid GSTIN")
	}
	if !export && (gst.Normalize(inv.PartyGSTIN) == "" || buyer.GSTIN == "") {
		return EInvoice{}, errors.New("e-invoice needs a registered buyer (GSTIN) unless it is an export")
	}
	if seller.Pincode < 100000 || seller.Pincode > 999999 {
		return EInvoice{}, errors.New("the registration's 6-digit PIN code is required (set it in Settings → Registrations)")
	}
	if !export && (buyer.Pincode < 100000 || buyer.Pincode > 999999) {
		return EInvoice{}, errors.New("the buyer's 6-digit PIN code is required (add it to the party address)")
	}
	if inv.ReverseCharge && export {
		return EInvoice{}, errors.New("reverse charge does not apply to exports")
	}
	if utf8Len(inv.Number) > 16 {
		return EInvoice{}, fmt.Errorf("document number %q exceeds 16 characters", inv.Number)
	}
	dt, err := NICDate(inv.InvoiceDate)
	if err != nil {
		return EInvoice{}, fmt.Errorf("invoice_date: %w", err)
	}
	regRev := "N"
	if inv.ReverseCharge {
		regRev = "Y"
	}
	pos := inv.PlaceOfSupply
	if pos == "" {
		pos = buyer.StateCode
	}
	igstOnIntra := "N"
	if !export && pos == seller.StateCode && inv.IGSTPaise > 0 {
		igstOnIntra = "Y" // e.g. supply to an SEZ unit in the same state
	}
	e := EInvoice{
		Version:  "1.1",
		TranDtls: EInvTran{TaxSch: "GST", SupTyp: supTyp, RegRev: regRev, IgstOnIntra: igstOnIntra},
		DocDtls:  EInvDoc{Typ: typ, No: inv.Number, Dt: dt},
	}
	party := func(p Party) (EInvParty, error) {
		a1, a2 := splitAddress(p.Address1)
		if p.Address2 != "" && a2 == "" {
			a2 = Truncate(p.Address2, AddrLimit)
		}
		loc := strings.TrimSpace(p.Place)
		if loc == "" {
			loc = PlaceFromAddress(p.Address1)
		}
		if a1 == "" {
			a1 = loc
		}
		loc = Truncate(loc, 50)
		if utf8Len(loc) < 3 {
			return EInvParty{}, fmt.Errorf("%s: city / location is required (3-50 characters)", nameOr(p))
		}
		if a1 == "" {
			return EInvParty{}, fmt.Errorf("%s: address is required", nameOr(p))
		}
		lgl := Truncate(p.LegalName, 100)
		if utf8Len(lgl) < 3 {
			lgl = Truncate(p.TradeName, 100)
		}
		if utf8Len(lgl) < 3 {
			return EInvParty{}, fmt.Errorf("%s: legal name must be at least 3 characters", nameOr(p))
		}
		trd := Truncate(p.TradeName, 100)
		if trd == lgl {
			trd = ""
		}
		return EInvParty{Gstin: gst.Normalize(p.GSTIN), LglNm: lgl, TrdNm: trd, Addr1: a1, Addr2: a2, Loc: loc,
			Pin: p.Pincode, Stcd: p.StateCode, Ph: Phone(p.Phone)}, nil
	}
	if e.SellerDtls, err = party(seller); err != nil {
		return EInvoice{}, fmt.Errorf("seller %w", err)
	}
	b := buyer
	if export {
		b.GSTIN, b.StateCode, b.Pincode = "URP", "96", 999999
		pos = "96"
	}
	bp, err := party(b)
	if err != nil {
		return EInvoice{}, fmt.Errorf("buyer %w", err)
	}
	if export {
		bp.Gstin = "URP"
	}
	e.BuyerDtls = EInvBuyer{EInvParty: bp, Pos: pos}

	if len(inv.Lines) == 0 {
		return EInvoice{}, errors.New("invoice has no lines")
	}
	var ass, totItems, cgst, sgst, igst, cess float64
	for i, l := range inv.Lines {
		if err := CheckHSN(l.LineNo, l.Name, l.HSN); err != nil {
			return EInvoice{}, err
		}
		gross := l.TaxablePaise + l.DiscountPaise
		unitPrice := 0.0
		if l.Qty > 0 {
			unitPrice = round(Rupees(gross)/l.Qty, 3)
		}
		isServ, unit := "N", UQC(l.Unit)
		if IsService(l.HSN) {
			isServ, unit = "Y", ""
		}
		rate := RateFromBP(l.TaxRateBP)
		if c := l.TaxCategory; c == gst.CatExempt || c == gst.CatNilRated || c == gst.CatNonGST {
			rate = 0
		}
		nonAdvl := int64(math.Round(float64(l.CessPerUnitPaise) * l.Qty))
		if nonAdvl > l.CessPaise {
			nonAdvl = l.CessPaise
		}
		it := EInvItem{
			SlNo: fmt.Sprint(i + 1), PrdDesc: Truncate(l.Name, 300), IsServc: isServ, HsnCd: strings.TrimSpace(l.HSN),
			Qty: round(l.Qty, 3), Unit: unit, UnitPrice: unitPrice, TotAmt: Rupees(gross),
			Discount: Rupees(l.DiscountPaise), AssAmt: Rupees(l.TaxablePaise), GstRt: rate,
			IgstAmt: Rupees(l.IGSTPaise), CgstAmt: Rupees(l.CGSTPaise), SgstAmt: Rupees(l.SGSTPaise),
			CesRt: RateFromBP(l.CessRateBP), CesAmt: Rupees(l.CessPaise - nonAdvl), CesNonAdvlAmt: Rupees(nonAdvl),
			TotItemVal: Rupees(l.TaxablePaise + l.IGSTPaise + l.CGSTPaise + l.SGSTPaise + l.CessPaise),
		}
		e.ItemList = append(e.ItemList, it)
		ass += it.AssAmt
		totItems += it.TotItemVal
		cgst, sgst, igst, cess = cgst+it.CgstAmt, sgst+it.SgstAmt, igst+it.IgstAmt, cess+it.CesAmt+it.CesNonAdvlAmt
	}
	e.ValDtls = EInvValues{
		AssVal: round(ass, 2), CgstVal: round(cgst, 2), SgstVal: round(sgst, 2), IgstVal: round(igst, 2), CesVal: round(cess, 2),
		OthChrg: Rupees(inv.TCSPaise), RndOffAmt: Rupees(inv.RoundOffPaise),
	}
	e.ValDtls.TotInvVal = round(totItems+e.ValDtls.OthChrg+e.ValDtls.RndOffAmt, 2)
	if typ != "INV" {
		if o.Original == nil {
			return EInvoice{}, fmt.Errorf("%s needs the original invoice (ref_invoice_id) for RefDtls", typ)
		}
		od, err := NICDate(o.Original.InvoiceDate)
		if err != nil {
			return EInvoice{}, fmt.Errorf("original invoice date: %w", err)
		}
		e.RefDtls = &EInvReference{PrecDocDtls: []EInvPrecDoc{{InvNo: o.Original.Number, InvDt: od}}}
	}
	if export && o.Export != nil {
		x := *o.Export
		e.ExpDtls = &x
	}
	return e, nil
}

func utf8Len(s string) int { return len([]rune(s)) }

func nameOr(p Party) string {
	if p.LegalName != "" {
		return p.LegalName
	}
	if p.GSTIN != "" {
		return p.GSTIN
	}
	return "party"
}
