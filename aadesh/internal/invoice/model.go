// Package invoice is aadesh's core: creating, numbering, cancelling and
// syncing GST invoices, and reacting to payments.
package invoice

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Document types.
const (
	DocSale           = "sale"
	DocPurchase       = "purchase"
	DocSaleReturn     = "sale_return"     // credit note
	DocPurchaseReturn = "purchase_return" // debit note
)

// NilUUID keys the tenant-wide (shared) number series.
const NilUUID = "00000000-0000-0000-0000-000000000000"

// ITC eligibility of a purchase line (GSTR-3B Table 4).
const (
	ITCEligible        = "eligible"
	ITCIneligible175   = "ineligible_17_5"  // blocked credit, Sec 17(5)
	ITCIneligibleOther = "ineligible_other" // personal use, exempt supplies, ...
)

var validITC = map[string]bool{ITCEligible: true, ITCIneligible175: true, ITCIneligibleOther: true}

// IsPurchaseDoc reports whether the doc type is a purchase bill or debit note.
func IsPurchaseDoc(docType string) bool {
	return docType == DocPurchase || docType == DocPurchaseReturn
}

// MaxNumberLen is the GST limit on an invoice serial number (Rule 46).
const MaxNumberLen = 16

var defaultPrefix = map[string]string{
	DocSale: "INV", DocPurchase: "PUR", DocSaleReturn: "CN", DocPurchaseReturn: "DN",
}

var prefixPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,3}$`)

// IsNote reports whether the doc type is a credit/debit note.
func IsNote(docType string) bool { return docType == DocSaleReturn || docType == DocPurchaseReturn }

// PartyInput is either a saved party (PartyID) or an inline walk-in customer.
type PartyInput struct {
	Name      string `json:"name"`
	Phone     string `json:"phone,omitempty"`
	GSTIN     string `json:"gstin,omitempty"`
	StateCode string `json:"state_code,omitempty"`
}

// LineInput is one line as sent by the POS.
type LineInput struct {
	ItemID  string `json:"item_id,omitempty"`
	Name    string `json:"name"`
	Unit    string `json:"unit,omitempty"`
	BatchNo string `json:"batch_no,omitempty"`
	// ITCEligibility applies to purchase bills and debit notes only: eligible
	// (default) | ineligible_17_5 | ineligible_other. A debit note inherits it
	// from the purchase line when omitted.
	ITCEligibility string `json:"itc_eligibility,omitempty"`
	gst.Line              // hsn, qty, unit_price_paise, discount_paise, discount_bp, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, price_includes_tax, tax_category
}

// Tender is how part of the bill was paid at the counter.
type Tender struct {
	Mode             string `json:"mode"`
	AmountPaise      int64  `json:"amount_paise"`
	Reference        string `json:"reference,omitempty"`
	PaymentRequestID string `json:"payment_request_id,omitempty"` // lenden request behind a confirmed counter UPI/card payment
}

// CreateInput is the body of POST /v1/invoices and each item of POST /v1/sync/invoices.
type CreateInput struct {
	DocType              string      `json:"doc_type"`
	StoreID              string      `json:"store_id,omitempty"`     // defaults to the active store
	InvoiceDate          string      `json:"invoice_date,omitempty"` // YYYY-MM-DD; defaults to client_created_at, then today IST
	DueDate              string      `json:"due_date,omitempty"`     // YYYY-MM-DD; defaults to invoice date + party credit days
	PartyID              string      `json:"party_id,omitempty"`
	Party                *PartyInput `json:"party,omitempty"`
	PlaceOfSupply        string      `json:"place_of_supply,omitempty"`
	SupplyCategory       string      `json:"supply_category,omitempty"` // "", sez_with_payment, sez_without_payment, export_with_payment, export_without_payment, deemed_export
	ReverseCharge        bool        `json:"reverse_charge,omitempty"`
	SupplierInvoiceNo    string      `json:"supplier_invoice_no,omitempty"`
	SupplierInvoiceDate  string      `json:"supplier_invoice_date,omitempty"` // required with supplier_invoice_no (GSTR-2B matching)
	RefInvoiceID         string      `json:"ref_invoice_id,omitempty"`
	Lines                []LineInput `json:"lines"`
	InvoiceDiscountPaise int64       `json:"invoice_discount_paise,omitempty"`
	InvoiceDiscountBP    int         `json:"invoice_discount_bp,omitempty"`
	TCSBP                int         `json:"tcs_bp,omitempty"` // income-tax TCS 206C(1H), e.g. 10 = 0.1 %
	Tenders              []Tender    `json:"tenders,omitempty"`
	Notes                string      `json:"notes,omitempty"`
	RoundOff             *bool       `json:"round_off,omitempty"`             // default true
	OverrideCreditLimit  bool        `json:"override_credit_limit,omitempty"` // managers may bill past a party's credit limit

	// Offline POS fields.
	ClientUUID      string     `json:"client_uuid,omitempty"`
	DeviceID        string     `json:"device_id,omitempty"`
	Number          int64      `json:"number,omitempty"` // seq from a reserved number block
	ClientCreatedAt *time.Time `json:"client_created_at,omitempty"`
}

var validTender = map[string]bool{"cash": true, "card": true, "upi": true, "bank": true, "credit": true, "other": true}

// Validate checks the request shape (amounts are checked again by the tax engine).
func (in *CreateInput) Validate() error {
	if _, ok := defaultPrefix[in.DocType]; !ok {
		return httpx.Validation("doc_type must be sale, purchase, sale_return or purchase_return")
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		return httpx.Validation("an invoice needs 1 to 500 lines")
	}
	for i, l := range in.Lines {
		if strings.TrimSpace(l.Name) == "" {
			return httpx.Validation("line %d: name is required", i+1)
		}
		if l.ITCEligibility != "" {
			if !validITC[l.ITCEligibility] {
				return httpx.Validation("line %d: itc_eligibility must be eligible, ineligible_17_5 or ineligible_other", i+1)
			}
			if !IsPurchaseDoc(in.DocType) {
				return httpx.Validation("line %d: itc_eligibility applies to purchase bills and debit notes only", i+1)
			}
		}
	}
	if in.DocType == DocPurchase && in.PartyID == "" {
		return httpx.Validation("purchase bills need a supplier party_id")
	}
	if IsNote(in.DocType) && in.RefInvoiceID == "" {
		return httpx.Validation("credit/debit notes need ref_invoice_id")
	}
	if in.Party != nil && in.Party.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.Party.GSTIN); err != nil {
			return httpx.Validation("party gstin: %s", err.Error())
		}
	}
	for _, d := range []struct{ name, v string }{{"invoice_date", in.InvoiceDate}, {"due_date", in.DueDate}, {"supplier_invoice_date", in.SupplierInvoiceDate}} {
		if d.v != "" {
			if _, err := time.Parse(time.DateOnly, d.v); err != nil {
				return httpx.Validation("%s must be YYYY-MM-DD", d.name)
			}
		}
	}
	if in.DocType == DocPurchase && in.SupplierInvoiceNo != "" && in.SupplierInvoiceDate == "" {
		return httpx.Validation("supplier_invoice_date is required with supplier_invoice_no")
	}
	if in.DocType != DocPurchase && (in.SupplierInvoiceNo != "" || in.SupplierInvoiceDate != "") {
		return httpx.Validation("supplier invoice fields apply to purchase bills only")
	}
	switch in.SupplyCategory {
	case gst.SupplyRegular, gst.SupplySEZWithPayment, gst.SupplySEZWithoutPayment, gst.SupplyExportWithPayment, gst.SupplyExportWithoutPayment, gst.SupplyDeemedExport:
	default:
		return httpx.Validation("unknown supply_category")
	}
	if in.InvoiceDiscountPaise < 0 || in.InvoiceDiscountBP < 0 || in.TCSBP < 0 {
		return httpx.Validation("discounts and tcs cannot be negative")
	}
	credit := 0
	for _, t := range in.Tenders {
		if !validTender[t.Mode] || t.AmountPaise <= 0 {
			return httpx.Validation("tender mode must be cash/card/upi/bank/credit/other with a positive amount")
		}
		if t.Mode == "credit" {
			credit++
		}
		if t.PaymentRequestID != "" && t.Mode != "upi" && t.Mode != "card" {
			return httpx.Validation("payment_request_id applies to upi or card tenders")
		}
	}
	if credit > 1 {
		return httpx.Validation("at most one credit tender")
	}
	if in.Number != 0 && (in.DeviceID == "" || in.ClientUUID == "") {
		return httpx.Validation("offline numbers need device_id and client_uuid")
	}
	if len(in.ClientUUID) > 64 {
		return errors.New("client_uuid too long")
	}
	return nil
}

// Invoice is the stored document returned by the API.
type Invoice struct {
	ID                   string     `json:"id"`
	StoreID              string     `json:"store_id"`
	DocType              string     `json:"doc_type"`
	Number               string     `json:"number"`
	FY                   string     `json:"fy"`
	InvoiceDate          string     `json:"invoice_date"`
	DueDate              *string    `json:"due_date,omitempty"`
	SupplierInvoiceNo    *string    `json:"supplier_invoice_no,omitempty"`
	SupplierInvoiceDate  *string    `json:"supplier_invoice_date,omitempty"`
	RefInvoiceID         *string    `json:"ref_invoice_id,omitempty"`
	PartyID              *string    `json:"party_id,omitempty"`
	PartyName            string     `json:"party_name"`
	PartyPhone           *string    `json:"party_phone,omitempty"`
	PartyGSTIN           *string    `json:"party_gstin,omitempty"`
	PartyState           *string    `json:"party_state,omitempty"`
	StoreState           string     `json:"store_state"`
	StoreGSTIN           *string    `json:"store_gstin,omitempty"`
	RegistrationID       *string    `json:"registration_id,omitempty"` // GST registration (pehchaan) the document was issued under
	PlaceOfSupply        string     `json:"place_of_supply"`
	InterState           bool       `json:"inter_state"`
	SupplyCategory       string     `json:"supply_category,omitempty"`
	ReverseCharge        bool       `json:"reverse_charge,omitempty"`
	Status               string     `json:"status"`
	PaymentStatus        string     `json:"payment_status"`
	TaxablePaise         int64      `json:"taxable_paise"`
	DiscountPaise        int64      `json:"discount_paise"`
	InvoiceDiscountPaise int64      `json:"invoice_discount_paise,omitempty"`
	CGSTPaise            int64      `json:"cgst_paise"`
	SGSTPaise            int64      `json:"sgst_paise"`
	IGSTPaise            int64      `json:"igst_paise"`
	CessPaise            int64      `json:"cess_paise"`
	TCSPaise             int64      `json:"tcs_paise,omitempty"`
	RoundOffPaise        int64      `json:"round_off_paise"`
	GrandTotalPaise      int64      `json:"grand_total_paise"`
	PaidPaise            int64      `json:"paid_paise"`
	ChangePaise          int64      `json:"change_paise,omitempty"`
	CreditedPaise        int64      `json:"credited_paise,omitempty"` // credit/debit notes posted against this document
	BalancePaise         int64      `json:"balance_paise"`            // grand total − credited − paid (never below 0)
	AmountInWords        string     `json:"amount_in_words"`
	IRN                  *string    `json:"irn,omitempty"`
	AckNo                *string    `json:"ack_no,omitempty"`
	AckDate              *time.Time `json:"ack_date,omitempty"`
	SignedQR             *string    `json:"signed_qr,omitempty"`
	EInvoiceStatus       *string    `json:"einvoice_status,omitempty"` // generated | cancelled | failed
	EInvoiceError        *string    `json:"einvoice_error,omitempty"`
	EInvoiceErrorCode    *string    `json:"einvoice_error_code,omitempty"`
	IRNCancelledAt       *time.Time `json:"irn_cancelled_at,omitempty"`
	EWBNo                *string    `json:"ewb_no,omitempty"`
	EWBDate              *time.Time `json:"ewb_date,omitempty"`
	EWBValidUpto         *time.Time `json:"ewb_valid_upto,omitempty"`
	EWBStatus            *string    `json:"ewb_status,omitempty"` // generated | updated | extended | cancelled | failed
	EWBError             *string    `json:"ewb_error,omitempty"`
	EWBVehicleNo         *string    `json:"ewb_vehicle_no,omitempty"`
	EWBDistanceKM        *int       `json:"ewb_distance_km,omitempty"`
	Notes                *string    `json:"notes,omitempty"`
	ClientUUID           *string    `json:"client_uuid,omitempty"`
	CreatedBy            string     `json:"created_by"`
	CreatedAt            time.Time  `json:"created_at"`
	CancelledAt          *time.Time `json:"cancelled_at,omitempty"`
	CancelReason         *string    `json:"cancel_reason,omitempty"`
	Lines                []Line     `json:"lines,omitempty"`
	Tenders              []Tender   `json:"tenders,omitempty"`
}

// Line is a stored invoice line.
type Line struct {
	LineNo           int     `json:"line_no"`
	ItemID           *string `json:"item_id,omitempty"`
	Name             string  `json:"name"`
	HSN              *string `json:"hsn,omitempty"`
	Unit             string  `json:"unit"`
	BatchNo          *string `json:"batch_no,omitempty"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"`
	DiscountBP       int     `json:"discount_bp,omitempty"`
	TaxRateBP        int     `json:"tax_rate_bp"`
	CessRateBP       int     `json:"cess_rate_bp"`
	CessPerUnitPaise int64   `json:"cess_per_unit_paise,omitempty"`
	TaxCategory      string  `json:"tax_category"`
	ITCEligibility   string  `json:"itc_eligibility,omitempty"` // purchase documents only
	PriceIncludesTax bool    `json:"price_includes_tax"`
	TaxablePaise     int64   `json:"taxable_paise"`
	CGSTPaise        int64   `json:"cgst_paise"`
	SGSTPaise        int64   `json:"sgst_paise"`
	IGSTPaise        int64   `json:"igst_paise"`
	CessPaise        int64   `json:"cess_paise"`
	TotalPaise       int64   `json:"total_paise"`
}

