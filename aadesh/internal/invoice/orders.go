package invoice

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Order kinds: documents before the tax invoice / purchase bill. They never post
// to stock or books and publish no events; only the invoice/bill/note they are
// converted into does (see README "Document flow").
const (
	KindQuotation       = "quotation"
	KindProforma        = "proforma"
	KindSalesOrder      = "sales_order"
	KindDeliveryChallan = "delivery_challan"
	KindPurchaseOrder   = "purchase_order"
	KindGRN             = "grn"
)

var orderPrefix = map[string]string{
	KindQuotation: "QT", KindProforma: "PI", KindSalesOrder: "SO", KindDeliveryChallan: "DC", KindPurchaseOrder: "PO", KindGRN: "GRN",
}

// nextKinds lists what each order kind may be converted into.
var nextKinds = map[string][]string{
	KindQuotation:       {KindProforma, KindSalesOrder, KindDeliveryChallan, DocSale},
	KindProforma:        {KindSalesOrder, KindDeliveryChallan, DocSale},
	KindSalesOrder:      {KindDeliveryChallan, DocSale},
	KindDeliveryChallan: {DocSale},
	KindPurchaseOrder:   {KindGRN, DocPurchase},
	KindGRN:             {DocPurchase},
}

// Order statuses.
const (
	OrderOpen      = "open"      // nothing converted yet
	OrderPartial   = "partial"   // some quantity converted
	OrderCompleted = "completed" // every line fully converted
	OrderClosed    = "closed"    // short-closed by hand: the rest is not expected
	OrderCancelled = "cancelled"
)

var validChallanReason = map[string]bool{"": true, "supply": true, "job_work": true, "approval": true, "exhibition": true, "other": true}

// IsPurchaseKind reports whether an order kind is on the purchase side.
func IsPurchaseKind(kind string) bool { return kind == KindPurchaseOrder || kind == KindGRN }

func canConvert(from, to string) bool {
	for _, k := range nextKinds[from] {
		if k == to {
			return true
		}
	}
	return false
}

// OrderInput is the body of POST /v1/orders.
type OrderInput struct {
	Kind              string      `json:"kind"`
	StoreID           string      `json:"store_id,omitempty"`
	DocDate           string      `json:"doc_date,omitempty"`      // YYYY-MM-DD, default today IST
	ValidUntil        string      `json:"valid_until,omitempty"`   // quotation / proforma
	ExpectedDate      string      `json:"expected_date,omitempty"` // sales order delivery / PO arrival
	PartyID           string      `json:"party_id,omitempty"`
	Party             *PartyInput `json:"party,omitempty"`
	PlaceOfSupply     string      `json:"place_of_supply,omitempty"`
	SupplierRef       string      `json:"supplier_ref,omitempty"`   // GRN: supplier's delivery note
	ChallanReason     string      `json:"challan_reason,omitempty"` // delivery challan: supply | job_work | approval | exhibition | other
	Lines             []LineInput `json:"lines"`
	InvoiceDiscountBP int         `json:"invoice_discount_bp,omitempty"`
	Notes             string      `json:"notes,omitempty"`
	Terms             string      `json:"terms,omitempty"`
	RoundOff          *bool       `json:"round_off,omitempty"`
	ClientUUID        string      `json:"client_uuid,omitempty"`

	link *orderLink
}

// Validate checks the shape of an order.
func (in *OrderInput) Validate() error {
	if _, ok := orderPrefix[in.Kind]; !ok {
		return httpx.Validation("kind must be quotation, proforma, sales_order, delivery_challan, purchase_order or grn")
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		return httpx.Validation("a document needs 1 to 500 lines")
	}
	for i, l := range in.Lines {
		if strings.TrimSpace(l.Name) == "" {
			return httpx.Validation("line %d: name is required", i+1)
		}
		if l.Qty <= 0 {
			return httpx.Validation("line %d: qty must be positive", i+1)
		}
		if l.ITCEligibility != "" {
			return httpx.Validation("line %d: itc_eligibility belongs on the purchase bill", i+1)
		}
	}
	if IsPurchaseKind(in.Kind) && in.PartyID == "" {
		return httpx.Validation("purchase orders and GRNs need a supplier party_id")
	}
	if !validChallanReason[in.ChallanReason] {
		return httpx.Validation("challan_reason must be supply, job_work, approval, exhibition or other")
	}
	if in.ChallanReason != "" && in.Kind != KindDeliveryChallan {
		return httpx.Validation("challan_reason applies to delivery challans only")
	}
	if in.Party != nil && in.Party.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.Party.GSTIN); err != nil {
			return httpx.Validation("party gstin: %s", err.Error())
		}
	}
	for _, d := range []struct{ name, v string }{{"doc_date", in.DocDate}, {"valid_until", in.ValidUntil}, {"expected_date", in.ExpectedDate}} {
		if d.v != "" {
			if _, err := time.Parse(time.DateOnly, d.v); err != nil {
				return httpx.Validation("%s must be YYYY-MM-DD", d.name)
			}
		}
	}
	if in.InvoiceDiscountBP < 0 || in.InvoiceDiscountBP > 10000 {
		return httpx.Validation("invoice_discount_bp must be 0-10000")
	}
	if len(in.ClientUUID) > 64 {
		return httpx.Validation("client_uuid too long")
	}
	return nil
}

