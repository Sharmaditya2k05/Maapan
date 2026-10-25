package bom

import (
	"net/http"
	"strconv"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// AnyOf runs h when the principal holds at least one of perms.
func AnyOf(h http.Handler, perms ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		if p == nil {
			httpx.WriteError(w, r, httpx.Unauthorized("not authenticated"))
			return
		}
		for _, perm := range perms {
			if p.Can(perm) {
				h.ServeHTTP(w, r)
				return
			}
		}
		httpx.WriteError(w, r, httpx.Forbidden("missing permission"))
	})
}

// Routes registers the BOM API.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier) {
	read := func(h httpx.HandlerFunc) http.Handler {
		return v.User(AnyOf(httpx.H(h), auth.PermBOMManage, auth.PermReportRead))
	}
	write := func(h httpx.HandlerFunc) http.Handler {
		return v.User(auth.Require(httpx.H(h), auth.PermBOMManage))
	}
	mux.Handle("GET /v1/boms", read(s.list))
	mux.Handle("POST /v1/boms", write(s.create))
	mux.Handle("GET /v1/boms/{id}", read(s.get))
	mux.Handle("PUT /v1/boms/{id}", write(s.update))
	mux.Handle("POST /v1/boms/{id}/activate", write(s.activate))
	mux.Handle("POST /v1/boms/{id}/archive", write(s.archive))
	mux.Handle("POST /v1/boms/{id}/new-version", write(s.newVersion))
	mux.Handle("GET /v1/boms/{id}/explode", read(s.explode))
	mux.Handle("GET /v1/boms/{id}/cost", read(s.cost))
}

func tenant(r *http.Request) string { return auth.FromContext(r.Context()).TenantID }

func qtyParam(r *http.Request) float64 {
	q, _ := strconv.ParseFloat(r.URL.Query().Get("qty"), 64)
	return q
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	page := httpx.ParsePage(r)
	items, err := s.List(r.Context(), tenant(r), httpx.Query(r, "item_id"), httpx.Query(r, "status"), httpx.Query(r, "q"), page)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items, "limit": page.Limit, "offset": page.Offset})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	var in Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := s.Create(r.Context(), tenant(r), auth.FromContext(r.Context()).UserID(), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, b)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	b, err := Load(r.Context(), s.DB, tenant(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) error {
	var in Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := s.Update(r.Context(), tenant(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) activate(w http.ResponseWriter, r *http.Request) error {
	b, err := s.Activate(r.Context(), tenant(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) archive(w http.ResponseWriter, r *http.Request) error {
	b, err := s.Archive(r.Context(), tenant(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, b)
}

func (s *Service) newVersion(w http.ResponseWriter, r *http.Request) error {
	b, err := s.NewVersion(r.Context(), tenant(r), auth.FromContext(r.Context()).UserID(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.Created(w, b)
}

func (s *Service) explode(w http.ResponseWriter, r *http.Request) error {
	tree, flat, err := s.ExplodeByID(r.Context(), tenant(r), r.PathValue("id"), qtyParam(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"tree": tree, "requirements": flat})
}

func (s *Service) cost(w http.ResponseWriter, r *http.Request) error {
	res, err := s.CostByID(r.Context(), tenant(r), r.PathValue("id"), qtyParam(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}
