// Package clients wraps kar's read-only calls to aadesh (invoices), pehchaan
// (store/tenant/registration) and bahi (party address).
package clients

import (
	"context"
	"fmt"
	"net/url"

	"github.com/maapan/mool/svc"
)

// Invoice mirrors the fields of aadesh's invoice JSON that tax documents need.
type Invoice struct {
	ID                  string `json:"id"`
	StoreID             string `json:"store_id"`
	RegistrationID      string `json:"registration_id,omitempty"`
	DocType             string `json:"doc_type"`
	Number              string `json:"number"`
	FY                  string `json:"fy"`
	InvoiceDate         string `json:"invoice_date"` // YYYY-MM-DD
	SupplierInvoiceNo   string `json:"supplier_invoice_no"`
	SupplierInvoiceDate string `json:"supplier_invoice_date,omitempty"`
	RefInvoiceID        string `json:"ref_invoice_id"`
	PartyID             string `json:"party_id"`
	PartyName           string `json:"party_name"`
	PartyPhone          string `json:"party_phone"`
	PartyGSTIN          string `json:"party_gstin"`
	PartyState          string `json:"party_state"`
	StoreState          string `json:"store_state"`
	StoreGSTIN          string `json:"store_gstin,omitempty"`
	PlaceOfSupply       string `json:"place_of_supply"`
	InterState          bool   `json:"inter_state"`
	SupplyCategory      string `json:"supply_category,omitempty"` // "", sez_with_payment, sez_without_payment, export_with_payment, export_without_payment, deemed_export
	ReverseCharge       bool   `json:"reverse_charge,omitempty"`
	BillOfSupply        bool   `json:"bill_of_supply,omitempty"`
	Status              string `json:"status"`
	TaxablePaise        int64  `json:"taxable_paise"`
	DiscountPaise       int64  `json:"discount_paise"`
	CGSTPaise           int64  `json:"cgst_paise"`
	SGSTPaise           int64  `json:"sgst_paise"`
	IGSTPaise           int64  `json:"igst_paise"`
	CessPaise           int64  `json:"cess_paise"`
	TCSPaise            int64  `json:"tcs_paise,omitempty"`
	RoundOffPaise       int64  `json:"round_off_paise"`
	GrandTotalPaise     int64  `json:"grand_total_paise"`
	IRN                 string `json:"irn,omitempty"`
	EInvoiceStatus      string `json:"einvoice_status,omitempty"`
	EWBNo               string `json:"ewb_no,omitempty"`
	Lines               []Line `json:"lines"`
}

// Line mirrors an aadesh invoice line.
type Line struct {
	LineNo           int     `json:"line_no"`
	ItemID           string  `json:"item_id"`
	Name             string  `json:"name"`
	HSN              string  `json:"hsn"`
	Unit             string  `json:"unit"`
	Qty              float64 `json:"qty"`
	UnitPricePaise   int64   `json:"unit_price_paise"`
	DiscountPaise    int64   `json:"discount_paise"`
	TaxRateBP        int     `json:"tax_rate_bp"`
	CessRateBP       int     `json:"cess_rate_bp"`
	CessPerUnitPaise int64   `json:"cess_per_unit_paise,omitempty"`
	TaxCategory      string  `json:"tax_category,omitempty"`    // taxable | exempt | nil_rated | non_gst
	ITCEligibility   string  `json:"itc_eligibility,omitempty"` // purchases: eligible | ineligible_17_5 | ineligible_other
	TaxablePaise     int64   `json:"taxable_paise"`
	CGSTPaise        int64   `json:"cgst_paise"`
	SGSTPaise        int64   `json:"sgst_paise"`
	IGSTPaise        int64   `json:"igst_paise"`
	CessPaise        int64   `json:"cess_paise"`
	TotalPaise       int64   `json:"total_paise"`
}

// Store is pehchaan's store lookup. Since Phase 3 it carries the store's
// registration (GSTIN) details; gstin/state_code/address come from the
// registration when the store has none of its own.
type Store struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenant_id"`
	Name                string `json:"name"`
	Code                string `json:"code"`
	StateCode           string `json:"state_code"`
	GSTIN               string `json:"gstin"`
	Address             string `json:"address"`
	Phone               string `json:"phone"`
	RegistrationID      string `json:"registration_id,omitempty"`
	LegalName           string `json:"legal_name,omitempty"`
	TradeName           string `json:"trade_name,omitempty"`
	City                string `json:"city,omitempty"`
	Pin                 string `json:"pin,omitempty"`
	Composition         bool   `json:"composition,omitempty"`
	EInvoiceApplicable  bool   `json:"einvoice_applicable,omitempty"`
	RegistrationPrimary bool   `json:"registration_primary,omitempty"`
}

