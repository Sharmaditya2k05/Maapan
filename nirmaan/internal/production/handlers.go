package production

import (
	"net/http"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/nirmaan/internal/bom"
)

// Routes registers the production-run API.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier) {
	read := func(h httpx.HandlerFunc) http.Handler {
		return v.User(bom.AnyOf(httpx.H(h), auth.PermBOMManage, auth.PermReportRead))
	}
	write := func(h httpx.HandlerFunc) http.Handler {
		return v.User(auth.Require(httpx.H(h), auth.PermBOMManage))
	}
	mux.Handle("GET /v1/production-runs", read(s.list))
	mux.Handle("POST /v1/production-runs", write(s.create))
	mux.Handle("GET /v1/production-runs/{id}", read(s.get))
	mux.Handle("POST /v1/production-runs/{id}/start", write(s.start))
	mux.Handle("POST /v1/production-runs/{id}/complete", write(s.complete))
	mux.Handle("POST /v1/production-runs/{id}/cancel", write(s.cancel))
}

func principal(r *http.Request) *auth.Principal { return auth.FromContext(r.Context()) }

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	runs, err := s.List(r.Context(), principal(r), httpx.Query(r, "store_id"), httpx.Query(r, "status"), httpx.ParsePage(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": runs})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	run, err := s.Create(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, run)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	run, err := s.Get(r.Context(), principal(r), r.PathValue("id"), httpx.Query(r, "availability") != "false")
	if err != nil {
		return err
	}
	return httpx.OK(w, run)
}

type startInput struct {
	Force bool `json:"force"`
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) error {
	var in startInput
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	run, err := s.Start(r.Context(), principal(r), r.PathValue("id"), in.Force)
	if err != nil {
		return err
	}
	return httpx.OK(w, run)
}

func (s *Service) complete(w http.ResponseWriter, r *http.Request) error {
	var in CompleteInput
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	run, err := s.Complete(r.Context(), principal(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, run)
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	run, err := s.Cancel(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, run)
}
