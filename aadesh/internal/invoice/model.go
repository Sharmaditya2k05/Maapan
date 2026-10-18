// Package invoice is aadesh's core: creating, numbering, cancelling and
// syncing GST invoices, and reacting to payments.
package invoice

import (
	"errors"
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

var defaultPrefix = map[string]string{
	DocSale: "INV", DocPurchase: "PUR", DocSaleReturn: "CN", DocPurchaseReturn: "DN",
}

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
	ItemID   string `json:"item_id,omitempty"`
	Name     string `json:"name"`
	Unit     string `json:"unit,omitempty"`
	BatchNo  string `json:"batch_no,omitempty"`
	gst.Line        // hsn, qty, unit_price_paise, discount_paise, tax_rate_bp, cess_rate_bp, price_includes_tax
}

// Tender is how part of the bill was paid at the counter.
type Tender struct {
	Mode        string `json:"mode"`
	AmountPaise int64  `json:"amount_paise"`
	Reference   string `json:"reference,omitempty"`
}

// CreateInput is the body of POST /v1/invoices and each item of POST /v1/sync/invoices.
type CreateInput struct {
	DocType           string      `json:"doc_type"`
	StoreID           string      `json:"store_id,omitempty"`     // defaults to the active store
	InvoiceDate       string      `json:"invoice_date,omitempty"` // YYYY-MM-DD, defaults to today IST
	PartyID           string      `json:"party_id,omitempty"`
	Party             *PartyInput `json:"party,omitempty"`
	PlaceOfSupply     string      `json:"place_of_supply,omitempty"`
	SupplierInvoiceNo string      `json:"supplier_invoice_no,omitempty"`
	RefInvoiceID      string      `json:"ref_invoice_id,omitempty"`
	Lines             []LineInput `json:"lines"`
	Tenders           []Tender    `json:"tenders,omitempty"`
	Notes             string      `json:"notes,omitempty"`
	RoundOff          *bool       `json:"round_off,omitempty"` // default true

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
	if in.InvoiceDate != "" {
		if _, err := time.Parse(time.DateOnly, in.InvoiceDate); err != nil {
			return httpx.Validation("invoice_date must be YYYY-MM-DD")
		}
	}
	for _, t := range in.Tenders {
		if !validTender[t.Mode] || t.AmountPaise <= 0 {
			return httpx.Validation("tender mode must be cash/card/upi/bank/credit/other with a positive amount")
		}
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
	ID                string     `json:"id"`
	StoreID           string     `json:"store_id"`
	DocType           string     `json:"doc_type"`
	Number            string     `json:"number"`
	FY                string     `json:"fy"`
	InvoiceDate       string     `json:"invoice_date"`
	SupplierInvoiceNo *string    `json:"supplier_invoice_no,omitempty"`
	RefInvoiceID      *string    `json:"ref_invoice_id,omitempty"`
	PartyID           *string    `json:"party_id,omitempty"`
	PartyName         string     `json:"party_name"`
	PartyPhone        *string    `json:"party_phone,omitempty"`
	PartyGSTIN        *string    `json:"party_gstin,omitempty"`
	PartyState        *string    `json:"party_state,omitempty"`
	StoreState        string     `json:"store_state"`
	PlaceOfSupply     string     `json:"place_of_supply"`
	InterState        bool       `json:"inter_state"`
	Status            string     `json:"status"`
	PaymentStatus     string     `json:"payment_status"`
	TaxablePaise      int64      `json:"taxable_paise"`
	DiscountPaise     int64      `json:"discount_paise"`
	CGSTPaise         int64      `json:"cgst_paise"`
	SGSTPaise         int64      `json:"sgst_paise"`
	IGSTPaise         int64      `json:"igst_paise"`
	CessPaise         int64      `json:"cess_paise"`
	RoundOffPaise     int64      `json:"round_off_paise"`
	GrandTotalPaise   int64      `json:"grand_total_paise"`
	PaidPaise         int64      `json:"paid_paise"`
	AmountInWords     string     `json:"amount_in_words"`
	Notes             *string    `json:"notes,omitempty"`
	ClientUUID        *string    `json:"client_uuid,omitempty"`
	CreatedBy         string     `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	CancelledAt       *time.Time `json:"cancelled_at,omitempty"`
	CancelReason      *string    `json:"cancel_reason,omitempty"`
	Lines             []Line     `json:"lines,omitempty"`
	Tenders           []Tender   `json:"tenders,omitempty"`
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
	TaxRateBP        int     `json:"tax_rate_bp"`
	CessRateBP       int     `json:"cess_rate_bp"`
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
	DocType       string
	StoreID       string
	PartyID       string
	Status        string
	PaymentStatus string
	From, To      string
	Search        string
	IncludeLines  bool // ?include=lines: attach lines and tenders (kar returns, buddhi)
	httpx.Page
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