// Filter narrows GET /v1/invoices.
type Filter struct {
	DocType        string
	StoreID        string
	PartyID        string
	RefInvoiceID   string // notes against one original (returns screen)
	RegistrationID string // documents issued under one GST registration
	StoreGSTIN     string // ... or under one GSTIN (kar returns are per GSTIN)
	Status         string
	PaymentStatus  string
	From, To       string
	Search         string
	IncludeLines   bool // ?include=lines: attach lines and tenders (kar returns, buddhi)
	httpx.Page
}

// NumberingSetting is a store's custom prefix for a document type.
type NumberingSetting struct {
	StoreID string `json:"store_id"`
	DocType string `json:"doc_type"`
	Prefix  string `json:"prefix"`
}

// Validate checks a numbering setting.
func (n *NumberingSetting) Validate() error {
	n.Prefix = strings.ToUpper(strings.TrimSpace(n.Prefix))
	if _, ok := defaultPrefix[n.DocType]; !ok {
		return httpx.Validation("doc_type must be sale, purchase, sale_return or purchase_return")
	}
	if n.StoreID == "" {
		return httpx.Validation("store_id is required")
	}
	if !prefixPattern.MatchString(n.Prefix) {
		return httpx.Validation("prefix must be 1-4 characters of A-Z, 0-9 or '-'")
	}
	return nil
}

