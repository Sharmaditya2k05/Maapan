package returns

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/internal/recon"
)

// Invoice is the part of aadesh's invoice JSON (GET /internal/v1/invoices,
// include=lines) that returns read. Pointer fields in aadesh decode to "" here.
type Invoice struct {
	ID                  string `json:"id"`
	StoreID             string `json:"store_id"`
	RegistrationID      string `json:"registration_id"`
	DocType             string `json:"doc_type"` // sale | sale_return | purchase | purchase_return
	Number              string `json:"number"`
	FY                  string `json:"fy"`
	InvoiceDate         string `json:"invoice_date"` // YYYY-MM-DD
	SupplierInvoiceNo   string `json:"supplier_invoice_no"`
	SupplierInvoiceDate string `json:"supplier_invoice_date"`
	RefInvoiceID        string `json:"ref_invoice_id"`
	PartyID             string `json:"party_id"`
	PartyName           string `json:"party_name"`
	PartyGSTIN          string `json:"party_gstin"`
	PartyState          string `json:"party_state"`
	StoreState          string `json:"store_state"`
	StoreGSTIN          string `json:"store_gstin"`
	PlaceOfSupply       string `json:"place_of_supply"`
	InterState          bool   `json:"inter_state"`
	SupplyCategory      string `json:"supply_category"` // gst.Supply*
	ReverseCharge       bool   `json:"reverse_charge"`
	Status              string `json:"status"` // posted | cancelled
	TaxablePaise        int64  `json:"taxable_paise"`
	CGSTPaise           int64  `json:"cgst_paise"`
	SGSTPaise           int64  `json:"sgst_paise"`
	IGSTPaise           int64  `json:"igst_paise"`
	CessPaise           int64  `json:"cess_paise"`
	GrandTotalPaise     int64  `json:"grand_total_paise"`
	// Export details (GSTR-1 Table 6A). aadesh does not capture them yet; when
	// absent the shipping-bill fields are left blank for the filer to complete.
	PortCode         string `json:"port_code,omitempty"`
	ShippingBillNo   string `json:"shipping_bill_no,omitempty"`
	ShippingBillDate string `json:"shipping_bill_date,omitempty"`
	Lines            []Line `json:"lines"`
}

// Line is an aadesh invoice line.
type Line struct {
	LineNo         int     `json:"line_no"`
	Name           string  `json:"name"`
	HSN            string  `json:"hsn"`
	Unit           string  `json:"unit"`
	Qty            float64 `json:"qty"`
	TaxRateBP      int     `json:"tax_rate_bp"`
	CessRateBP     int     `json:"cess_rate_bp"`
	TaxCategory    string  `json:"tax_category"`    // taxable | exempt | nil_rated | non_gst
	ITCEligibility string  `json:"itc_eligibility"` // purchases: eligible (default) | ineligible_17_5 | ineligible_other
	TaxablePaise   int64   `json:"taxable_paise"`
	CGSTPaise      int64   `json:"cgst_paise"`
	SGSTPaise      int64   `json:"sgst_paise"`
	IGSTPaise      int64   `json:"igst_paise"`
	CessPaise      int64   `json:"cess_paise"`
	TotalPaise     int64   `json:"total_paise"`
}

// Registration is one GSTIN of the business (pehchaan).
type Registration struct {
	ID                 string `json:"id"`
	TenantID           string `json:"tenant_id"`
	GSTIN              string `json:"gstin"`
	LegalName          string `json:"legal_name"`
	TradeName          string `json:"trade_name"`
	StateCode          string `json:"state_code"`
	Composition        bool   `json:"composition"`
	EInvoiceApplicable bool   `json:"einvoice_applicable"`
	IsPrimary          bool   `json:"is_primary"`
	Status             string `json:"status"`
}

// InvoiceQuery selects one GSTIN's documents of a date range.
type InvoiceQuery struct {
	GSTIN        string   // store_gstin filter
	From, To     string   // YYYY-MM-DD inclusive
	DocTypes     []string // one pass per type
	IncludeLines bool
}

// InvoiceSource streams invoices page by page so a month never has to sit in
// memory at once.
type InvoiceSource interface {
	EachInvoice(ctx context.Context, q InvoiceQuery, fn func(Invoice) error) error
	Invoice(ctx context.Context, id string) (Invoice, error)
}

// RegistrationSource lists a tenant's GST registrations.
type RegistrationSource interface {
	Registrations(ctx context.Context, tenantID string) ([]Registration, error)
}

// TwoBFetcher downloads GSTR-2B through a GSP (satisfied by the gsp client).
type TwoBFetcher = recon.TwoBFetcher

// TwoBFunc adapts a function to TwoBFetcher, e.g. to wrap the gsp client's
// FetchGSTR2B(ctx, credentials, gstin, period) with the GSTIN's portal credentials.
type TwoBFunc func(ctx context.Context, gstin, period string) ([]byte, error)

// FetchGSTR2B calls f.
func (f TwoBFunc) FetchGSTR2B(ctx context.Context, gstin, period string) ([]byte, error) {
	return f(ctx, gstin, period)
}

// Deps is what the returns routes need.
type Deps struct {
	DB            *pgxpool.Pool
	Invoices      InvoiceSource
	Registrations RegistrationSource
	TwoB          TwoBFetcher // optional; nil → POST /v1/returns/gstr2b/fetch answers 503
	Log           *slog.Logger
}