// Order is a stored pre-invoice document.
type Order struct {
	ID                string      `json:"id"`
	StoreID           string      `json:"store_id"`
	Kind              string      `json:"kind"`
	Number            string      `json:"number"`
	FY                string      `json:"fy"`
	DocDate           string      `json:"doc_date"`
	ValidUntil        *string     `json:"valid_until,omitempty"`
	ExpectedDate      *string     `json:"expected_date,omitempty"`
	PartyID           *string     `json:"party_id,omitempty"`
	PartyName         string      `json:"party_name"`
	PartyPhone        *string     `json:"party_phone,omitempty"`
	PartyGSTIN        *string     `json:"party_gstin,omitempty"`
	PartyState        *string     `json:"party_state,omitempty"`
	StoreState        string      `json:"store_state"`
	StoreGSTIN        *string     `json:"store_gstin,omitempty"`
	RegistrationID    *string     `json:"registration_id,omitempty"`
	PlaceOfSupply     string      `json:"place_of_supply"`
	InterState        bool        `json:"inter_state"`
	SupplierRef       *string     `json:"supplier_ref,omitempty"`
	ChallanReason     *string     `json:"challan_reason,omitempty"`
	Status            string      `json:"status"`
	TaxablePaise      int64       `json:"taxable_paise"`
	DiscountPaise     int64       `json:"discount_paise"`
	InvoiceDiscountBP int         `json:"invoice_discount_bp,omitempty"`
	CGSTPaise         int64       `json:"cgst_paise"`
	SGSTPaise         int64       `json:"sgst_paise"`
	IGSTPaise         int64       `json:"igst_paise"`
	CessPaise         int64       `json:"cess_paise"`
	RoundOffPaise     int64       `json:"round_off_paise"`
	GrandTotalPaise   int64       `json:"grand_total_paise"`
	AmountInWords     string      `json:"amount_in_words"`
	SourceOrderID     *string     `json:"source_order_id,omitempty"`
	Notes             *string     `json:"notes,omitempty"`
	Terms             *string     `json:"terms,omitempty"`
	ClientUUID        *string     `json:"client_uuid,omitempty"`
	CreatedBy         string      `json:"created_by"`
	CreatedAt         time.Time   `json:"created_at"`
	ClosedAt          *time.Time  `json:"closed_at,omitempty"`
	CloseReason       *string     `json:"close_reason,omitempty"`
	CancelledAt       *time.Time  `json:"cancelled_at,omitempty"`
	CancelReason      *string     `json:"cancel_reason,omitempty"`
	Lines             []OrderLine `json:"lines,omitempty"`
	ConvertedTo       []DocLink   `json:"converted_to,omitempty"` // documents made from this one
	MadeFrom          []DocLink   `json:"made_from,omitempty"`    // the document(s) this one was made from
}

// OrderLine is a stored order line with its open quantity.
type OrderLine struct {
	ID               string   `json:"id"`
	LineNo           int      `json:"line_no"`
	ItemID           *string  `json:"item_id,omitempty"`
	Name             string   `json:"name"`
	HSN              *string  `json:"hsn,omitempty"`
	Unit             string   `json:"unit"`
	UnitFactor       float64  `json:"unit_factor"`
	BaseQty          float64  `json:"base_qty"`
	BatchNo          *string  `json:"batch_no,omitempty"`
	Serials          []string `json:"serials,omitempty"`
	Qty              float64  `json:"qty"`
	ConvertedQty     float64  `json:"converted_qty"`
	OpenQty          float64  `json:"open_qty"` // qty − converted (0 once the order is closed or cancelled)
	UnitPricePaise   int64    `json:"unit_price_paise"`
	DiscountPaise    int64    `json:"discount_paise"`
	DiscountBP       int      `json:"discount_bp,omitempty"`
	TaxRateBP        int      `json:"tax_rate_bp"`
	CessRateBP       int      `json:"cess_rate_bp"`
	CessPerUnitPaise int64    `json:"cess_per_unit_paise,omitempty"`
	TaxCategory      string   `json:"tax_category"`
	PriceIncludesTax bool     `json:"price_includes_tax"`
	TaxablePaise     int64    `json:"taxable_paise"`
	CGSTPaise        int64    `json:"cgst_paise"`
	SGSTPaise        int64    `json:"sgst_paise"`
	IGSTPaise        int64    `json:"igst_paise"`
	CessPaise        int64    `json:"cess_paise"`
	TotalPaise       int64    `json:"total_paise"`
	SourceLineID     *string  `json:"source_line_id,omitempty"`
}

