package job

import (
	"encoding/csv"
	"net/http"
	"strings"

	"github.com/taraju/aayaat/internal/mapping"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/httpx"
)

// Routes registers aayaat's HTTP API. aayaat publishes but does not subscribe,
// so it has no /events endpoint.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier) {
	user := func(h httpx.HandlerFunc) http.Handler {
		return v.User(auth.Require(httpx.H(h), auth.PermImportRun))
	}
	mux.Handle("POST /v1/imports", user(s.create))
	mux.Handle("GET /v1/imports", user(s.list))
	mux.Handle("GET /v1/imports/{id}", user(s.get))
	// templates/{kind} and {id}/preview|errors|rows share one pattern: Go's mux
	// rejects the two overlapping wildcard patterns, so dispatch here.
	mux.Handle("GET /v1/imports/{a}/{b}", user(s.getSub))
	mux.Handle("PUT /v1/imports/{id}/mapping", user(s.setMapping))
	mux.Handle("POST /v1/imports/{id}/validate", user(s.validate))
	mux.Handle("POST /v1/imports/{id}/commit", user(s.commit))
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxFileBytes+(1<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		return httpx.BadRequest("send multipart/form-data with a 'file' (max 50 MB) and a 'kind'")
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return httpx.Validation("file is required")
	}
	defer f.Close()
	params := Params{AsOf: r.FormValue("as_of"), StoreID: r.FormValue("store_id"), NotifyPhone: r.FormValue("notify_phone")}
	j, err := s.Create(r.Context(), auth.FromContext(r.Context()), r.FormValue("kind"), hdr.Filename,
		hdr.Header.Get("Content-Type"), f, params)
	if err != nil {
		return err
	}
	return httpx.Created(w, j)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	pg := httpx.ParsePage(r)
	items, total, err := s.List(r.Context(), auth.FromContext(r.Context()).TenantID, httpx.Query(r, "kind"), pg)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Job]{Items: items, Total: total, Limit: pg.Limit, Offset: pg.Offset})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	j, err := s.Get(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, j)
}

func (s *Service) getSub(w http.ResponseWriter, r *http.Request) error {
	a, b := r.PathValue("a"), r.PathValue("b")
	if a == "templates" {
		return template(w, b)
	}
	tenant := auth.FromContext(r.Context()).TenantID
	switch b {
	case "preview":
		p, err := s.Preview(r.Context(), tenant, a, httpx.Query(r, "sheet"))
		if err != nil {
			return err
		}
		return httpx.OK(w, p)
	case "errors":
		if _, err := s.Get(r.Context(), tenant, a); err != nil {
			return err
		}
		if httpx.Query(r, "format") == "csv" {
			rows, _, err := s.Rows(r.Context(), tenant, a, "errors", httpx.Page{})
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="import-errors-`+a+`.csv"`)
			return WriteErrorsCSV(w, rows)
		}
		return s.writeRows(w, r, tenant, a, "errors")
	case "rows":
		return s.writeRows(w, r, tenant, a, httpx.Query(r, "status"))
	}
	return httpx.NotFound("unknown import resource %q", b)
}

func (s *Service) writeRows(w http.ResponseWriter, r *http.Request, tenant, id, status string) error {
	if _, err := s.Get(r.Context(), tenant, id); err != nil {
		return err
	}
	pg := httpx.ParsePage(r)
	rows, total, err := s.Rows(r.Context(), tenant, id, status, pg)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[RowResult]{Items: rows, Total: total, Limit: pg.Limit, Offset: pg.Offset})
}

func (s *Service) setMapping(w http.ResponseWriter, r *http.Request) error {
	var in MappingInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	j, err := s.SetMapping(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, j)
}

func (s *Service) validate(w http.ResponseWriter, r *http.Request) error {
	sum, err := s.Validate(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, sum)
}

func (s *Service) commit(w http.ResponseWriter, r *http.Request) error {
	j, err := s.Commit(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, j)
}

// template writes a CSV with the expected headers and one example row.
func template(w http.ResponseWriter, kind string) error {
	fields, ok := mapping.Fields[strings.TrimSuffix(kind, ".csv")]
	if !ok {
		return httpx.NotFound("no template for %q", kind)
	}
	head := make([]string, len(fields))
	example := make([]string, len(fields))
	for i, f := range fields {
		head[i], example[i] = f.Label, f.Example
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="taraju-`+strings.TrimSuffix(kind, ".csv")+`-template.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write(head)
	_ = cw.Write(example)
	cw.Flush()
	return cw.Error()
}
