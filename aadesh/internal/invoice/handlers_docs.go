package invoice

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// docRoutes registers the document-flow, print and settings routes.
func (s *Service) docRoutes(mux *http.ServeMux, v *auth.Verifier) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	mux.Handle("POST /v1/orders", user(s.createOrder))
	mux.Handle("GET /v1/orders", user(s.listOrders, auth.PermInvoiceRead))
	mux.Handle("GET /v1/orders/{id}", user(s.getOrder, auth.PermInvoiceRead))
	mux.Handle("POST /v1/orders/{id}/convert", user(s.convertOrder))
	mux.Handle("POST /v1/orders/{id}/cancel", user(s.cancelOrder, auth.PermInvoiceCancel))
	mux.Handle("POST /v1/orders/{id}/close", user(s.closeOrder))

	mux.Handle("GET /v1/invoices/{id}/links", user(s.invoiceLinks, auth.PermInvoiceRead))
	mux.Handle("GET /v1/invoices/{id}/pdf", user(s.invoicePDF, auth.PermInvoiceRead))
	mux.Handle("GET /v1/invoices/{id}/print/thermal", user(s.thermal, auth.PermInvoiceRead))
	mux.Handle("GET /v1/settings/billing", user(s.billingSettings, auth.PermInvoiceRead))
	mux.Handle("PUT /v1/settings/billing", user(s.setBillingSettings, auth.PermSettingsManage))

	mux.Handle("GET /internal/v1/invoices/{id}/pdf-link", v.Internal(httpx.H(s.pdfLink)))
	mux.Handle("GET /internal/v1/invoices/{id}/pdf", v.Internal(httpx.H(s.invoicePDF)))
	mux.Handle("GET /internal/v1/orders/{id}", v.Internal(httpx.H(s.getOrder)))

	// Signed, expiring link for WhatsApp / e-mail attachments: no token, the signature is the credential.
	mux.Handle("GET /public/v1/invoices/{id}/pdf", httpx.H(s.publicPDF))
}

// orderPerm is the permission a write on an order kind needs.
func orderPerm(kind string) string {
	if IsPurchaseKind(kind) {
		return auth.PermPurchaseManage
	}
	return auth.PermInvoiceCreate
}

func (s *Service) createOrder(w http.ResponseWriter, r *http.Request) error {
	var in OrderInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if _, ok := orderPrefix[in.Kind]; ok && !p.Can(orderPerm(in.Kind)) {
		return httpx.Forbidden("missing permission %s", orderPerm(in.Kind))
	}
	o, created, err := s.CreateOrder(r.Context(), p, in)
	if err != nil {
		return err
	}
	if !created {
		return httpx.OK(w, o)
	}
	return httpx.Created(w, o)
}

