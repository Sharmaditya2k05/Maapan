package messaging

import (
	"io"
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// Routes registers soochna's HTTP API.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	mux.Handle("POST /internal/v1/messages", v.Internal(httpx.H(s.send)))
	mux.Handle("POST /internal/v1/jobs/retry-failed", v.Internal(httpx.H(s.retry)))

	mux.Handle("GET /v1/messages", v.User(auth.Require(httpx.H(s.list), auth.PermSettingsManage)))
	mux.Handle("GET /v1/templates", v.User(httpx.H(s.templates)))

	// Provider delivery reports (public on the gateway, verified by ?token=).
	mux.Handle("POST /v1/webhooks/{provider}", httpx.H(s.webhook))

	mux.Handle("POST /events", events.Handler(v, trustIAM, s.EventRoutes()))
}

func (s *Service) send(w http.ResponseWriter, r *http.Request) error {
	var in SendInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.Send(r.Context(), auth.FromContext(r.Context()).TenantID, in)
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

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	f := ListFilter{
		Status: httpx.Query(r, "status"), Channel: httpx.Query(r, "channel"),
		Template: httpx.Query(r, "template"), To: httpx.Query(r, "to"), Page: httpx.ParsePage(r),
	}
	items, total, err := s.List(r.Context(), auth.FromContext(r.Context()).TenantID, f)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Message]{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Service) templates(w http.ResponseWriter, _ *http.Request) error {
	return httpx.OK(w, map[string]any{"templates": templates.Names()})
}

func (s *Service) webhook(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return httpx.BadRequest("read body")
	}
	n, err := s.ApplyWebhook(r.Context(), r.PathValue("provider"), r.URL.Query().Get("token"), raw)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]int{"updated": n})
}
