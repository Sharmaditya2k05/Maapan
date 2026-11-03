package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maapan/mool/svc"
)

func TestAadeshPDFLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/invoices/inv1/pdf-link" || r.URL.Query().Get("ttl") != "3600" {
			t.Errorf("unexpected call %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("X-Tenant-ID") != "t1" || r.Header.Get("X-Service-Secret") != "s" {
			t.Errorf("missing tenant or secret headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://x/api/aadesh/public/v1/invoices/inv1/pdf?exp=1&sig=a", "file_name": "INV-1.pdf"})
	}))
	defer srv.Close()
	a := &AadeshPDF{C: svc.New(srv.URL, "s", false), TTLSeconds: 3600}
	u, name, err := a.Link(context.Background(), "t1", "inv1")
	if err != nil || name != "INV-1.pdf" || u == "" {
		t.Fatalf("link %q %q %v", u, name, err)
	}
}