// DocLink is one conversion of an order line into a later document.
type DocLink struct {
	FromOrderID string     `json:"from_order_id"`
	FromLineID  string     `json:"from_line_id"`
	FromNumber  string     `json:"from_number,omitempty"`
	ToKind      string     `json:"to_kind"` // an order kind, or sale | purchase
	ToOrderID   *string    `json:"to_order_id,omitempty"`
	ToInvoiceID *string    `json:"to_invoice_id,omitempty"`
	ToNumber    string     `json:"to_number,omitempty"`
	ToLineNo    int        `json:"to_line_no"`
	Qty         float64    `json:"qty"`
	Serials     []string   `json:"serials,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ReversedAt  *time.Time `json:"reversed_at,omitempty"` // the later document was cancelled
}

// orderLink is what a document being created takes from an order.
type orderLink struct {
	OrderID string
	Lines   []linkLine
}

type linkLine struct {
	LineID   string
	Qty      float64
	Serials  []string
	ToLineNo int
}

// ConvertLine picks (part of) an order line to carry forward.
type ConvertLine struct {
	LineNo  int      `json:"line_no"`
	Qty     float64  `json:"qty"`                // in the line's unit; default: all that is open
	Serials []string `json:"serials,omitempty"`  // default: the line's unconsumed serials (as many as needed)
	BatchNo string   `json:"batch_no,omitempty"` // batch actually received/dispatched (GRN, challan); default: the line's
}

// ConvertInput is the body of POST /v1/orders/{id}/convert.
type ConvertInput struct {
	To    string        `json:"to"`              // next kind, or "invoice" (sale / purchase bill by side)
	Lines []ConvertLine `json:"lines,omitempty"` // omitted = every open line in full

	DocDate       string `json:"doc_date,omitempty"` // date of the new document (invoice_date for an invoice)
	ValidUntil    string `json:"valid_until,omitempty"`
	ExpectedDate  string `json:"expected_date,omitempty"`
	SupplierRef   string `json:"supplier_ref,omitempty"`
	ChallanReason string `json:"challan_reason,omitempty"`
	Notes         string `json:"notes,omitempty"`
	Terms         string `json:"terms,omitempty"`
	ClientUUID    string `json:"client_uuid,omitempty"`
	RoundOff      *bool  `json:"round_off,omitempty"`

	// Invoice / purchase bill only.
	DueDate             string   `json:"due_date,omitempty"`
	SupplierInvoiceNo   string   `json:"supplier_invoice_no,omitempty"`
	SupplierInvoiceDate string   `json:"supplier_invoice_date,omitempty"`
	Tenders             []Tender `json:"tenders,omitempty"`
	TCSBP               int      `json:"tcs_bp,omitempty"`
	OverrideCreditLimit bool     `json:"override_credit_limit,omitempty"`
	ITCEligibility      string   `json:"itc_eligibility,omitempty"` // purchase bill: applied to every line
}

// ConvertResult is either a new order or a new invoice.
type ConvertResult struct {
	Kind    string   `json:"kind"`
	Order   *Order   `json:"order,omitempty"`
	Invoice *Invoice `json:"invoice,omitempty"`
}

// CreateOrder validates, prices, numbers and stores a pre-invoice document.
// created=false means client_uuid matched an existing order.
func (s *Service) CreateOrder(ctx context.Context, p *auth.Principal, in OrderInput) (*Order, bool, error) {
	if err := in.Validate(); err != nil {
		return nil, false, err
	}
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID == "" {
		return nil, false, httpx.Validation("store_id is required")
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return nil, false, httpx.Forbidden("no access to store")
	}
	if in.ClientUUID != "" {
		var id string
		err := s.DB.QueryRow(ctx, `SELECT id FROM orders WHERE tenant_id=$1 AND client_uuid=$2`, p.TenantID, in.ClientUUID).Scan(&id)
		if err == nil {
			o, err := s.GetOrder(ctx, p.TenantID, id)
			return o, false, err
		}
		if !db.IsNotFound(err) {
			return nil, false, err
		}
	}
	store, err := s.Dir.Store(ctx, p.TenantID, storeID)
	if err != nil {
		return nil, false, err
	}
	party, err := s.resolveParty(ctx, CreateInput{PartyID: in.PartyID, Party: in.Party})
	if err != nil {
		return nil, false, err
	}
	pos := in.PlaceOfSupply
	if pos == "" {
		pos = party.StateCode
	}
	if pos == "" {
		pos = store.StateCode
	}
	now := s.Now()
	date, err := s.documentDate(CreateInput{InvoiceDate: in.DocDate}, now)
	if err != nil {
		return nil, false, err
	}
	if err := s.validateLines(ctx, in.Kind, storeID, date, in.Lines); err != nil {
		return nil, false, err
	}
	bs, err := s.BillingSettings(ctx, p.TenantID)
	if err != nil {
		return nil, false, err
	}
	glines := make([]gst.Line, len(in.Lines))
	for i, l := range in.Lines {
		glines[i] = l.Line
	}
	sum, err := gst.ComputeWith(glines, gst.Options{StoreState: store.StateCode, PlaceOfSupply: pos, InvoiceDiscountBP: in.InvoiceDiscountBP})
	if err != nil {
		return nil, false, httpx.Validation("%s", err.Error())
	}
	applyRoundOff(&sum, bs, in.RoundOff != nil && !*in.RoundOff)

	o := &Order{
		ID: uuid.NewString(), StoreID: storeID, Kind: in.Kind, DocDate: date.Format(time.DateOnly), ValidUntil: nilIfEmpty(in.ValidUntil),
		ExpectedDate: nilIfEmpty(in.ExpectedDate), PartyID: nilIfEmpty(party.ID), PartyName: party.Name, PartyPhone: nilIfEmpty(party.Phone),
		PartyGSTIN: nilIfEmpty(party.GSTIN), PartyState: nilIfEmpty(party.StateCode), StoreState: store.StateCode,
		StoreGSTIN: nilIfEmpty(store.GSTIN), RegistrationID: nilIfEmpty(store.RegistrationID), PlaceOfSupply: pos, InterState: sum.InterState,
		SupplierRef: nilIfEmpty(strings.TrimSpace(in.SupplierRef)), ChallanReason: nilIfEmpty(in.ChallanReason), Status: OrderOpen,
		TaxablePaise: sum.TaxablePaise, DiscountPaise: sum.DiscountPaise, InvoiceDiscountBP: in.InvoiceDiscountBP,
		CGSTPaise: sum.CGSTPaise, SGSTPaise: sum.SGSTPaise, IGSTPaise: sum.IGSTPaise, CessPaise: sum.CessPaise,
		RoundOffPaise: sum.RoundOffPaise, GrandTotalPaise: sum.GrandPaise, AmountInWords: gst.AmountInWords(sum.GrandPaise),
		Notes: nilIfEmpty(in.Notes), Terms: nilIfEmpty(in.Terms), ClientUUID: nilIfEmpty(in.ClientUUID), CreatedBy: p.UserID(), CreatedAt: now,
	}
	if in.Kind == KindDeliveryChallan && o.ChallanReason == nil {
		r := "supply"
		o.ChallanReason = &r
	}
	if in.link != nil {
		o.SourceOrderID = &in.link.OrderID
	}
	for i, l := range in.Lines {
		lt := sum.Lines[i]
		unit := strings.ToUpper(strings.TrimSpace(l.Unit))
		if unit == "" {
			unit = "PCS"
		}
		o.Lines = append(o.Lines, OrderLine{
			ID: uuid.NewString(), LineNo: i + 1, ItemID: nilIfEmpty(l.ItemID), Name: strings.TrimSpace(l.Name), HSN: nilIfEmpty(l.HSN),
			Unit: unit, UnitFactor: l.factor, BaseQty: l.Qty * l.factor, BatchNo: nilIfEmpty(l.BatchNo), Serials: l.Serials, Qty: l.Qty,
			OpenQty: l.Qty, UnitPricePaise: l.UnitPricePaise, DiscountPaise: l.DiscountPaise, DiscountBP: l.DiscountBP, TaxRateBP: lt.RateBP,
			CessRateBP: l.CessRateBP, CessPerUnitPaise: l.CessPerUnitPaise, TaxCategory: lt.TaxCategory, PriceIncludesTax: l.PriceIncludesTax,
			TaxablePaise: lt.TaxablePaise, CGSTPaise: lt.CGSTPaise, SGSTPaise: lt.SGSTPaise, IGSTPaise: lt.IGSTPaise, CessPaise: lt.CessPaise,
			TotalPaise: lt.TotalPaise, SourceLineID: nilIfEmpty(l.sourceLine),
		})
	}

	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		regKey, err := registrationKey(ctx, tx, p.TenantID, store)
		if err != nil {
			return err
		}
		fy := FinancialYear(date)
		ns := numberSeries{Key: regKey, RegKey: regKey, Prefix: orderPrefix[in.Kind]}
		seq, prefix, err := ns.take(ctx, tx, p.TenantID, in.Kind, fy, 1)
		if err != nil {
			return err
		}
		o.FY, o.Number = fy, FormatNumber(prefix, fy, seq)
		if err := insertOrder(ctx, tx, p.TenantID, seq, regKey, o); err != nil {
			return err
		}
		if in.link != nil {
			return consumeOrderLines(ctx, tx, p.TenantID, in.link, in.Kind, o.ID, "")
		}
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err) && in.ClientUUID != "" {
			var id string
			if e2 := s.DB.QueryRow(ctx, `SELECT id FROM orders WHERE tenant_id=$1 AND client_uuid=$2`, p.TenantID, in.ClientUUID).Scan(&id); e2 == nil {
				got, err := s.GetOrder(ctx, p.TenantID, id)
				return got, false, err
			}
		}
		return nil, false, err
	}
	got, err := s.GetOrder(ctx, p.TenantID, o.ID)
	return got, true, err
}

// ConvertOrder carries (part of) an order forward into the next document:
// another order kind, or the tax invoice / purchase bill. The new document and
// the open-quantity bookkeeping are written in one transaction, under a lock on
// the source order, so two conversions can never take the same quantity.
func (s *Service) ConvertOrder(ctx context.Context, p *auth.Principal, id string, in ConvertInput) (*ConvertResult, bool, error) {
	src, err := s.GetOrder(ctx, p.TenantID, id)
	if err != nil {
		return nil, false, err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(src.StoreID) {
		return nil, false, httpx.NotFound("order not found")
	}
	to := in.To
	if to == "invoice" || to == "bill" || to == "" {
		to = DocSale
		if IsPurchaseKind(src.Kind) {
			to = DocPurchase
		}
	}
	if !canConvert(src.Kind, to) {
		return nil, false, httpx.Validation("a %s cannot be converted into %s (allowed: %s)", src.Kind, to, strings.Join(nextKinds[src.Kind], ", "))
	}
	// A retried conversion (same client_uuid) returns what the first one made.
	if in.ClientUUID != "" {
		if res, ok, err := s.convertedBefore(ctx, p.TenantID, src.ID, to, in.ClientUUID); err != nil || ok {
			return res, false, err
		}
	}
	if src.Status != OrderOpen && src.Status != OrderPartial {
		return nil, false, httpx.Conflict("%s %s is %s; nothing is open to convert", src.Kind, src.Number, src.Status)
	}
	consumed, err := consumedSerials(ctx, s.DB, src.ID)
	if err != nil {
		return nil, false, err
	}
	byNo := map[int]*OrderLine{}
	for i := range src.Lines {
		byNo[src.Lines[i].LineNo] = &src.Lines[i]
	}
	picks := in.Lines
	if len(picks) == 0 {
		for _, l := range src.Lines {
			if l.OpenQty > 1e-9 {
				picks = append(picks, ConvertLine{LineNo: l.LineNo})
			}
		}
		if len(picks) == 0 {
			return nil, false, httpx.Conflict("nothing is open on %s", src.Number)
		}
	}
	link := &orderLink{OrderID: src.ID}
	var lines []LineInput
	used := map[int]bool{}
	for _, pk := range picks {
		ol, ok := byNo[pk.LineNo]
		if !ok {
			return nil, false, httpx.Validation("line %d is not on %s", pk.LineNo, src.Number)
		}
		if used[pk.LineNo] {
			return nil, false, httpx.Validation("line %d is listed twice", pk.LineNo)
		}
		used[pk.LineNo] = true
		qty := pk.Qty
		if qty == 0 {
			qty = ol.OpenQty
		}
		if qty <= 0 || qty > ol.OpenQty+1e-9 {
			return nil, false, httpx.Validation("line %d: only %.3f %s of %s is open", pk.LineNo, ol.OpenQty, ol.Unit, ol.Name)
		}
		serials := pk.Serials
		if len(serials) == 0 && len(ol.Serials) > 0 {
			need := int(math.Round(qty * unitFactorOr1(ol.UnitFactor)))
			for _, sn := range ol.Serials {
				if len(serials) == need {
					break
				}
				if !consumed[ol.ID][sn] {
					serials = append(serials, sn)
				}
			}
		}
		disc := ol.DiscountPaise
		if qty < ol.Qty {
			disc = int64(math.Round(float64(ol.DiscountPaise) * qty / ol.Qty))
		}
		li := LineInput{ItemID: deref(ol.ItemID), Name: ol.Name, Unit: ol.Unit, BatchNo: deref(ol.BatchNo), Serials: serials, sourceLine: ol.ID}
		li.HSN, li.Qty, li.UnitPricePaise, li.DiscountPaise, li.DiscountBP = deref(ol.HSN), qty, ol.UnitPricePaise, disc, ol.DiscountBP
		li.TaxRateBP, li.CessRateBP, li.CessPerUnitPaise, li.TaxCategory, li.PriceIncludesTax = ol.TaxRateBP, ol.CessRateBP, ol.CessPerUnitPaise, ol.TaxCategory, ol.PriceIncludesTax
		if pk.BatchNo != "" {
			li.BatchNo = strings.TrimSpace(pk.BatchNo)
		}
		if to == DocPurchase {
			li.ITCEligibility = in.ITCEligibility
		}
		lines = append(lines, li)
		link.Lines = append(link.Lines, linkLine{LineID: ol.ID, Qty: qty, Serials: append([]string(nil), serials...), ToLineNo: len(lines)})
	}
	var party *PartyInput
	if src.PartyID == nil {
		party = &PartyInput{Name: src.PartyName, Phone: deref(src.PartyPhone), GSTIN: deref(src.PartyGSTIN), StateCode: deref(src.PartyState)}
	}
	notes := in.Notes
	if notes == "" {
		notes = deref(src.Notes)
	}

	if to == DocSale || to == DocPurchase {
		ci := CreateInput{
			DocType: to, StoreID: src.StoreID, InvoiceDate: in.DocDate, DueDate: in.DueDate, PartyID: deref(src.PartyID), Party: party,
			PlaceOfSupply: src.PlaceOfSupply, SupplierInvoiceNo: in.SupplierInvoiceNo, SupplierInvoiceDate: in.SupplierInvoiceDate,
			Lines: lines, InvoiceDiscountBP: src.InvoiceDiscountBP, TCSBP: in.TCSBP, Tenders: in.Tenders, Notes: notes,
			RoundOff: in.RoundOff, OverrideCreditLimit: in.OverrideCreditLimit, ClientUUID: in.ClientUUID, link: link,
		}
		if err := ci.Validate(); err != nil {
			return nil, false, err
		}
		inv, created, err := s.Create(ctx, p, ci)
		if err != nil {
			return nil, false, err
		}
		return &ConvertResult{Kind: to, Invoice: inv}, created, nil
	}
	terms := in.Terms
	if terms == "" {
		terms = deref(src.Terms)
	}
	oi := OrderInput{
		Kind: to, StoreID: src.StoreID, DocDate: in.DocDate, ValidUntil: in.ValidUntil, ExpectedDate: in.ExpectedDate,
		PartyID: deref(src.PartyID), Party: party, PlaceOfSupply: src.PlaceOfSupply, SupplierRef: in.SupplierRef,
		ChallanReason: in.ChallanReason, Lines: lines, InvoiceDiscountBP: src.InvoiceDiscountBP, Notes: notes, Terms: terms,
		RoundOff: in.RoundOff, ClientUUID: in.ClientUUID, link: link,
	}
	o, created, err := s.CreateOrder(ctx, p, oi)
	if err != nil {
		return nil, false, err
	}
	return &ConvertResult{Kind: to, Order: o}, created, nil
}

// convertedBefore finds the document an earlier conversion with the same
// client_uuid made. A client_uuid already used for something else is a conflict.
func (s *Service) convertedBefore(ctx context.Context, tenantID, srcID, to, clientUUID string) (*ConvertResult, bool, error) {
	var id string
	var source *string
	if to == DocSale || to == DocPurchase {
		err := s.DB.QueryRow(ctx, `SELECT id, source_order_id::text FROM invoices WHERE tenant_id=$1 AND client_uuid=$2`, tenantID, clientUUID).Scan(&id, &source)
		if db.IsNotFound(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if deref(source) != srcID {
			return nil, false, httpx.Conflict("client_uuid %s was already used for another document", clientUUID)
		}
		inv, err := s.Get(ctx, tenantID, id)
		if err != nil {
			return nil, false, err
		}
		return &ConvertResult{Kind: inv.DocType, Invoice: inv}, true, nil
	}
	err := s.DB.QueryRow(ctx, `SELECT id, source_order_id::text FROM orders WHERE tenant_id=$1 AND client_uuid=$2`, tenantID, clientUUID).Scan(&id, &source)
	if db.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if deref(source) != srcID {
		return nil, false, httpx.Conflict("client_uuid %s was already used for another document", clientUUID)
	}
	o, err := s.GetOrder(ctx, tenantID, id)
	if err != nil {
		return nil, false, err
	}
	return &ConvertResult{Kind: o.Kind, Order: o}, true, nil
}

// consumeOrderLines takes quantity from an order inside the transaction that
// writes the new document: it locks the order, re-checks open quantity and
// serials, moves converted_qty and records the links.
func consumeOrderLines(ctx context.Context, tx pgx.Tx, tenantID string, link *orderLink, toKind, toOrderID, toInvoiceID string) error {
	var status, kind, number string
	err := tx.QueryRow(ctx, `SELECT status, kind, number FROM orders WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, link.OrderID).Scan(&status, &kind, &number)
	if db.IsNotFound(err) {
		return httpx.NotFound("order not found")
	}
	if err != nil {
		return err
	}
	if status != OrderOpen && status != OrderPartial {
		return httpx.Conflict("%s is %s; nothing is open to convert", number, status)
	}
	if !canConvert(kind, toKind) {
		return httpx.Validation("a %s cannot be converted into %s", kind, toKind)
	}
	consumed, err := consumedSerials(ctx, tx, link.OrderID)
	if err != nil {
		return err
	}
	for _, ll := range link.Lines {
		var qty, conv float64
		var serials []string
		var lineNo int
		if err := tx.QueryRow(ctx, `SELECT line_no, qty::float8, converted_qty::float8, serials FROM order_lines WHERE id=$1 AND order_id=$2`,
			ll.LineID, link.OrderID).Scan(&lineNo, &qty, &conv, &serials); err != nil {
			return err
		}
		if ll.Qty > qty-conv+1e-9 {
			return httpx.Conflict("line %d of %s: only %.3f is still open", lineNo, number, qty-conv)
		}
		if len(serials) > 0 {
			onLine := map[string]bool{}
			for _, sn := range serials {
				onLine[sn] = true
			}
			for _, sn := range ll.Serials {
				if !onLine[sn] {
					return httpx.Validation("line %d of %s: serial %s is not on it", lineNo, number, sn)
				}
				if consumed[ll.LineID][sn] {
					return httpx.Conflict("line %d of %s: serial %s was already carried forward", lineNo, number, sn)
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE order_lines SET converted_qty = converted_qty + $2 WHERE id=$1`, ll.LineID, ll.Qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_links (tenant_id, from_order_id, from_line_id, to_kind, to_order_id, to_invoice_id, to_line_no, qty, serials)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenantID, link.OrderID, ll.LineID, toKind, nilIfEmpty(toOrderID), nilIfEmpty(toInvoiceID),
			ll.ToLineNo, ll.Qty, serialsOrEmpty(ll.Serials)); err != nil {
			return err
		}
	}
	return refreshOrderStatus(ctx, tx, link.OrderID)
}

// consumedSerials lists, per order line, the serials already carried forward.
func consumedSerials(ctx context.Context, q db.Querier, orderID string) (map[string]map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT from_line_id::text, serials FROM document_links WHERE from_order_id=$1 AND reversed_at IS NULL`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var lineID string
		var serials []string
		if err := rows.Scan(&lineID, &serials); err != nil {
			return nil, err
		}
		if out[lineID] == nil {
			out[lineID] = map[string]bool{}
		}
		for _, sn := range serials {
			out[lineID][sn] = true
		}
	}
	return out, rows.Err()
}

// refreshOrderStatus derives open / partial / completed from the lines; closed
// and cancelled orders keep their status.
func refreshOrderStatus(ctx context.Context, tx pgx.Tx, orderID string) error {
	_, err := tx.Exec(ctx, `UPDATE orders o SET status = CASE
			WHEN o.status IN ('closed','cancelled') THEN o.status
			WHEN NOT EXISTS (SELECT 1 FROM order_lines l WHERE l.order_id=o.id AND l.converted_qty < l.qty) THEN 'completed'
			WHEN EXISTS (SELECT 1 FROM order_lines l WHERE l.order_id=o.id AND l.converted_qty > 0) THEN 'partial'
			ELSE 'open' END
		WHERE o.id=$1`, orderID)
	return err
}

// reverseLinks gives back the quantity a cancelled document took from its
// order(s). col is "to_invoice_id" or "to_order_id".
func reverseLinks(ctx context.Context, tx pgx.Tx, tenantID, col, id string) error {
	if col != "to_invoice_id" && col != "to_order_id" {
		return fmt.Errorf("reverseLinks: bad column %q", col)
	}
	rows, err := tx.Query(ctx, `UPDATE document_links SET reversed_at = now()
		WHERE tenant_id=$1 AND `+col+`=$2 AND reversed_at IS NULL RETURNING from_order_id::text, from_line_id::text, qty::float8`, tenantID, id)
	if err != nil {
		return err
	}
	type back struct {
		order, line string
		qty         float64
	}
	var backs []back
	for rows.Next() {
		var b back
		if err := rows.Scan(&b.order, &b.line, &b.qty); err != nil {
			rows.Close()
			return err
		}
		backs = append(backs, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	orders := map[string]bool{}
	for _, b := range backs {
		if !orders[b.order] {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM orders WHERE id=$1 FOR UPDATE`, b.order); err != nil {
				return err
			}
		}
		orders[b.order] = true
		if _, err := tx.Exec(ctx, `UPDATE order_lines SET converted_qty = GREATEST(0, converted_qty - $2) WHERE id=$1`, b.line, b.qty); err != nil {
			return err
		}
	}
	for o := range orders {
		if err := refreshOrderStatus(ctx, tx, o); err != nil {
			return err
		}
	}
	return nil
}

// CancelOrder cancels an order nothing live was made from, and gives back what
// it took from its own source.
func (s *Service) CancelOrder(ctx context.Context, p *auth.Principal, id, reason string) (*Order, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, httpx.Validation("reason is required")
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var storeID, status, number string
		err := tx.QueryRow(ctx, `SELECT store_id, status, number FROM orders WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&storeID, &status, &number)
		if db.IsNotFound(err) {
			return httpx.NotFound("order not found")
		}
		if err != nil {
			return err
		}
		if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
			return httpx.NotFound("order not found")
		}
		if status == OrderCancelled {
			return httpx.Conflict("%s is already cancelled", number)
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT COALESCE(to_order_id, to_invoice_id)) FROM document_links
			WHERE from_order_id=$1 AND reversed_at IS NULL`, id).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return httpx.Conflict("cancel the %d document(s) made from %s first, or close it instead", live, number)
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled', cancelled_at=now(), cancel_reason=$2 WHERE id=$1`, id, reason); err != nil {
			return err
		}
		return reverseLinks(ctx, tx, p.TenantID, "to_order_id", id)
	})
	if err != nil {
		return nil, err
	}
	return s.GetOrder(ctx, p.TenantID, id)
}

// CloseOrder short-closes an open or partly converted order: the rest is not
// expected any more and cannot be converted.
func (s *Service) CloseOrder(ctx context.Context, p *auth.Principal, id, reason string) (*Order, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var storeID, status, number string
		err := tx.QueryRow(ctx, `SELECT store_id, status, number FROM orders WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&storeID, &status, &number)
		if db.IsNotFound(err) {
			return httpx.NotFound("order not found")
		}
		if err != nil {
			return err
		}
		if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
			return httpx.NotFound("order not found")
		}
		if status != OrderOpen && status != OrderPartial {
			return httpx.Conflict("%s is %s and cannot be closed", number, status)
		}
		_, err = tx.Exec(ctx, `UPDATE orders SET status='closed', closed_at=now(), close_reason=$2 WHERE id=$1`, id, nilIfEmpty(reason))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetOrder(ctx, p.TenantID, id)
}

const orderCols = `id, store_id, kind, number, fy, doc_date, valid_until, expected_date, party_id, party_name, party_phone, party_gstin,
	party_state, store_state, store_gstin, registration_id, place_of_supply, inter_state, supplier_ref, challan_reason, status,
	taxable_paise, discount_paise, invoice_discount_bp, cgst_paise, sgst_paise, igst_paise, cess_paise, round_off_paise,
	grand_total_paise, source_order_id, notes, terms, client_uuid, created_by, created_at, closed_at, close_reason, cancelled_at, cancel_reason`

func scanOrder(row pgx.Row) (*Order, error) {
	var o Order
	var date time.Time
	var valid, expected *time.Time
	err := row.Scan(&o.ID, &o.StoreID, &o.Kind, &o.Number, &o.FY, &date, &valid, &expected, &o.PartyID, &o.PartyName, &o.PartyPhone,
		&o.PartyGSTIN, &o.PartyState, &o.StoreState, &o.StoreGSTIN, &o.RegistrationID, &o.PlaceOfSupply, &o.InterState, &o.SupplierRef,
		&o.ChallanReason, &o.Status, &o.TaxablePaise, &o.DiscountPaise, &o.InvoiceDiscountBP, &o.CGSTPaise, &o.SGSTPaise, &o.IGSTPaise,
		&o.CessPaise, &o.RoundOffPaise, &o.GrandTotalPaise, &o.SourceOrderID, &o.Notes, &o.Terms, &o.ClientUUID, &o.CreatedBy, &o.CreatedAt,
		&o.ClosedAt, &o.CloseReason, &o.CancelledAt, &o.CancelReason)
	if err != nil {
		return nil, err
	}
	o.DocDate = date.Format(time.DateOnly)
	if valid != nil {
		v := valid.Format(time.DateOnly)
		o.ValidUntil = &v
	}
	if expected != nil {
		v := expected.Format(time.DateOnly)
		o.ExpectedDate = &v
	}
	o.AmountInWords = gst.AmountInWords(o.GrandTotalPaise)
	return &o, nil
}

func insertOrder(ctx context.Context, tx pgx.Tx, tenantID string, seq int64, regKey string, o *Order) error {
	err := tx.QueryRow(ctx, `INSERT INTO orders (id, tenant_id, store_id, kind, number, seq_no, fy, registration_key, registration_id, doc_date,
		valid_until, expected_date, party_id, party_name, party_phone, party_gstin, party_state, store_state, store_gstin, place_of_supply,
		inter_state, supplier_ref, challan_reason, status, taxable_paise, discount_paise, invoice_discount_bp, cgst_paise, sgst_paise,
		igst_paise, cess_paise, round_off_paise, grand_total_paise, source_order_id, notes, terms, client_uuid, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,
		        $31,$32,$33,$34,$35,$36,$37,$38) RETURNING created_at`,
		o.ID, tenantID, o.StoreID, o.Kind, o.Number, seq, o.FY, regKey, o.RegistrationID, o.DocDate, dateOrNil(deref(o.ValidUntil)),
		dateOrNil(deref(o.ExpectedDate)), o.PartyID, o.PartyName, o.PartyPhone, o.PartyGSTIN, o.PartyState, o.StoreState, o.StoreGSTIN,
		o.PlaceOfSupply, o.InterState, o.SupplierRef, o.ChallanReason, o.Status, o.TaxablePaise, o.DiscountPaise, o.InvoiceDiscountBP,
		o.CGSTPaise, o.SGSTPaise, o.IGSTPaise, o.CessPaise, o.RoundOffPaise, o.GrandTotalPaise, o.SourceOrderID, o.Notes, o.Terms,
		o.ClientUUID, o.CreatedBy).Scan(&o.CreatedAt)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, l := range o.Lines {
		batch.Queue(`INSERT INTO order_lines (id, order_id, line_no, item_id, name, hsn, unit, unit_factor, batch_no, serials, qty,
			unit_price_paise, discount_paise, discount_bp, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category, price_includes_tax,
			taxable_paise, cgst_paise, sgst_paise, igst_paise, cess_paise, total_paise, source_line_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
			l.ID, o.ID, l.LineNo, l.ItemID, l.Name, l.HSN, l.Unit, unitFactorOr1(l.UnitFactor), l.BatchNo, serialsOrEmpty(l.Serials), l.Qty,
			l.UnitPricePaise, l.DiscountPaise, l.DiscountBP, l.TaxRateBP, l.CessRateBP, l.CessPerUnitPaise, l.TaxCategory, l.PriceIncludesTax,
			l.TaxablePaise, l.CGSTPaise, l.SGSTPaise, l.IGSTPaise, l.CessPaise, l.TotalPaise, l.SourceLineID)
	}
	return tx.SendBatch(ctx, batch).Close()
}

// GetOrder loads an order with its lines (open quantities) and links.
func (s *Service) GetOrder(ctx context.Context, tenantID, id string) (*Order, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, httpx.NotFound("order not found")
	}
	o, err := scanOrder(s.DB.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("order not found")
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, line_no, item_id, name, hsn, unit, unit_factor::float8, batch_no, serials, qty::float8,
		converted_qty::float8, unit_price_paise, discount_paise, discount_bp, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category,
		price_includes_tax, taxable_paise, cgst_paise, sgst_paise, igst_paise, cess_paise, total_paise, source_line_id
		FROM order_lines WHERE order_id=$1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ItemID, &l.Name, &l.HSN, &l.Unit, &l.UnitFactor, &l.BatchNo, &l.Serials, &l.Qty,
			&l.ConvertedQty, &l.UnitPricePaise, &l.DiscountPaise, &l.DiscountBP, &l.TaxRateBP, &l.CessRateBP, &l.CessPerUnitPaise,
			&l.TaxCategory, &l.PriceIncludesTax, &l.TaxablePaise, &l.CGSTPaise, &l.SGSTPaise, &l.IGSTPaise, &l.CessPaise, &l.TotalPaise,
			&l.SourceLineID); err != nil {
			return nil, err
		}
		l.BaseQty = l.Qty * unitFactorOr1(l.UnitFactor)
		if o.Status == OrderOpen || o.Status == OrderPartial {
			l.OpenQty = math.Max(0, math.Round((l.Qty-l.ConvertedQty)*1000)/1000)
		}
		o.Lines = append(o.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	links, err := s.links(ctx, `l.from_order_id=$1`, id)
	if err != nil {
		return nil, err
	}
	o.ConvertedTo = links
	if o.MadeFrom, err = s.links(ctx, `l.to_order_id=$1`, id); err != nil {
		return nil, err
	}
	return o, nil
}

// InvoiceLinks lists the order lines an invoice was made from.
func (s *Service) InvoiceLinks(ctx context.Context, invoiceID string) ([]DocLink, error) {
	return s.links(ctx, `l.to_invoice_id=$1`, invoiceID)
}

func (s *Service) links(ctx context.Context, cond, id string) ([]DocLink, error) {
	rows, err := s.DB.Query(ctx, `SELECT l.from_order_id::text, l.from_line_id::text, f.number, l.to_kind, l.to_order_id::text,
		l.to_invoice_id::text, COALESCE(o.number, i.number, ''), l.to_line_no, l.qty::float8, l.serials, l.created_at, l.reversed_at
		FROM document_links l JOIN orders f ON f.id = l.from_order_id
		LEFT JOIN orders o ON o.id = l.to_order_id LEFT JOIN invoices i ON i.id = l.to_invoice_id
		WHERE `+cond+` ORDER BY l.created_at, l.to_line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocLink
	for rows.Next() {
		var d DocLink
		if err := rows.Scan(&d.FromOrderID, &d.FromLineID, &d.FromNumber, &d.ToKind, &d.ToOrderID, &d.ToInvoiceID, &d.ToNumber, &d.ToLineNo,
			&d.Qty, &d.Serials, &d.CreatedAt, &d.ReversedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// OrderFilter narrows GET /v1/orders.
type OrderFilter struct {
	Kind, StoreID, PartyID, Status string
	OpenOnly                       bool // open or partial: the "pending" list
	From, To, Search               string
	httpx.Page
}

// ListOrders returns orders (without lines) matching f.
func (s *Service) ListOrders(ctx context.Context, tenantID string, f OrderFilter) ([]Order, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.StoreID != "" {
		add("store_id = $%d", f.StoreID)
	}
	if f.PartyID != "" {
		add("party_id = $%d", f.PartyID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.OpenOnly {
		where = append(where, "status IN ('open','partial')")
	}
	if f.From != "" {
		add("doc_date >= $%d", f.From)
	}
	if f.To != "" {
		add("doc_date <= $%d", f.To)
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(number ILIKE $%d OR party_name ILIKE $%d OR party_phone ILIKE $%d)", n, n, n))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM orders WHERE %s ORDER BY doc_date DESC, seq_no DESC LIMIT $%d OFFSET $%d`,
		orderCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *o)
	}
	return out, total, rows.Err()
}
