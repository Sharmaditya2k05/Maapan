package messaging

import (
	"io"
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Routes registers soochna's HTTP API. jobs guards the Cloud Scheduler
// endpoints (service secret or the scheduler's OIDC token); nil = v.Internal.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool, jobs Middleware) {
	if jobs == nil {
		jobs = v.Internal
	}
	internal := func(fn httpx.HandlerFunc) http.Handler { return v.Internal(httpx.H(fn)) }
	user := func(fn httpx.HandlerFunc, perm string) http.Handler { return v.User(auth.Require(httpx.H(fn), perm)) }

	mux.Handle("POST /internal/v1/messages", internal(s.send))
	mux.Handle("POST /internal/v1/jobs/retry-failed", jobs(httpx.H(s.retry)))
	mux.Handle("POST /internal/v1/jobs/payment-reminders", jobs(httpx.H(s.reminders)))
	mux.Handle("GET /internal/v1/dead-letters", internal(s.listDeadInternal))
	mux.Handle("POST /internal/v1/dead-letters/replay", internal(s.replayInternal))
	mux.Handle("POST /internal/v1/consents/bulk", internal(s.bulkConsent))

	mux.Handle("GET /v1/messages", user(s.list, auth.PermSettingsManage))
	mux.Handle("GET /v1/templates", v.User(httpx.H(s.templates)))
	mux.Handle("GET /v1/dead-letters", user(s.listDeadTenant, auth.PermSettingsManage))
	mux.Handle("POST /v1/dead-letters/replay", user(s.replayTenant, auth.PermSettingsManage))
	mux.Handle("GET /v1/consents", user(s.listConsents, auth.PermPartyManage))
	mux.Handle("POST /v1/consents", user(s.setConsent, auth.PermPartyManage))
	mux.Handle("GET /v1/settings/messaging", user(s.getSenders, auth.PermSettingsManage))
	mux.Handle("PUT /v1/settings/messaging", user(s.putSenders, auth.PermSettingsManage))

	// Provider delivery reports and inbound STOP/START (public on the gateway, verified by ?token=).
	mux.Handle("POST /v1/webhooks/{provider}", httpx.H(s.webhook))

	mux.Handle("POST /events", events.Handler(v, trustIAM, s.EventRoutes()))
}

func tenantOf(r *http.Request) string {
	if p := auth.FromContext(r.Context()); p != nil {
		return p.TenantID
	}
	return ""
}

func (s *Service) send(w http.ResponseWriter, r *http.Request) error {
	var in SendInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.Send(r.Context(), tenantOf(r), in)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, res)
}

func (s *Service) retry(w http.ResponseWriter, r *http.Request) error {
	res, err := s.RetryFailed(r.Context(), 200)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) reminders(w http.ResponseWriter, r *http.Request) error {
	var in RemindersInput
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	res, err := s.PaymentReminders(r.Context(), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func deadFilter(r *http.Request) DeadFilter {
	return DeadFilter{
		Template: httpx.Query(r, "template"), Channel: httpx.Query(r, "channel"), Reason: httpx.Query(r, "reason"),
		All: httpx.Query(r, "all") == "true", Page: httpx.ParsePage(r),
	}
}

func (s *Service) listDead(w http.ResponseWriter, r *http.Request, f DeadFilter) error {
	items, total, err := s.ListDead(r.Context(), f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[DeadLetter]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) listDeadInternal(w http.ResponseWriter, r *http.Request) error {
	f := deadFilter(r)
	f.TenantID = httpx.Query(r, "tenant_id")
	return s.listDead(w, r, f)
}

func (s *Service) listDeadTenant(w http.ResponseWriter, r *http.Request) error {
	f := deadFilter(r)
	f.TenantID = tenantOf(r)
	return s.listDead(w, r, f)
}

func (s *Service) replayInternal(w http.ResponseWriter, r *http.Request) error {
	var in ReplayInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	by := r.Header.Get(auth.HeaderUserID)
	if by == "" {
		by = "ops"
	}
	res, err := s.ReplayDead(r.Context(), in, by)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) replayTenant(w http.ResponseWriter, r *http.Request) error {
	var in ReplayInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.TenantID = tenantOf(r) // a store can only replay its own messages
	res, err := s.ReplayDead(r.Context(), in, auth.FromContext(r.Context()).UserID())
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) bulkConsent(w http.ResponseWriter, r *http.Request) error {
	var in BulkConsentInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.BulkConsent(r.Context(), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) listConsents(w http.ResponseWriter, r *http.Request) error {
	items, err := s.ListConsents(r.Context(), tenantOf(r), httpx.Query(r, "address"), httpx.ParsePage(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) setConsent(w http.ResponseWriter, r *http.Request) error {
	var in ConsentInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Source = "admin"
	if err := s.SetConsent(r.Context(), tenantOf(r), in); err != nil {
		return err
	}
	return httpx.OK(w, map[string]string{"address": in.Address, "channel": in.Channel, "status": in.Status})
}

func (s *Service) getSenders(w http.ResponseWriter, r *http.Request) error {
	out, err := s.GetSenders(r.Context(), tenantOf(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, out)
}

func (s *Service) putSenders(w http.ResponseWriter, r *http.Request) error {
	var in SenderSettings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	out, err := s.PutSenders(r.Context(), tenantOf(r), auth.FromContext(r.Context()).UserID(), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, out)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	f := ListFilter{
		Status: httpx.Query(r, "status"), Channel: httpx.Query(r, "channel"),
		Template: httpx.Query(r, "template"), To: httpx.Query(r, "to"), Page: httpx.ParsePage(r),
	}
	items, total, err := s.List(r.Context(), tenantOf(r), f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Message]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) templates(w http.ResponseWriter, _ *http.Request) error {
	type info struct {
		Name     string   `json:"name"`
		Category string   `json:"category"`
		Params   []string `json:"params"`
		Channels []string `json:"channels"`
	}
	out := []info{}
	for _, n := range templates.Names() {
		i := info{Name: n, Category: string(templates.CategoryOf(n)), Params: templates.ParamOrder(n)}
		for _, ch := range []string{templates.SMS, templates.WhatsApp, templates.Email} {
			if templates.HasChannel(n, ch) {
				i.Channels = append(i.Channels, ch)
			}
		}
		out = append(out, i)
	}
	return httpx.OK(w, map[string]any{"templates": templates.Names(), "details": out})
}

func (s *Service) webhook(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return httpx.BadRequest("read body")
	}
	res, err := s.ApplyWebhook(r.Context(), r.PathValue("provider"), r.URL.Query().Get("token"), raw, r.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}
