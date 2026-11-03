package jobwork

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/bom"
)

// Routes registers the job-work API.
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier) {
	read := func(h httpx.HandlerFunc) http.Handler {
		return v.User(bom.AnyOf(httpx.H(h), auth.PermBOMManage, auth.PermStockManage, auth.PermReportRead, auth.PermGSTFile))
	}
	write := func(h httpx.HandlerFunc) http.Handler {
		return v.User(bom.AnyOf(httpx.H(h), auth.PermBOMManage, auth.PermStockManage))
	}
	mux.Handle("GET /v1/job-workers", read(s.listWorkers))
	mux.Handle("POST /v1/job-workers", write(s.createWorker))
	mux.Handle("GET /v1/job-workers/{id}", read(s.getWorker))
	mux.Handle("PUT /v1/job-workers/{id}", write(s.updateWorker))
	mux.Handle("GET /v1/job-work/challans", read(s.listChallans))
	mux.Handle("POST /v1/job-work/challans", write(s.createChallan))
	mux.Handle("GET /v1/job-work/challans/{id}", read(s.getChallan))
	mux.Handle("POST /v1/job-work/challans/{id}/cancel", write(s.cancelChallan))
	mux.Handle("POST /v1/job-work/challans/{id}/receipts", write(s.receive))
	mux.Handle("GET /v1/job-work/pending", read(s.pending))
	mux.Handle("GET /v1/job-work/itc04", read(s.itc04))
}

func principal(r *http.Request) *auth.Principal { return auth.FromContext(r.Context()) }

func (s *Service) listWorkers(w http.ResponseWriter, r *http.Request) error {
	items, err := s.ListWorkers(r.Context(), principal(r).TenantID, httpx.Query(r, "q"), httpx.ParsePage(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) createWorker(w http.ResponseWriter, r *http.Request) error {
	var in WorkerInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	jw, err := s.CreateWorker(r.Context(), principal(r).TenantID, in)
	if err != nil {
		return err
	}
	return httpx.Created(w, jw)
}

func (s *Service) getWorker(w http.ResponseWriter, r *http.Request) error {
	jw, err := s.GetWorker(r.Context(), principal(r).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, jw)
}

func (s *Service) updateWorker(w http.ResponseWriter, r *http.Request) error {
	var in WorkerInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	jw, err := s.UpdateWorker(r.Context(), principal(r).TenantID, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, jw)
}

func (s *Service) listChallans(w http.ResponseWriter, r *http.Request) error {
	items, err := s.ListChallans(r.Context(), principal(r), httpx.Query(r, "job_worker_id"), httpx.Query(r, "status"),
		httpx.Query(r, "from"), httpx.Query(r, "to"), httpx.ParsePage(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) createChallan(w http.ResponseWriter, r *http.Request) error {
	var in ChallanInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	c, err := s.CreateChallan(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, c)
}

func (s *Service) getChallan(w http.ResponseWriter, r *http.Request) error {
	c, err := s.GetChallan(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, c)
}

func (s *Service) cancelChallan(w http.ResponseWriter, r *http.Request) error {
	c, err := s.CancelChallan(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, c)
}

func (s *Service) receive(w http.ResponseWriter, r *http.Request) error {
	var in ReceiptInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	c, err := s.Receive(r.Context(), principal(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, c)
}

func (s *Service) pending(w http.ResponseWriter, r *http.Request) error {
	lines, err := s.Pending(r.Context(), principal(r), httpx.Query(r, "job_worker_id"), httpx.Query(r, "as_of"),
		httpx.Query(r, "all") == "true")
	if err != nil {
		return err
	}
	var value int64
	overdue := 0
	for _, l := range lines {
		value += l.PendingValue
		if l.Status == DueOverdue {
			overdue++
		}
	}
	return httpx.OK(w, map[string]any{"items": lines, "pending_value_paise": value, "overdue_lines": overdue})
}

// itc04: ?period=2026-27-H1 (or from/to) &table=4|5A &format=csv|json (csv by default).
func (s *Service) itc04(w http.ResponseWriter, r *http.Request) error {
	per, err := ParsePeriod(httpx.Query(r, "period"), httpx.Query(r, "from"), httpx.Query(r, "to"))
	if err != nil {
		return err
	}
	t4, t5, err := s.ITC04(r.Context(), principal(r).TenantID, per)
	if err != nil {
		return err
	}
	if httpx.Query(r, "format") == "json" {
		return httpx.OK(w, map[string]any{"period": per, "table_4": t4, "table_5a": t5})
	}
	table := strings.ToUpper(httpx.Query(r, "table"))
	var buf bytes.Buffer
	switch table {
	case "", "4":
		table = "4"
		err = WriteTable4CSV(&buf, t4)
	case "5A":
		err = WriteTable5ACSV(&buf, t5)
	default:
		return httpx.Validation("table must be 4 or 5A")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ITC04_`+strings.ReplaceAll(per.From+"_"+per.To, "-", "")+"_table"+table+`.csv"`)
	_, err = w.Write(buf.Bytes())
	return err
}
