package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maapan/dwaar/internal/ratelimit"
	"github.com/maapan/mool/auth"
)

var secret = []byte("test-secret-test-secret-test-secret")

func TestParsePath(t *testing.T) {
	cases := []struct {
		in      string
		svc     string
		rest    string
		matched bool
	}{
		{"/api/aadesh/v1/invoices", "aadesh", "/v1/invoices", true},
		{"/api/pehchaan/v1/auth/otp/request", "pehchaan", "/v1/auth/otp/request", true},
		{"/api/bahi", "bahi", "/", true},
		{"/api/unknown/v1/x", "", "", false},
		{"/v1/invoices", "", "", false},
		{"/api/", "", "", false},
	}
	for _, c := range cases {
		got, ok := ParsePath(c.in)
		if ok != c.matched || got.Service != c.svc || got.Rest != c.rest {
			t.Errorf("ParsePath(%q) = %+v,%v", c.in, got, ok)
		}
	}
}

func TestBlocked(t *testing.T) {
	for _, p := range []string{"/internal/v1/stores/1", "/internal", "/events", "/Events", "//internal/v1/x", "/v1/../internal/x"} {
		if !Blocked(p) {
			t.Errorf("%q must be blocked", p)
		}
	}
	for _, p := range []string{"/v1/invoices", "/v1/internal-notes", "/v1/events-log"} {
		if Blocked(p) {
			t.Errorf("%q must pass", p)
		}
	}
}

func TestIsPublic(t *testing.T) {
	if !IsPublic("POST", "/api/pehchaan/v1/auth/otp/verify") || !IsPublic("POST", "/api/lenden/v1/webhooks/razorpay/") {
		t.Fatal("auth and webhook routes are public")
	}
	if IsPublic("GET", "/api/pehchaan/v1/auth/otp/verify") || IsPublic("POST", "/api/pehchaan/v1/users") {
		t.Fatal("other routes need a token")
	}
}

func TestStripSpoofed(t *testing.T) {
	h := http.Header{}
	for _, k := range []string{"X-Service-Secret", "X-Tenant-ID", "X-User-ID", "X-Role", "X-Serverless-Authorization"} {
		h.Set(k, "evil")
	}
	h.Set("X-Store-ID", "s1")
	StripSpoofed(h)
	for _, k := range spoofable {
		if h.Get(k) != "" {
			t.Errorf("%s survived", k)
		}
	}
	if h.Get("X-Store-ID") != "s1" {
		t.Error("store selection must be kept")
	}
}

func token(t *testing.T, role string, stores ...string) string {
	t.Helper()
	tok, err := auth.Issue(secret, auth.Claims{
		TenantID: "t1", StoreID: "s1", Role: role, Stores: stores,
		RegisteredClaims: auth.RegisteredClaimsFor("u1"),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func newGateway(t *testing.T, upstream string, limits Limits) *Gateway {
	u, _ := url.Parse(upstream)
	return New(Options{
		JWTSecret: secret,
		Upstreams: Upstreams{"aadesh": u, "pehchaan": u},
		Limiter:   ratelimit.NewMemory(),
		Limits:    limits,
	})
}

func TestProxyForwardsIdentity(t *testing.T) {
	var got *http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}))
	defer up.Close()
	g := newGateway(t, up.URL, Limits{UserPerMinute: 100, PublicPerMinute: 100})

	req := httptest.NewRequest("GET", "/api/aadesh/v1/invoices?limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, auth.RoleCashier, "s1", "s2"))
	req.Header.Set("X-Store-ID", "s2")
	req.Header.Set("X-Tenant-ID", "attacker")
	req.Header.Set("X-Service-Secret", "guess")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got.URL.Path != "/v1/invoices" || got.URL.RawQuery != "limit=5" {
		t.Errorf("upstream got %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("X-Tenant-ID") != "t1" || got.Header.Get("X-User-ID") != "u1" ||
		got.Header.Get("X-Role") != auth.RoleCashier || got.Header.Get("X-Store-ID") != "s2" {
		t.Errorf("identity headers wrong: %v", got.Header)
	}
	if got.Header.Get("X-Service-Secret") != "" || got.Header.Get(hdrRest) != "" {
		t.Error("spoofed/internal headers leaked upstream")
	}
	if !strings.HasPrefix(got.Header.Get("Authorization"), "Bearer ") {
		t.Error("user token must be forwarded")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("upstream CORS headers must be dropped")
	}
}

func TestAuthAndStoreChecks(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	g := newGateway(t, up.URL, Limits{UserPerMinute: 100, PublicPerMinute: 100})

	do := func(method, path, tok, store string) int {
		req := httptest.NewRequest(method, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if store != "" {
			req.Header.Set("X-Store-ID", store)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("GET", "/api/aadesh/v1/invoices", "", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := do("POST", "/api/pehchaan/v1/auth/otp/request", "", ""); c != 200 {
		t.Errorf("public route: %d", c)
	}
	if c := do("GET", "/api/aadesh/v1/invoices", token(t, auth.RoleCashier, "s1"), "s9"); c != 403 {
		t.Errorf("foreign store: %d", c)
	}
	if c := do("GET", "/api/aadesh/v1/invoices", token(t, auth.RoleSuperAdmin), "s9"); c != 200 {
		t.Errorf("admin any store: %d", c)
	}
	if c := do("GET", "/api/aadesh/internal/v1/invoices", token(t, auth.RoleSuperAdmin), ""); c != 404 {
		t.Errorf("internal must be hidden: %d", c)
	}
	if c := do("POST", "/api/aadesh/events", "", ""); c != 404 {
		t.Errorf("events must be hidden: %d", c)
	}
	if c := do("GET", "/api/bahi/v1/parties", token(t, auth.RoleSuperAdmin), ""); c != 503 {
		t.Errorf("unconfigured upstream: %d", c)
	}
}

func TestPublicRateLimit(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	g := newGateway(t, up.URL, Limits{PublicPerMinute: 2, UserPerMinute: 100})
	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/api/pehchaan/v1/auth/otp/request", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
		if rec.Code == 429 && rec.Header().Get("Retry-After") == "" {
			t.Error("429 needs Retry-After")
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes %v", codes)
	}
}
