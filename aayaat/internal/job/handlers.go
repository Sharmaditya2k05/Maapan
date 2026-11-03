package job

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
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
	// templates/{kind} and {id}/preview|errors|rows|batches share one pattern: Go's
	// mux rejects the two overlapping wildcard patterns, so dispatch here.
	mux.Handle("GET /v1/imports/{a}/{b}", user(s.getSub))
	mux.Handle("PUT /v1/imports/{id}/mapping", user(s.setMapping))
	mux.Handle("POST /v1/imports/{id}/validate", user(s.validate))
	mux.Handle("POST /v1/imports/{id}/dry-run", user(s.dryRun))
	mux.Handle("POST /v1/imports/{id}/commit", user(s.commit))
	mux.Handle("POST /v1/imports/{id}/rollback", user(s.rollback))

	mux.Handle("GET /v1/mapping-templates", user(s.listTemplates))
	mux.Handle("POST /v1/mapping-templates", user(s.saveTemplate))
	mux.Handle("GET /v1/mapping-templates/{id}", user(s.getTemplate))
	mux.Handle("DELETE /v1/mapping-templates/{id}", user(s.deleteTemplate))
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
	var params Params
	if raw := r.FormValue("params"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return httpx.Validation("params must be a JSON object: %s", err.Error())
		}
	}
	for _, kv := range []struct {
		field string
		dst   *string
	}{{"as_of", &params.AsOf}, {"store_id", &params.StoreID}, {"notify_phone", &params.NotifyPhone},
		{"source", &params.Source}, {"doc_type", &params.DocType}, {"side", &params.Side}, {"bills_mode", &params.BillsMode}} {
		if v := r.FormValue(kv.field); v != "" {
			*kv.dst = v
		}
	}
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
	case "errors", "warnings":
		if _, err := s.Get(r.Context(), tenant, a); err != nil {
			return err
		}
		if httpx.Query(r, "format") == "csv" {
			rows, _, err := s.Rows(r.Context(), tenant, a, b, httpx.Page{})
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="import-`+b+`-`+a+`.csv"`)
			return WriteErrorsCSV(w, rows)
		}
		return s.writeRows(w, r, tenant, a, b)
	case "rows":
		return s.writeRows(w, r, tenant, a, httpx.Query(r, "status"))
	case "batches":
		if _, err := s.Get(r.Context(), tenant, a); err != nil {
			return err
		}
		bs, err := s.Batches(r.Context(), tenant, a)
		if err != nil {
			return err
		}
		return httpx.OK(w, map[string]any{"items": bs})
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
	sum, err := s.Validate(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, sum)
}

func (s *Service) dryRun(w http.ResponseWriter, r *http.Request) error {
	d, err := s.DryRunCommit(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

func (s *Service) commit(w http.ResponseWriter, r *http.Request) error {
	if q := httpx.Query(r, "dry_run"); q == "true" || q == "1" {
		return s.dryRun(w, r)
	}
	j, err := s.Commit(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, j)
}

func (s *Service) rollback(w http.ResponseWriter, r *http.Request) error {
	batch := 0
	if q := httpx.Query(r, "batch"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			return httpx.Validation("batch must be a batch number (see GET /v1/imports/{id}/batches)")
		}
		batch = n
	}
	res, err := s.Rollback(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"), batch)
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) listTemplates(w http.ResponseWriter, r *http.Request) error {
	ts, err := s.ListTemplates(r.Context(), auth.FromContext(r.Context()).TenantID, httpx.Query(r, "kind"))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": ts})
}

func (s *Service) saveTemplate(w http.ResponseWriter, r *http.Request) error {
	var in TemplateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := s.SaveTemplate(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, t)
}

func (s *Service) getTemplate(w http.ResponseWriter, r *http.Request) error {
	t, err := s.GetTemplate(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, t)
}

func (s *Service) deleteTemplate(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteTemplate(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Batch is one journalled bulk call of a job.
type Batch struct {
	BatchNo    int        `json:"batch_no"`
	Phase      string     `json:"phase"`
	Path       string     `json:"path"`
	StoreID    *string    `json:"store_id,omitempty"`
	FirstRow   int        `json:"first_row"`
	LastRow    int        `json:"last_row"`
	Records    int        `json:"records"`
	Status     string     `json:"status"`
	Created    int        `json:"created"`
	Updated    int        `json:"updated"`
	Errors     int        `json:"errors"`
	Error      *string    `json:"error,omitempty"`
	SentAt     time.Time  `json:"sent_at"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// Batches lists the bulk calls a job made, oldest first.
func (s *Service) Batches(ctx context.Context, tenantID, id string) ([]Batch, error) {
	rows, err := s.DB.Query(ctx, `SELECT batch_no, phase, path, store_id, first_row, last_row, records, status, created, updated, errors,
		error, sent_at, recorded_at FROM import_batches WHERE tenant_id=$1 AND job_id=$2 ORDER BY batch_no`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.BatchNo, &b.Phase, &b.Path, &b.StoreID, &b.FirstRow, &b.LastRow, &b.Records, &b.Status, &b.Created,
			&b.Updated, &b.Errors, &b.Error, &b.SentAt, &b.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
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
	w.Header().Set("Content-Disposition", `attachment; filename="maapan-`+strings.TrimSuffix(kind, ".csv")+`-template.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write(head)
	_ = cw.Write(example)
	cw.Flush()
	return cw.Error()
}
