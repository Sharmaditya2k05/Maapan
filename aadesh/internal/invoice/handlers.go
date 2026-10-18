package invoice

import (
	"context"
	"net/http"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
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
	mux.Handle("POST /v1/sync/invoices", user(s.sync, auth.PermInvoiceCreate))
	mux.Handle("POST /v1/number-blocks", user(s.reserveBlock, auth.PermInvoiceCreate))

	mux.Handle("GET /internal/v1/invoices/{id}", v.Internal(httpx.H(s.get)))
	mux.Handle("GET /internal/v1/invoices", v.Internal(httpx.H(s.list)))

	mux.Handle("POST /events", events.Handler(v, trustIAM, events.Router{
		events.PaymentCaptured: func(ctx context.Context, e events.Envelope) error { return s.ApplyPayment(ctx, e, false) },
		events.PaymentRefunded: func(ctx context.Context, e events.Envelope) error { return s.ApplyPayment(ctx, e, true) },
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
	StoreState    string     `json:"store_state"`
	PlaceOfSupply string     `json:"place_of_supply"`
	Lines         []gst.Line `json:"lines"`
	RoundOff      *bool      `json:"round_off,omitempty"`
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) error {
	var in previewInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	sum, err := gst.Compute(in.Lines, in.StoreState, in.PlaceOfSupply, in.RoundOff == nil || *in.RoundOff)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	return httpx.OK(w, map[string]any{"summary": sum, "amount_in_words": gst.AmountInWords(sum.GrandPaise)})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	f := Filter{
		DocType: httpx.Query(r, "doc_type"), StoreID: httpx.Query(r, "store_id"), PartyID: httpx.Query(r, "party_id"),
		Status: httpx.Query(r, "status"), PaymentStatus: httpx.Query(r, "payment_status"),
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

type syncInput struct {
	Invoices []CreateInput `json:"invoices"`
}

type syncResult struct {
	ClientUUID string       `json:"client_uuid"`
	Status     string       `json:"status"` // created | duplicate | error
	Invoice    *Invoice     `json:"invoice,omitempty"`
	Error      *httpx.Error `json:"error,omitempty"`
}

// sync uploads bills made offline. Each bill is processed independently and
// idempotently (client_uuid), so the POS can resend the whole queue safely.
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
			res.Status, res.Error = "error", httpx.AsError(err)
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
