package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/taraju/kar/internal/compliance"
	"github.com/taraju/mool/auth"
)

func TestSettingsUpdateValidate(t *testing.T) {
	bad := "00" // 99 is Centre Jurisdiction and valid since mool/gst gained it
	if err := (&SettingsUpdate{StateCode: &bad}).Validate(); err == nil {
		t.Fatal("unknown state must fail")
	}
	g := "27AAPFU0939F1ZV"
	if err := (&SettingsUpdate{GSTIN: &g}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCanAccessStore(t *testing.T) {
	cashier := &auth.Principal{Claims: &auth.Claims{Role: auth.RoleCashier, Stores: []string{"s1"}}}
	owner := &auth.Principal{Claims: &auth.Claims{Role: auth.RoleSuperAdmin}}
	if !CanAccessStore(cashier, "s1") || CanAccessStore(cashier, "s2") || !CanAccessStore(owner, "s2") ||
		!CanAccessStore(&auth.Principal{Internal: true}, "s2") {
		t.Fatal("store scoping")
	}
}

// Routes must register without pattern conflicts, and must leave the
// /v1/returns/* paths free for kar/internal/returns.
func TestRoutesRegister(t *testing.T) {
	mux := http.NewServeMux()
	s := &Service{Engine: &compliance.Engine{}}
	s.Routes(mux, auth.NewVerifier("jwt-secret-jwt-secret-jwt-secret-32", "svc"), false)
	for _, p := range []string{"GET /v1/returns/gstr1", "GET /v1/returns/gstr3b"} {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {}) // panics if already registered
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/einvoices/abc"}, {"POST", "/v1/einvoices/abc/cancel"}, {"POST", "/v1/einvoices/abc/json"},
		{"GET", "/v1/einvoices"}, {"PUT", "/v1/eway-bills/abc/part-b"}, {"POST", "/v1/eway-bills/abc/extend"},
		{"PUT", "/v1/settings/gsp-credentials/27AAPFU0939F1ZV"}, {"POST", "/v1/hsn/import"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s %s not routed", c.method, c.path)
		}
	}
}
