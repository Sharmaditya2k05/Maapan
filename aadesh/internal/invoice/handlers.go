package invoice

import (
	"context"
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Routes registers aadesh's HTTP API on mux.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	mux.Handle("POST /v1/invoices", user(s.create, auth.PermInvoiceCreate))
	mux.Handle("POST /v1/invoices/preview", user(s.preview, auth.PermInvoiceCreate))
	mux.Handle("GET /v1/invoices", user(s.list, auth.PermInvoiceRead))
	mux.Handle("GET /v1/invoices/{id}", user(s.get, auth.PermInvoiceRead))
	mux.Handle("POST /v1/invoices/{id}/cancel", user(s.cancel, auth.PermInvoiceCancel))
	mux.Handle("POST /v1/invoices/{id}/payment-request", user(s.paymentRequest, auth.PermPaymentCollect))
	mux.Handle("POST /v1/sync/invoices", user(s.sync, auth.PermInvoiceCreate))
	mux.Handle("POST /v1/number-blocks", user(s.reserveBlock, auth.PermInvoiceCreate))
	mux.Handle("POST /v1/number-blocks/{id}/release", user(s.releaseBlock, auth.PermInvoiceCreate))
	mux.Handle("GET /v1/settings/numbering", user(s.numbering, auth.PermInvoiceRead))
	mux.Handle("PUT /v1/settings/numbering", user(s.setNumbering, auth.PermSettingsManage))

	mux.Handle("GET /internal/v1/invoices/{id}", v.Internal(httpx.H(s.get)))
	mux.Handle("GET /internal/v1/invoices", v.Internal(httpx.H(s.list)))

	mux.Handle("POST /events", events.Handler(v, trustIAM, events.Router{
		events.PaymentCaptured:   func(ctx context.Context, e events.Envelope) error { return s.ApplyPayment(ctx, e, false) },
		events.PaymentRefunded:   func(ctx context.Context, e events.Envelope) error { return s.ApplyPayment(ctx, e, true) },
		events.EInvoiceGenerated: s.ApplyEInvoice,
		events.EWayGenerated:     s.ApplyEWay,
	}))
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.DocType == DocPurchase || in.DocType == DocPurchaseReturn {
		if p := auth.FromContext(r.Context()); !p.Can(auth.PermPurchaseManage) {
			return httpx.Forbidden("missing permission %s", auth.PermPurchaseManage)
		}
	}
	inv, created, err := s.Create(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	if !created {
		return httpx.OK(w, inv)
	}
	return httpx.Created(w, inv)
}

// previewInput computes totals without saving (live totals on the POS screen).
type previewInput struct {
	StoreState           string     `json:"store_state"`
	PlaceOfSupply        string     `json:"place_of_supply"`
	SupplyCategory       string     `json:"supply_category,omitempty"`
	ReverseCharge        bool       `json:"reverse_charge,omitempty"`
	InvoiceDiscountPaise int64      `json:"invoice_discount_paise,omitempty"`
	InvoiceDiscountBP    int        `json:"invoice_discount_bp,omitempty"`
	TCSBP                int        `json:"tcs_bp,omitempty"`
	InvoiceDate          string     `json:"invoice_date,omitempty"`
	Lines                []gst.Line `json:"lines"`
	RoundOff             *bool      `json:"round_off,omitempty"`
	Tenders              []Tender   `json:"tenders,omitempty"`
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) error {
	var in previewInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	opts := gst.Options{
		StoreState: in.StoreState, PlaceOfSupply: in.PlaceOfSupply, RoundToRupee: in.RoundOff == nil || *in.RoundOff,
		SupplyCategory: in.SupplyCategory, ReverseCharge: in.ReverseCharge, InvoiceDiscountPaise: in.InvoiceDiscountPaise,
		InvoiceDiscountBP: in.InvoiceDiscountBP, TCSBP: in.TCSBP,
	}
	if in.InvoiceDate != "" {
		d, err := s.documentDate(CreateInput{InvoiceDate: in.InvoiceDate}, s.Now())
		if err != nil {
			return err
		}
		opts.On = d
	}
	sum, err := gst.ComputeWith(in.Lines, opts)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	out := map[string]any{"summary": sum, "amount_in_words": gst.AmountInWords(sum.GrandPaise)}
	if len(in.Tenders) > 0 {
		paid, change, credit, err := Settle(sum.GrandPaise, in.Tenders)
		if err != nil {
			return err
		}
		out["paid_paise"], out["change_paise"], out["credit_paise"] = paid, change, credit
	}
	return httpx.OK(w, out)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	f := Filter{
		DocType: httpx.Query(r, "doc_type"), StoreID: httpx.Query(r, "store_id"), PartyID: httpx.Query(r, "party_id"),
		RefInvoiceID: httpx.Query(r, "ref_invoice_id"), RegistrationID: httpx.Query(r, "registration_id"),
		StoreGSTIN: httpx.Query(r, "store_gstin"),
		Status:     httpx.Query(r, "status"), PaymentStatus: httpx.Query(r, "payment_status"),
		From: httpx.Query(r, "from"), To: httpx.Query(r, "to"), Search: httpx.Query(r, "q"), Page: httpx.ParsePage(r),
		IncludeLines: httpx.Query(r, "include") == "lines",
	}
	// Store-scoped roles only ever see their own stores.
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		if f.StoreID == "" {
			f.StoreID = p.StoreID
		}
		if !p.Claims.CanAccessStore(f.StoreID) {
			return httpx.Forbidden("no access to store")
		}
	}
	items, total, err := List(r.Context(), s.DB, p.TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Invoice]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	inv, err := s.Get(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(inv.StoreID) {
		return httpx.NotFound("invoice not found")
	}
	return httpx.OK(w, inv)
}

type cancelInput struct {
	Reason string `json:"reason"`
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	var in cancelInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	inv, err := s.Cancel(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in.Reason)
	if err != nil {
		return err
	}
	return httpx.OK(w, inv)
}

type paymentRequestInput struct {
	Kind string `json:"kind,omitempty"` // upi_qr (default) | payment_link
}

func (s *Service) paymentRequest(w http.ResponseWriter, r *http.Request) error {
	var in paymentRequestInput
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	req, err := s.PaymentRequest(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in.Kind)
	if err != nil {
		return err
	}
	return httpx.Created(w, req)
}

type syncInput struct {
	Invoices []CreateInput `json:"invoices"`
}

type syncResult struct {
	ClientUUID string       `json:"client_uuid"`
	Status     string       `json:"status"` // created | duplicate | error | retry
	Invoice    *Invoice     `json:"invoice,omitempty"`
	Error      *httpx.Error `json:"error,omitempty"`
}

// sync uploads bills made offline. Each bill is processed independently and
// idempotently (client_uuid), so the POS can resend the whole queue safely.
// "retry" means a downstream service was unavailable: keep the bill queued.
func (s *Service) sync(w http.ResponseWriter, r *http.Request) error {
	var in syncInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Invoices) == 0 || len(in.Invoices) > 200 {
		return httpx.Validation("send 1 to 200 invoices per sync call")
	}
	p := auth.FromContext(r.Context())
	results := make([]syncResult, 0, len(in.Invoices))
	for _, ci := range in.Invoices {
		res := syncResult{ClientUUID: ci.ClientUUID}
		if ci.ClientUUID == "" {
			res.Status, res.Error = "error", httpx.Validation("client_uuid is required for sync")
			results = append(results, res)
			continue
		}
		if err := ci.Validate(); err != nil {
			res.Status, res.Error = "error", httpx.AsError(err)
			results = append(results, res)
			continue
		}
		inv, created, err := s.Create(r.Context(), p, ci)
		switch {
		case err != nil:
			res.Error = httpx.AsError(err)
			res.Status = "error"
			if res.Error.Status >= 500 {
				res.Status = "retry"
			}
		case created:
			res.Status, res.Invoice = "created", inv
		default:
			res.Status, res.Invoice = "duplicate", inv
		}
		results = append(results, res)
	}
	return httpx.OK(w, map[string]any{"results": results})
}

type blockInput struct {
	StoreID  string `json:"store_id,omitempty"`
	DocType  string `json:"doc_type"`
	DeviceID string `json:"device_id"`
	Size     int64  `json:"size"`
}

func (s *Service) reserveBlock(w http.ResponseWriter, r *http.Request) error {
	var in blockInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := s.ReserveBlock(r.Context(), auth.FromContext(r.Context()), in.StoreID, in.DocType, in.DeviceID, in.Size)
	if err != nil {
		return err
	}
	return httpx.Created(w, b)
}

type releaseInput struct {
	LastUsed int64 `json:"last_used"`
}

func (s *Service) releaseBlock(w http.ResponseWriter, r *http.Request) error {
	var in releaseInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := s.ReleaseBlock(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in.LastUsed)
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) numbering(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	rows, err := s.Numbering(r.Context(), p.TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"default_prefixes": defaultPrefix, "stores": rows})
}

func (s *Service) setNumbering(w http.ResponseWriter, r *http.Request) error {
	var in NumberingSetting
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.SetNumbering(r.Context(), auth.FromContext(r.Context()).TenantID, in); err != nil {
		return err
	}
	return httpx.OK(w, in)
}
