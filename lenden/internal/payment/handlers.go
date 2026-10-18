package payment

import (
	"net/http"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/httpx"
)

// Routes registers lenden's HTTP API on mux.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, hooks []*Webhooks) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	mux.Handle("POST /v1/payment-requests", user(s.createRequest, auth.PermPaymentCollect))
	mux.Handle("GET /v1/payment-requests/{id}", user(s.getRequest, auth.PermPaymentCollect))
	mux.Handle("GET /v1/payments", user(s.listPayments, auth.PermPaymentCollect))
	mux.Handle("GET /v1/payments/{id}", user(s.getPayment, auth.PermPaymentCollect))
	mux.Handle("POST /v1/payments/manual", user(s.manual, auth.PermPaymentCollect))
	mux.Handle("POST /v1/payments/{id}/refund", user(s.refund, auth.PermInvoiceCancel))
	for _, wh := range hooks {
		mux.Handle("POST /v1/webhooks/"+wh.Source.Gateway(), wh)
	}
	mux.Handle("GET /v1/gateways", user(s.listGateways, auth.PermPaymentCollect))
	if _, ok := s.Gateways.Mock(); ok {
		mux.Handle("POST /v1/dev/simulate-payment/{id}", user(s.simulate, auth.PermPaymentCollect))
	}

	mux.Handle("POST /internal/v1/payment-requests", v.Internal(httpx.H(s.createRequest)))
	mux.Handle("GET /internal/v1/payment-requests/{id}", v.Internal(httpx.H(s.getRequest)))
	mux.Handle("POST /internal/v1/jobs/reconcile", v.Internal(httpx.H(s.reconcile)))
}

// listGateways tells the POS which gateways it may pick and which is the default.
func (s *Service) listGateways(w http.ResponseWriter, _ *http.Request) error {
	return httpx.OK(w, map[string]any{"default": s.Gateways.Default().Name(), "available": s.Gateways.Names()})
}

func (s *Service) createRequest(w http.ResponseWriter, r *http.Request) error {
	var in RequestInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	req, err := s.CreateRequest(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, req)
}

func (s *Service) getRequest(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	req, err := s.GetRequest(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if p.Claims != nil && req.StoreID != nil && !p.Claims.CanAccessStore(*req.StoreID) {
		return httpx.NotFound("payment request not found")
	}
	return httpx.OK(w, req)
}

func (s *Service) listPayments(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	f := Filter{
		StoreID: httpx.Query(r, "store_id"), InvoiceID: httpx.Query(r, "invoice_id"), PartyID: httpx.Query(r, "party_id"),
		Method: httpx.Query(r, "method"), Gateway: httpx.Query(r, "gateway"), Status: httpx.Query(r, "status"),
		From: httpx.Query(r, "from"), To: httpx.Query(r, "to"), Page: httpx.ParsePage(r),
	}
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		if f.StoreID == "" {
			f.StoreID = p.StoreID
		}
		if !p.Claims.CanAccessStore(f.StoreID) {
			return httpx.Forbidden("no access to store")
		}
	}
	items, total, err := s.ListPayments(r.Context(), p.TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Payment]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) getPayment(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	pay, err := s.GetPayment(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if p.Claims != nil && pay.StoreID != nil && !p.Claims.CanAccessStore(*pay.StoreID) {
		return httpx.NotFound("payment not found")
	}
	return httpx.OK(w, pay)
}

func (s *Service) manual(w http.ResponseWriter, r *http.Request) error {
	var in ManualInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	pay, err := s.Manual(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, pay)
}

func (s *Service) refund(w http.ResponseWriter, r *http.Request) error {
	var in RefundInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	pay, err := s.Refund(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, pay)
}

func (s *Service) reconcile(w http.ResponseWriter, r *http.Request) error {
	rep, err := s.Reconcile(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, rep)
}

type simulateInput struct {
	Method string `json:"method,omitempty"`
}

func (s *Service) simulate(w http.ResponseWriter, r *http.Request) error {
	var in simulateInput
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	req, err := s.Simulate(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"), in.Method)
	if err != nil {
		return err
	}
	return httpx.OK(w, req)
}