// Registration is one GSTIN of a business (pehchaan, PHASE3-CONTRACTS §1).
type Registration struct {
	ID                 string `json:"id"`
	TenantID           string `json:"tenant_id"`
	GSTIN              string `json:"gstin"` // "" = unregistered identity
	LegalName          string `json:"legal_name"`
	TradeName          string `json:"trade_name"`
	StateCode          string `json:"state_code"`
	Address            string `json:"address"`
	City               string `json:"city"`
	Pin                string `json:"pin"`
	Phone              string `json:"phone"`
	Email              string `json:"email"`
	Composition        bool   `json:"composition"`
	EInvoiceApplicable bool   `json:"einvoice_applicable"`
	IsPrimary          bool   `json:"is_primary"`
	Status             string `json:"status"`
}

// Tenant is pehchaan's tenant (business).
type Tenant struct {
	ID           string `json:"id"`
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin"`
	StateCode    string `json:"state_code"`
	OwnerPhone   string `json:"owner_phone"`
}

// Party is bahi's party.
type Party struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	GSTIN     string `json:"gstin"`
	StateCode string `json:"state_code"`
	Address   string `json:"address"`
}

// Directory groups the downstream clients.
type Directory struct {
	Aadesh   *svc.Client
	Pehchaan *svc.Client
	Bahi     *svc.Client
}

// Invoice fetches one invoice with lines.
func (d *Directory) Invoice(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := d.Aadesh.Get(ctx, "/internal/v1/invoices/"+url.PathEscape(id), &inv)
	return inv, err
}

// Store fetches a store (with its registration fields).
func (d *Directory) Store(ctx context.Context, id string) (Store, error) {
	var s Store
	err := d.Pehchaan.Get(ctx, "/internal/v1/stores/"+url.PathEscape(id), &s)
	return s, err
}

// Tenant fetches a tenant.
func (d *Directory) Tenant(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	err := d.Pehchaan.Get(ctx, "/internal/v1/tenants/"+url.PathEscape(id), &t)
	return t, err
}

// Registration fetches one registration.
func (d *Directory) Registration(ctx context.Context, id string) (Registration, error) {
	var r Registration
	err := d.Pehchaan.Get(ctx, "/internal/v1/registrations/"+url.PathEscape(id), &r)
	return r, err
}

// Registrations lists a tenant's registrations (GSTINs).
func (d *Directory) Registrations(ctx context.Context, tenantID string) ([]Registration, error) {
	var page struct {
		Items []Registration `json:"items"`
	}
	err := d.Pehchaan.Get(ctx, "/internal/v1/registrations?"+url.Values{"tenant_id": {tenantID}}.Encode(), &page)
	return page.Items, err
}

// Party fetches a party.
func (d *Directory) Party(ctx context.Context, id string) (Party, error) {
	var p Party
	err := d.Bahi.Get(ctx, "/internal/v1/parties/"+url.PathEscape(id), &p)
	return p, err
}

type invoicePage struct {
	Items []Invoice `json:"items"`
	Total int       `json:"total"`
}

// InvoiceFilter narrows InvoicesWhere. From/To are inclusive YYYY-MM-DD.
type InvoiceFilter struct {
	From, To       string
	StoreGSTIN     string // only invoices issued under this GSTIN
	RegistrationID string // only invoices of this registration
	DocType        string // optional
}

// InvoicesBetween returns every invoice dated from..to (inclusive, YYYY-MM-DD),
// with lines and tenders, in pages of 500 (aadesh ?include=lines).
func (d *Directory) InvoicesBetween(ctx context.Context, from, to string) ([]Invoice, error) {
	return d.InvoicesWhere(ctx, InvoiceFilter{From: from, To: to})
}

// InvoicesWhere pages through aadesh's internal invoice list with lines.
func (d *Directory) InvoicesWhere(ctx context.Context, f InvoiceFilter) ([]Invoice, error) {
	const pageSize = 500
	var all []Invoice
	for offset := 0; ; offset += pageSize {
		var page invoicePage
		q := url.Values{"from": {f.From}, "to": {f.To}, "include": {"lines"},
			"limit": {fmt.Sprint(pageSize)}, "offset": {fmt.Sprint(offset)}}
		if f.StoreGSTIN != "" {
			q.Set("store_gstin", f.StoreGSTIN)
		}
		if f.RegistrationID != "" {
			q.Set("registration_id", f.RegistrationID)
		}
		if f.DocType != "" {
			q.Set("doc_type", f.DocType)
		}
		if err := d.Aadesh.Get(ctx, "/internal/v1/invoices?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if len(page.Items) < pageSize || offset+pageSize >= page.Total {
			break
		}
	}
	return all, nil
}