func (s *Service) listOrders(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	f := OrderFilter{
		Kind: httpx.Query(r, "kind"), StoreID: httpx.Query(r, "store_id"), PartyID: httpx.Query(r, "party_id"),
		Status: httpx.Query(r, "status"), OpenOnly: httpx.Query(r, "open") == "true",
		From: httpx.Query(r, "from"), To: httpx.Query(r, "to"), Search: httpx.Query(r, "q"), Page: httpx.ParsePage(r),
	}
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		if f.StoreID == "" {
			f.StoreID = p.StoreID
		}
		if !p.Claims.CanAccessStore(f.StoreID) {
			return httpx.Forbidden("no access to store")
		}
	}
	items, total, err := s.ListOrders(r.Context(), p.TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Order]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) getOrder(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	o, err := s.GetOrder(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(o.StoreID) {
		return httpx.NotFound("order not found")
	}
	return httpx.OK(w, o)
}

// orderKindFor loads just enough of an order to check the caller's permission.
func (s *Service) requireOrderPerm(r *http.Request) error {
	p := auth.FromContext(r.Context())
	o, err := s.GetOrder(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if perm := orderPerm(o.Kind); !p.Can(perm) {
		return httpx.Forbidden("missing permission %s", perm)
	}
	return nil
}

func (s *Service) convertOrder(w http.ResponseWriter, r *http.Request) error {
	var in ConvertInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.requireOrderPerm(r); err != nil {
		return err
	}
	res, created, err := s.ConvertOrder(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	if !created {
		return httpx.OK(w, res)
	}
	return httpx.Created(w, res)
}

func (s *Service) cancelOrder(w http.ResponseWriter, r *http.Request) error {
	var in cancelInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.requireOrderPerm(r); err != nil {
		return err
	}
	o, err := s.CancelOrder(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in.Reason)
	if err != nil {
		return err
	}
	return httpx.OK(w, o)
}

func (s *Service) closeOrder(w http.ResponseWriter, r *http.Request) error {
	var in cancelInput
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	if err := s.requireOrderPerm(r); err != nil {
		return err
	}
	o, err := s.CloseOrder(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in.Reason)
	if err != nil {
		return err
	}
	return httpx.OK(w, o)
}

// visibleInvoice loads an invoice the caller may see.
func (s *Service) visibleInvoice(r *http.Request) (*Invoice, error) {
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return nil, httpx.NotFound("invoice not found")
	}
	inv, err := s.Get(r.Context(), p.TenantID, id)
	if err != nil {
		return nil, err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(inv.StoreID) {
		return nil, httpx.NotFound("invoice not found")
	}
	return inv, nil
}

func (s *Service) invoiceLinks(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.visibleInvoice(r)
	if err != nil {
		return err
	}
	links, err := s.InvoiceLinks(r.Context(), inv.ID)
	if err != nil {
		return err
	}
	if links == nil {
		links = []DocLink{}
	}
	return httpx.OK(w, map[string]any{"source_order_id": inv.SourceOrderID, "links": links})
}

func (s *Service) writePDF(w http.ResponseWriter, r *http.Request, tenantID string, inv *Invoice) error {
	pdf, err := RenderInvoicePDF(inv, s.printData(r.Context(), tenantID, inv))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/pdf")
	disp := "inline"
	if httpx.Query(r, "download") == "true" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+`; filename="`+PDFFileName(inv.Number)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = w.Write(pdf)
	return err
}

func (s *Service) invoicePDF(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.visibleInvoice(r)
	if err != nil {
		return err
	}
	return s.writePDF(w, r, auth.FromContext(r.Context()).TenantID, inv)
}

// publicPDF serves an invoice PDF to whoever holds a valid signed link. Every
// failure is the same 404, so a link prober learns nothing.
func (s *Service) publicPDF(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	notFound := httpx.NotFound("link expired or invalid")
	if _, err := uuid.Parse(id); err != nil {
		return notFound
	}
	if !VerifyPDFLink(s.PDFKey, id, httpx.Query(r, "exp"), httpx.Query(r, "sig"), s.Now()) {
		return notFound
	}
	var tenantID string
	if err := s.DB.QueryRow(r.Context(), `SELECT tenant_id::text FROM invoices WHERE id=$1`, id).Scan(&tenantID); err != nil {
		return notFound
	}
	inv, err := s.Get(r.Context(), tenantID, id)
	if err != nil {
		return notFound
	}
	logx.From(r.Context()).Info("public invoice pdf served", "invoice_id", id, "tenant_id", tenantID)
	return s.writePDF(w, r, tenantID, inv)
}

// pdfLink mints a fresh signed public URL for soochna (?ttl= seconds, ≤ 24 h).
func (s *Service) pdfLink(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.Validation("X-Tenant-ID is required")
	}
	inv, err := s.visibleInvoice(r)
	if err != nil {
		return err
	}
	if s.PublicBaseURL == "" || len(s.PDFKey) == 0 {
		return httpx.Unavailable("public PDF links are not configured (PUBLIC_BASE_URL)")
	}
	ttl := PDFLinkTTL
	if v, err := strconv.Atoi(httpx.Query(r, "ttl")); err == nil && v > 0 {
		ttl = min(time.Duration(v)*time.Second, MaxPDFLinkTTL)
	}
	exp := s.Now().Add(ttl).Truncate(time.Second)
	return httpx.OK(w, PDFLink{URL: s.pdfLinkURL(inv.ID, exp), ExpiresAt: exp.UTC(), FileName: PDFFileName(inv.Number), Number: inv.Number})
}

// thermal returns the receipt for a 2"/3" printer: ?width=58|80 (default: the
// tenant setting), ?format=escpos (raw bytes, default) | text | json.
func (s *Service) thermal(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.visibleInvoice(r)
	if err != nil {
		return err
	}
	pd := s.printData(r.Context(), auth.FromContext(r.Context()).TenantID, inv)
	width := pd.Settings.ThermalWidthMM
	switch httpx.Query(r, "width") {
	case "":
	case "58", "2":
		width = 58
	case "80", "3":
		width = 80
	default:
		return httpx.Validation("width must be 58 or 80 (mm)")
	}
	raw, text := ThermalReceipt(inv, pd, width)
	switch httpx.Query(r, "format") {
	case "", "escpos":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+PDFFileName(inv.Number)[:len(PDFFileName(inv.Number))-4]+`.bin"`)
		w.Header().Set("X-Thermal-Width-MM", strconv.Itoa(width))
		_, err := w.Write(raw)
		return err
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := w.Write([]byte(text))
		return err
	case "json":
		return httpx.OK(w, map[string]any{"width_mm": width, "columns": ThermalColumns(width),
			"escpos_base64": base64.StdEncoding.EncodeToString(raw), "text": text})
	}
	return httpx.Validation("format must be escpos, text or json")
}

func (s *Service) billingSettings(w http.ResponseWriter, r *http.Request) error {
	b, err := s.BillingSettings(r.Context(), auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) setBillingSettings(w http.ResponseWriter, r *http.Request) error {
	var in BillingSettings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := s.SetBillingSettings(r.Context(), auth.FromContext(r.Context()).TenantID, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}
