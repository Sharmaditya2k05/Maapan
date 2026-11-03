package gateway

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/maapan/mool/httpx"
)

func TestSignedInvoicePDFIsPublic(t *testing.T) {
	cases := map[string]bool{
		"GET /api/aadesh/public/v1/invoices/7f1c/pdf":        true,
		"GET /api/aadesh/public/v1/invoices/7f1c/pdf/":       true,
		"POST /api/aadesh/public/v1/invoices/7f1c/pdf":       false,
		"GET /api/aadesh/public/v1/invoices//pdf":            false,
		"GET /api/aadesh/public/v1/invoices/a/b/pdf":         false,
		"GET /api/aadesh/public/v1/invoices/../internal/pdf": false,
		"GET /api/aadesh/v1/invoices/7f1c/pdf":               false,
		"GET /api/kar/public/v1/invoices/7f1c/pdf":           false,
	}
	for in, want := range cases {
		var method, path string
		for i := range in {
			if in[i] == ' ' {
				method, path = in[:i], in[i+1:]
				break
			}
		}
		if got := IsPublic(method, path); got != want {
			t.Errorf("%s: public=%v want %v", in, got, want)
		}
	}
}

// The client sees one X-Request-ID, not dwaar's and the service's.
func TestSingleRequestIDHeader(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
		_, _ = w.Write([]byte("{}"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	g := New(Options{Upstreams: Upstreams{"aadesh": u}})
	h := httpx.RequestID(slog.Default())(g)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/aadesh/public/v1/invoices/x/pdf", nil))
	if got := rec.Header().Values("X-Request-ID"); len(got) != 1 {
		t.Fatalf("X-Request-ID values: %v", got)
	}
}
