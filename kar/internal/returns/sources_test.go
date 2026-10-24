package returns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/taraju/mool/svc"
)

func TestAadeshPaging(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, r.URL.RawQuery)
		if q.Get("store_gstin") != ourGSTIN || q.Get("include") != "lines" || q.Get("from") != "2026-10-01" {
			http.Error(w, "bad query", 400)
			return
		}
		off, _ := strconv.Atoi(q.Get("offset"))
		// 5 sales; the second page repeats the last item of the first (a bill
		// created mid-read shifts offsets).
		all := []Invoice{{ID: "a"}, {ID: "b"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
		if q.Get("doc_type") != "sale" {
			all = nil
		}
		end := min(off+2, len(all))
		page := []Invoice{}
		if off < len(all) {
			page = all[off:end]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": page, "total": len(all)})
	}))
	defer srv.Close()
	src := AadeshInvoices{C: svc.New(srv.URL, "s", false), PageSize: 2}
	var ids []string
	err := src.EachInvoice(context.Background(), InvoiceQuery{GSTIN: ourGSTIN, From: "2026-10-01", To: "2026-10-31",
		DocTypes: []string{"sale", "sale_return"}, IncludeLines: true}, func(inv Invoice) error { ids = append(ids, inv.ID); return nil })
	if err != nil || len(ids) != 4 || ids[3] != "d" {
		t.Fatalf("ids %v err %v queries %v", ids, err, queries)
	}
	if len(queries) != 4 { // 3 pages of sales + 1 empty page of credit notes
		t.Errorf("queries %v", queries)
	}
}

func TestRegistrationsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/registrations":
			http.NotFound(w, r) // pehchaan without Phase 3
		case "/internal/v1/tenants/t1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "t1", "business_name": "Acme", "gstin": ourGSTIN, "state_code": "27"})
		default:
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	got, err := PehchaanRegistrations{C: svc.New(srv.URL, "s", false)}.Registrations(context.Background(), "t1")
	if err != nil || len(got) != 1 || got[0].GSTIN != ourGSTIN || !got[0].IsPrimary {
		t.Fatalf("fallback %+v %v", got, err)
	}
}
