package returns

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"github.com/taraju/kar/internal/recon"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Register mounts the returns, GSTR-2B and IMS routes:
//
//	GET  /v1/returns/gstr1?period=&gstin=[&format=json|csv&section=]   report:read (downloads need gst:file)
//	GET  /v1/returns/gstr1/export?period=&gstin=&format=json|csv&section= gst:file
//	GET  /v1/returns/gstr3b?period=&gstin=[&format=json|csv]           report:read (downloads need gst:file)
//	GET  /v1/returns/itc-opening?period=&gstin=                          report:read
//	PUT  /v1/returns/itc-opening                                         gst:file
//	POST /v1/returns/gstr2b/import?gstin=&period=                        gst:file
//	GET  /v1/returns/gstr2b?gstin=&period=[&status=]                     report:read
//	POST /v1/returns/gstr2b/fetch?gstin=&period=                         gst:file
//	PUT  /v1/returns/ims/{doc_key}?gstin=                                gst:file
//	POST /v1/returns/ims/accept-matched?gstin=&period=                   gst:file
//	GET  /v1/returns/ims/export?gstin=&period=                           gst:file
func Register(mux *http.ServeMux, v *auth.Verifier, d Deps) {
	s := newService(d)
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	mux.Handle("GET /v1/returns/gstr1", user(s.gstr1, auth.PermReportRead))
	mux.Handle("GET /v1/returns/gstr1/export", user(s.gstr1Export, auth.PermGSTFile))
	mux.Handle("GET /v1/returns/gstr3b", user(s.gstr3b, auth.PermReportRead))
	mux.Handle("GET /v1/returns/itc-opening", user(s.getOpening, auth.PermReportRead))
	mux.Handle("PUT /v1/returns/itc-opening", user(s.putOpening, auth.PermGSTFile))

	mux.Handle("POST /v1/returns/gstr2b/import", user(s.rc.Import, auth.PermGSTFile))
	mux.Handle("GET /v1/returns/gstr2b", user(s.rc.List, auth.PermReportRead))
	mux.Handle("POST /v1/returns/gstr2b/fetch", user(s.rc.Fetch, auth.PermGSTFile))
	mux.Handle("PUT /v1/returns/ims/{doc_key}", user(s.rc.SetAction, auth.PermGSTFile))
	mux.Handle("POST /v1/returns/ims/accept-matched", user(s.rc.AcceptMatched, auth.PermGSTFile))
	mux.Handle("GET /v1/returns/ims/export", user(s.rc.Export, auth.PermGSTFile))
}

type service struct {
	d  Deps
	rc *recon.Service
}

func newService(d Deps) *service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &service{d: d}
	s.rc = &recon.Service{DB: d.DB, Purchases: s.purchases, Log: d.Log,
		ResolveGSTIN: func(r *http.Request) (string, error) {
			reg, err := s.registration(r.Context(), httpx.Query(r, "gstin"))
			return reg.GSTIN, err
		}}
	if d.TwoB != nil {
		s.rc.Fetcher = d.TwoB
	}
	return s
}

// registration resolves want (a GSTIN, or "" for the primary) against the
// tenant's active registrations that have a GSTIN.
func (s *service) registration(ctx context.Context, want string) (Registration, error) {
	p := auth.FromContext(ctx)
	regs, err := s.d.Registrations.Registrations(ctx, p.TenantID)
	if err != nil {
		return Registration{}, err
	}
	var usable []Registration
	for _, r := range regs {
		r.GSTIN = gst.Normalize(r.GSTIN)
		if r.GSTIN == "" || (r.Status != "" && r.Status != "active") {
			continue
		}
		if r.StateCode == "" {
			r.StateCode = gst.StateCode(r.GSTIN)
		}
		usable = append(usable, r)
	}
	want = gst.Normalize(want)
	if want != "" {
		for _, r := range usable {
			if r.GSTIN == want {
				return r, nil
			}
		}
		return Registration{}, httpx.Validation("gstin %s is not an active registration of this business", want)
	}
	if len(usable) == 0 {
		return Registration{}, httpx.Validation("this business has no active GST registration; add the GSTIN in settings first")
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].IsPrimary && !usable[j].IsPrimary })
	return usable[0], nil
}

// b2clThreshold reads the tenant's B2CL limit from tax settings (default ₹1,00,000).
func (s *service) b2clThreshold(ctx context.Context, tenantID string) int64 {
	if s.d.DB == nil {
		return DefaultB2CLThreshold
	}
	var v int64
	if err := s.d.DB.QueryRow(ctx, `SELECT b2cl_threshold_paise FROM tax_settings WHERE tenant_id=$1`, tenantID).Scan(&v); err != nil || v <= 0 {
		return DefaultB2CLThreshold
	}
	return v
}

// purchases is the recon.PurchaseLoader on top of the invoice source.
func (s *service) purchases(ctx context.Context, gstin, from, to string) ([]recon.Purchase, error) {
	out := []recon.Purchase{}
	err := s.d.Invoices.EachInvoice(ctx, InvoiceQuery{GSTIN: gstin, From: from, To: to, DocTypes: []string{"purchase", "purchase_return"},
		IncludeLines: true},
		func(inv Invoice) error {
			if inv.Status != "posted" || gst.Normalize(inv.StoreGSTIN) != gstin {
				return nil
			}
			// The supplier reports nil/exempt/non-GST lines outside 2B, so
			// compare only the taxed part of the bill's value.
			taxable := inv.TaxablePaise
			if len(inv.Lines) > 0 {
				taxable = 0
				for _, l := range inv.Lines {
					if k, _ := lineKind(inv.SupplyCategory, l); k == kindTaxed {
						taxable += l.TaxablePaise
					}
				}
			}
			out = append(out, recon.Purchase{ID: inv.ID, DocType: inv.DocType, Number: inv.Number, InvoiceDate: inv.InvoiceDate,
				SupplierGSTIN: gst.Normalize(inv.PartyGSTIN), SupplierName: inv.PartyName, SupplierInvoiceNo: inv.SupplierInvoiceNo,
				SupplierInvoiceDate: inv.SupplierInvoiceDate, ReverseCharge: inv.ReverseCharge, Taxable: Rs(taxable),
				IGST: Rs(inv.IGSTPaise), CGST: Rs(inv.CGSTPaise), SGST: Rs(inv.SGSTPaise), Cess: Rs(inv.CessPaise)})
			return nil
		})
	return out, err
}
