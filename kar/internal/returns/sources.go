package returns

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/taraju/mool/httpx"
	"github.com/taraju/mool/svc"
)

// AadeshInvoices reads invoices from aadesh's internal API.
type AadeshInvoices struct {
	C        *svc.Client
	PageSize int // default 200 (aadesh caps at 500; lines make pages heavy)
}

type invoicePage struct {
	Items []Invoice `json:"items"`
	Total int       `json:"total"`
}

// EachInvoice pages through GET /internal/v1/invoices?store_gstin=&doc_type=&from=&to=
// and hands each invoice to fn. Only one page is held at a time. An invoice
// seen twice (offsets shift when a bill is created mid-read) is passed once.
func (a AadeshInvoices) EachInvoice(ctx context.Context, q InvoiceQuery, fn func(Invoice) error) error {
	size := a.PageSize
	if size <= 0 {
		size = 200
	}
	seen := map[string]struct{}{}
	types := q.DocTypes
	if len(types) == 0 {
		types = []string{""}
	}
	for _, dt := range types {
		for offset := 0; ; offset += size {
			v := url.Values{"from": {q.From}, "to": {q.To}, "limit": {fmt.Sprint(size)}, "offset": {fmt.Sprint(offset)}}
			if q.GSTIN != "" {
				v.Set("store_gstin", q.GSTIN)
			}
			if dt != "" {
				v.Set("doc_type", dt)
			}
			if q.IncludeLines {
				v.Set("include", "lines")
			}
			var page invoicePage
			if err := a.C.Get(ctx, "/internal/v1/invoices?"+v.Encode(), &page); err != nil {
				return err
			}
			for _, inv := range page.Items {
				if _, dup := seen[inv.ID]; dup && inv.ID != "" {
					continue
				}
				seen[inv.ID] = struct{}{}
				if err := fn(inv); err != nil {
					return err
				}
			}
			if len(page.Items) < size || offset+size >= page.Total {
				break
			}
		}
	}
	return nil
}

// Invoice fetches one invoice with lines.
func (a AadeshInvoices) Invoice(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := a.C.Get(ctx, "/internal/v1/invoices/"+url.PathEscape(id), &inv)
	return inv, err
}

// PehchaanRegistrations reads registrations from pehchaan.
type PehchaanRegistrations struct{ C *svc.Client }

// Registrations calls GET /internal/v1/registrations?tenant_id=. Until pehchaan
// serves registrations (404), it falls back to the tenant's own GSTIN as the
// single primary registration.
func (p PehchaanRegistrations) Registrations(ctx context.Context, tenantID string) ([]Registration, error) {
	var out struct {
		Items []Registration `json:"items"`
	}
	err := p.C.Get(ctx, "/internal/v1/registrations?"+url.Values{"tenant_id": {tenantID}}.Encode(), &out)
	if err == nil {
		return out.Items, nil
	}
	if e := httpx.AsError(err); e.Status != 404 && !strings.Contains(e.Message, "returned 404") {
		return nil, err
	}
	var t struct {
		ID           string `json:"id"`
		BusinessName string `json:"business_name"`
		GSTIN        string `json:"gstin"`
		StateCode    string `json:"state_code"`
	}
	if err := p.C.Get(ctx, "/internal/v1/tenants/"+url.PathEscape(tenantID), &t); err != nil {
		return nil, err
	}
	return []Registration{{ID: t.ID, TenantID: t.ID, GSTIN: t.GSTIN, LegalName: t.BusinessName, StateCode: t.StateCode,
		IsPrimary: true, Status: "active"}}, nil
}

// NewSources builds the default adapters on top of the service clients.
func NewSources(aadesh, pehchaan *svc.Client) (InvoiceSource, RegistrationSource) {
	return AadeshInvoices{C: aadesh}, PehchaanRegistrations{C: pehchaan}
}