// Block is a reserved range of numbers for an offline device.
type Block struct {
	ID         string     `json:"id"`
	DocType    string     `json:"doc_type"`
	FY         string     `json:"fy"`
	Prefix     string     `json:"prefix"`
	DeviceID   string     `json:"device_id"`
	StartNo    int64      `json:"start_no"`
	EndNo      int64      `json:"end_no"`
	LastUsedNo *int64     `json:"last_used_no,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

// FormatNumber renders a serial: PFX/26-27/00001.
func FormatNumber(prefix, fy string, seq int64) string {
	s := itoa(seq)
	for len(s) < 5 {
		s = "0" + s
	}
	return prefix + "/" + fy + "/" + s
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// FinancialYear returns the Indian FY label ("26-27") for d.
func FinancialYear(d time.Time) string {
	y := d.Year()
	if d.Month() < time.April {
		y--
	}
	return twoDigit(y%100) + "-" + twoDigit((y+1)%100)
}

func twoDigit(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// Settle works out what the counter collected. paid is what counts towards
// the bill (never more than the total), change is cash handed back, and
// credit is what the party still owes. Non-cash tenders cannot exceed the
// total: a card machine does not give change.
func Settle(total int64, tenders []Tender) (paid, change, credit int64, err error) {
	var cash, nonCash int64
	for _, t := range tenders {
		switch t.Mode {
		case "credit":
			credit += t.AmountPaise
		case "cash":
			cash += t.AmountPaise
		default:
			nonCash += t.AmountPaise
		}
	}
	if nonCash > total {
		return 0, 0, 0, httpx.Validation("card/UPI/bank tenders exceed the bill total")
	}
	paid = cash + nonCash
	if paid > total {
		change = paid - total
		paid = total
	}
	if credit == 0 && paid < total {
		credit = total - paid // unpaid remainder is on account whether or not a credit tender was written
	} else if credit > total-paid {
		credit = total - paid
	}
	return paid, change, credit, nil
}
