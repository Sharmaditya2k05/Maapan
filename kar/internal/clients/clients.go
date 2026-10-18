// Package clients wraps kar's read-only calls to aadesh (invoices), pehchaan
// (store/tenant) and bahi (party address).
package clients

import (
	"context"
	"fmt"
	"net/url"

	"github.com/taraju/mool/svc"
)

// Invoice mirrors the fields of aadesh's invoice JSON that tax documents need.
type Invoice struct {
	ID                string `json:"id"`
	StoreID           string `json:"store_id"`
	DocType           string `json:"doc_type"`
	Number            string `json:"number"`
	FY                string `json:"fy"`
	InvoiceDate       string `json:"invoice_date"` // YYYY-MM-DD
	SupplierInvoiceNo string `json:"supplier_invoice_no"`
	RefInvoiceID      string `json:"ref_invoice_id"`
	PartyID           string `json:"party_id"`
	PartyName         string `json:"party_name"`
	PartyPhone        string `json:"party_phone"`
	PartyGSTIN        string `json:"party_gstin"`
	PartyState        string `json:"party_state"`
	StoreState        string `json:"store_state"`
	PlaceOfSupply     string `json:"place_of_supply"`
	InterState        bool   `json:"inter_state"`
	Status            string `json:"status"`
	TaxablePaise      int64  `json:"taxable_paise"`
	DiscountPaise     int64  `json:"discount_paise"`
	CGSTPaise         int64  `json:"cgst_paise"`
	SGSTPaise         int64  `json:"sgst_paise"`
	IGSTPaise         int64  `json:"igst_paise"`
	CessPaise         int64  `json:"cess_paise"`
	RoundOffPaise     int64  `json:"round_off_paise"`
	GrandTotalPaise   int64  `json:"grand_total_paise"`
	Lines             []Line `json:"lines"`
}

// Line mirrors an aadesh invoice line.
type Line struct {
	LineNo         int     `json:"line_no"`
	ItemID         string  `json:"item_id"`
	Name           string  `json:"name"`
	HSN            string  `json:"hsn"`
	Unit           string  `json:"unit"`
	Qty            float64 `json:"qty"`
	UnitPricePaise int64   `json:"unit_price_paise"`
	DiscountPaise  int64   `json:"discount_paise"`
	TaxRateBP      int     `json:"tax_rate_bp"`
	CessRateBP     int     `json:"cess_rate_bp"`
	TaxablePaise   int64   `json:"taxable_paise"`
	CGSTPaise      int64   `json:"cgst_paise"`
	SGSTPaise      int64   `json:"sgst_paise"`
	IGSTPaise      int64   `json:"igst_paise"`
	CessPaise      int64   `json:"cess_paise"`
	TotalPaise     int64   `json:"total_paise"`
}

// Store is pehchaan's store.
type Store struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	StateCode string `json:"state_code"`
	GSTIN     string `json:"gstin"`
	Address   string `json:"address"`
	Phone     string `json:"phone"`
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

// Store fetches a store.
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

// InvoicesBetween returns every invoice dated from..to (inclusive, YYYY-MM-DD),
// with lines and tenders, in pages of 500 (aadesh ?include=lines).
func (d *Directory) InvoicesBetween(ctx context.Context, from, to string) ([]Invoice, error) {
	const pageSize = 500
	var all []Invoice
	for offset := 0; ; offset += pageSize {
		var page invoicePage
		q := url.Values{"from": {from}, "to": {to}, "include": {"lines"},
			"limit": {fmt.Sprint(pageSize)}, "offset": {fmt.Sprint(offset)}}
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
