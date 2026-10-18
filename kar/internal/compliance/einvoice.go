package compliance

import (
	"errors"
	"fmt"

	"github.com/taraju/kar/internal/clients"
)

// EInvoice is the IRP INV-01 schema v1.1 payload (fields Taraju populates).
type EInvoice struct {
	Version    string         `json:"Version"`
	TranDtls   EInvTran       `json:"TranDtls"`
	DocDtls    EInvDoc        `json:"DocDtls"`
	SellerDtls EInvParty      `json:"SellerDtls"`
	BuyerDtls  EInvBuyer      `json:"BuyerDtls"`
	ItemList   []EInvItem     `json:"ItemList"`
	ValDtls    EInvValues     `json:"ValDtls"`
	RefDtls    *EInvReference `json:"RefDtls,omitempty"`
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
	SlNo       string  `json:"SlNo"`
	PrdDesc    string  `json:"PrdDesc"`
	IsServc    string  `json:"IsServc"`
	HsnCd      string  `json:"HsnCd"`
	Qty        float64 `json:"Qty"`
	Unit       string  `json:"Unit"`
	UnitPrice  float64 `json:"UnitPrice"`
	TotAmt     float64 `json:"TotAmt"`
	Discount   float64 `json:"Discount"`
	AssAmt     float64 `json:"AssAmt"`
	GstRt      float64 `json:"GstRt"`
	IgstAmt    float64 `json:"IgstAmt"`
	CgstAmt    float64 `json:"CgstAmt"`
	SgstAmt    float64 `json:"SgstAmt"`
	CesRt      float64 `json:"CesRt"`
	CesAmt     float64 `json:"CesAmt"`
	TotItemVal float64 `json:"TotItemVal"`
}

type EInvValues struct {
	AssVal    float64 `json:"AssVal"`
	CgstVal   float64 `json:"CgstVal"`
	SgstVal   float64 `json:"SgstVal"`
	IgstVal   float64 `json:"IgstVal"`
	CesVal    float64 `json:"CesVal"`
	Discount  float64 `json:"Discount"`
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

// BuildEInvoice maps a B2B sale (INV) or sales return (CRN) to IRP JSON.
// orig is the original invoice for a credit note (may be nil).
func BuildEInvoice(inv clients.Invoice, seller, buyer Party, orig *clients.Invoice) (EInvoice, error) {
	if inv.Status != "posted" {
		return EInvoice{}, errors.New("only posted invoices can be registered")
	}
	typ := map[string]string{"sale": "INV", "sale_return": "CRN"}[inv.DocType]
	if typ == "" {
		return EInvoice{}, fmt.Errorf("e-invoicing applies to sales and credit notes, not %s", inv.DocType)
	}
	if inv.PartyGSTIN == "" || buyer.GSTIN == "" {
		return EInvoice{}, errors.New("e-invoice is only for B2B: the buyer has no GSTIN")
	}
	if seller.GSTIN == "" {
		return EInvoice{}, errors.New("store GSTIN is required")
	}
	if seller.Pincode < 100000 || buyer.Pincode < 100000 {
		return EInvoice{}, errors.New("6-digit seller and buyer PIN codes are required")
	}
	dt, err := NICDate(inv.InvoiceDate)
	if err != nil {
		return EInvoice{}, err
	}
	e := EInvoice{
		Version:  "1.1",
		TranDtls: EInvTran{TaxSch: "GST", SupTyp: "B2B", RegRev: "N", IgstOnIntra: "N"},
		DocDtls:  EInvDoc{Typ: typ, No: inv.Number, Dt: dt},
	}
	party := func(p Party) EInvParty {
		a1, a2 := splitAddress(p.Address1)
		if a1 == "" {
			a1 = p.Place
		}
		loc := p.Place
		if loc == "" {
			loc = a1
		}
		return EInvParty{Gstin: p.GSTIN, LglNm: p.LegalName, TrdNm: p.TradeName, Addr1: a1, Addr2: a2,
			Loc: truncate(loc, 50), Pin: p.Pincode, Stcd: p.StateCode, Ph: p.Phone}
	}
	e.SellerDtls = party(seller)
	e.BuyerDtls = EInvBuyer{EInvParty: party(buyer), Pos: inv.PlaceOfSupply}

	for i, l := range inv.Lines {
		if l.HSN == "" {
			return EInvoice{}, fmt.Errorf("line %d (%s) has no HSN code", l.LineNo, l.Name)
		}
		gross := l.TaxablePaise + l.DiscountPaise
		unitPrice := 0.0
		if l.Qty > 0 {
			unitPrice = round(Rupees(gross)/l.Qty, 3)
		}
		isServ := "N"
		if IsService(l.HSN) {
			isServ = "Y"
		}
		e.ItemList = append(e.ItemList, EInvItem{
			SlNo: fmt.Sprint(i + 1), PrdDesc: truncate(l.Name, 300), IsServc: isServ, HsnCd: l.HSN,
			Qty: round(l.Qty, 3), Unit: UQC(l.Unit), UnitPrice: unitPrice, TotAmt: Rupees(gross),
			Discount: Rupees(l.DiscountPaise), AssAmt: Rupees(l.TaxablePaise), GstRt: RateFromBP(l.TaxRateBP),
			IgstAmt: Rupees(l.IGSTPaise), CgstAmt: Rupees(l.CGSTPaise), SgstAmt: Rupees(l.SGSTPaise),
			CesRt: RateFromBP(l.CessRateBP), CesAmt: Rupees(l.CessPaise), TotItemVal: Rupees(l.TotalPaise),
		})
	}
	e.ValDtls = EInvValues{
		AssVal: Rupees(inv.TaxablePaise), CgstVal: Rupees(inv.CGSTPaise), SgstVal: Rupees(inv.SGSTPaise),
		IgstVal: Rupees(inv.IGSTPaise), CesVal: Rupees(inv.CessPaise), RndOffAmt: Rupees(inv.RoundOffPaise),
		TotInvVal: Rupees(inv.GrandTotalPaise),
	}
	if orig != nil {
		od, err := NICDate(orig.InvoiceDate)
		if err == nil {
			e.RefDtls = &EInvReference{PrecDocDtls: []EInvPrecDoc{{InvNo: orig.Number, InvDt: od}}}
		}
	}
	return e, nil
}
