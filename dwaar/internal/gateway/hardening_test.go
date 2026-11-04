package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maapan/dwaar/internal/ratelimit"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		xff     []string
		trusted int
		want    string
	}{
		{nil, 1, "192.0.2.1"},                                          // no header: TCP peer
		{[]string{"203.0.113.7"}, 1, "203.0.113.7"},                    // Cloud Run / Render front end
		{[]string{"6.6.6.6, 203.0.113.7"}, 1, "203.0.113.7"},           // spoofed first hop ignored
		{[]string{"6.6.6.6", "203.0.113.7"}, 1, "203.0.113.7"},         // repeated header lines
		{[]string{"6.6.6.6, 203.0.113.7, 35.1.1.1"}, 2, "203.0.113.7"}, // Google LB appends "client, lb"
		{[]string{"203.0.113.7"}, 2, "203.0.113.7"},                    // fewer hops than trusted: leftmost
		{[]string{"6.6.6.6, 203.0.113.7"}, 0, "192.0.2.1"},             // header not trusted at all
		{[]string{"not-an-ip"}, 1, "192.0.2.1"},                        // garbage falls back to the peer
		{[]string{"2001:db8::1"}, 1, "2001:db8::1"},
		{[]string{"203.0.113.7, 10.201.4.9"}, 1, "203.0.113.7"},           // Render: internal hop behind the edge
		{[]string{"6.6.6.6, 203.0.113.7, 10.201.4.9"}, 1, "203.0.113.7"},  // ...spoofed first hop still ignored
		{[]string{"203.0.113.7, 172.16.0.2, 10.0.0.1"}, 1, "203.0.113.7"}, // several internal hops
		{[]string{"10.0.0.5"}, 1, "10.0.0.5"},                             // only internal hops: keep it
		{[]string{"192.168.1.20, 127.0.0.1"}, 1, "192.168.1.20"},          // local dev: leftmost private
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil) // RemoteAddr 192.0.2.1:1234
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := ClientIP(r, c.trusted); got != c.want {
			t.Errorf("ClientIP(%v, %d) = %q, want %q", c.xff, c.trusted, got, c.want)
		}
	}
}

// A client cannot dodge the OTP limit by rotating a fake first X-Forwarded-For hop,
// and the services receive the resolved IP as the only hop.
func TestSpoofedForwardedForDoesNotDodgeLimit(t *testing.T) {
	var seen atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("X-Forwarded-For"))
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	g := New(Options{JWTSecret: secret, Upstreams: Upstreams{"pehchaan": u}, Limiter: ratelimit.NewMemory(),
		Limits: Limits{PublicPerMinute: 2}, TrustedProxies: 1})
	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/api/pehchaan/v1/auth/otp/request", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('1'+i))+", 203.0.113.7")
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes %v", codes)
	}
	if got := seen.Load(); got != "203.0.113.7" {
		t.Fatalf("upstream X-Forwarded-For = %q", got)
	}
}

func TestWebhooksHaveTheirOwnBucket(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	g := New(Options{JWTSecret: secret, Upstreams: Upstreams{"pehchaan": u, "soochna": u}, Limiter: ratelimit.NewMemory(),
		Limits: Limits{PublicPerMinute: 1, WebhookPerMinute: 5}})
	do := func(path string) int {
		req := httptest.NewRequest("POST", path, nil)
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("/api/pehchaan/v1/auth/otp/request"); c != 200 {
		t.Fatalf("first otp: %d", c)
	}
	if c := do("/api/pehchaan/v1/auth/otp/request"); c != 429 {
		t.Fatalf("second otp: %d", c)
	}
	for i := 0; i < 5; i++ {
		if c := do("/api/soochna/v1/webhooks/msg91"); c != 200 {
			t.Fatalf("delivery report %d: %d (must not share the OTP bucket)", i+1, c)
		}
	}
	if c := do("/api/soochna/v1/webhooks/msg91"); c != 429 {
		t.Fatalf("webhook burst over its own limit: %d", c)
	}
	if c := do("/api/soochna/v1/webhooks/gupshup"); c != 200 {
		t.Fatalf("each provider route has its own bucket: %d", c)
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return true, 0, errors.New("redis down")
}

func TestOTPRoutesFailClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	g := New(Options{JWTSecret: secret, Upstreams: Upstreams{"pehchaan": u, "aadesh": u}, Limiter: brokenLimiter{},
		Limits: Limits{PublicPerMinute: 5, UserPerMinute: 5}})
	do := func(method, path, tok string) int {
		req := httptest.NewRequest(method, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("POST", "/api/pehchaan/v1/auth/otp/request", ""); c != 503 {
		t.Fatalf("OTP must be refused when the limiter fails: %d", c)
	}
	if c := do("POST", "/api/pehchaan/v1/auth/refresh", ""); c != 200 {
		t.Fatalf("refresh may fail open: %d", c)
	}
	if c := do("GET", "/api/aadesh/v1/invoices", token(t, auth.RoleSuperAdmin)); c != 200 {
		t.Fatalf("billing must not stop when the limiter fails: %d", c)
	}

	// With the production wiring (Redis + memory fallback) the OTP limit holds.
	g.opt.Limiter = ratelimit.NewFallback(brokenLimiter{}, nil)
	g.opt.Limits.PublicPerMinute = 1
	if c := do("POST", "/api/pehchaan/v1/auth/otp/request", ""); c != 200 {
		t.Fatalf("first OTP with fallback: %d", c)
	}
	if c := do("POST", "/api/pehchaan/v1/auth/otp/request", ""); c != 429 {
		t.Fatalf("second OTP with fallback must be limited: %d", c)
	}
}

func TestIdempotencyKeyPassthrough(t *testing.T) {
	var got atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get(HeaderIdempotencyKey))
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	g := newGateway(t, up.URL, Limits{UserPerMinute: 100})
	do := func(method, key string) int {
		got.Store("")
		req := httptest.NewRequest(method, "/api/aadesh/v1/invoices", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token(t, auth.RoleSuperAdmin))
		if key != "" {
			req.Header.Set(HeaderIdempotencyKey, key)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, m := range []string{"POST", "PUT", "PATCH"} {
		if c := do(m, "pos-7f3a-0001"); c != 200 || got.Load() != "pos-7f3a-0001" {
			t.Fatalf("%s: %d key=%q", m, c, got.Load())
		}
	}
	if c := do("GET", "pos-7f3a-0001"); c != 200 || got.Load() != "" {
		t.Fatalf("GET must drop the key: %d %q", c, got.Load())
	}
	if c := do("POST", strings.Repeat("k", 129)); c != 400 {
		t.Fatalf("long key: %d", c)
	}
	if c := do("POST", "has space"); c != 400 {
		t.Fatalf("bad key: %d", c)
	}
}

func TestPreviousJWTSecretAccepted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	newSecret := []byte("new-secret-new-secret-new-secret-new")
	g := New(Options{JWTSecret: newSecret, PreviousJWTSecret: secret, Upstreams: Upstreams{"aadesh": u},
		Limiter: ratelimit.NewMemory(), Limits: Limits{UserPerMinute: 100}})
	req := httptest.NewRequest("GET", "/api/aadesh/v1/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, auth.RoleSuperAdmin)) // signed with the old secret
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("token signed with JWT_SECRET_PREVIOUS: %d", rec.Code)
	}
	g.opt.PreviousJWTSecret = nil
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("without the previous secret: %d", rec.Code)
	}
}

func TestCatalogGate(t *testing.T) {
	for _, c := range []struct {
		mode string
		tok  bool
		want int
	}{
		{CatalogPublic, false, 200}, {"", false, 200},
		{CatalogAuth, false, 401}, {CatalogAuth, true, 200},
		{CatalogOff, true, 404},
	} {
		g := New(Options{JWTSecret: secret, Catalog: c.mode})
		req := httptest.NewRequest("GET", "/api", nil)
		if c.tok {
			req.Header.Set("Authorization", "Bearer "+token(t, auth.RoleCashier, "s1"))
		}
		rec := httptest.NewRecorder()
		httpx.H(g.Catalog).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("mode %q token=%v: %d, want %d", c.mode, c.tok, rec.Code, c.want)
		}
	}
}

func TestStatusAndMetrics(t *testing.T) {
	var probes atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			probes.Add(1)
			w.Header().Set(httpx.HeaderRequestID, "x")
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(httpx.HeaderRequestID, "x") // the service itself answered 503
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	sleeping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // edge, no request id
	}))
	defer sleeping.Close()
	u1, _ := url.Parse(up.URL)
	u2, _ := url.Parse(down.URL)
	u3, _ := url.Parse(sleeping.URL)
	g := New(Options{JWTSecret: secret, Upstreams: Upstreams{"aadesh": u1, "bhandar": u2, "kar": u3},
		Limiter: ratelimit.NewMemory(), Limits: Limits{UserPerMinute: 100}, StatusTTL: time.Minute})

	// One proxied request to have traffic to report.
	req := httptest.NewRequest("POST", "/api/aadesh/v1/invoices", strings.NewReader(`{"a":1}`))
	req.Header.Set("Authorization", "Bearer "+token(t, auth.RoleSuperAdmin))
	g.ServeHTTP(httptest.NewRecorder(), req)

	get := func(tok string) StatusReport {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/_status", nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		httpx.H(g.Status).ServeHTTP(rec, r)
		var rep StatusReport
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &rep) != nil {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		return rep
	}
	rep := get("")
	if rep.Status != "degraded" || rep.RateLimiter != "memory" {
		t.Fatalf("report %+v", rep)
	}
	want := map[string]string{"aadesh": HealthUp, "bhandar": HealthDown, "kar": HealthStarting, "bahi": HealthNotConfigured}
	for k, v := range want {
		if rep.Services[k].Status != v {
			t.Errorf("%s = %q, want %q", k, rep.Services[k].Status, v)
		}
	}
	if rep.Services["aadesh"].Traffic != nil {
		t.Error("traffic counters are for authenticated callers only")
	}
	rep = get(token(t, auth.RoleSuperAdmin))
	tr := rep.Services["aadesh"].Traffic
	if tr == nil || tr.Requests != 1 || tr.BytesIn != 7 || tr.BytesOut != int64(len(`{"ok":true}`)) {
		t.Fatalf("traffic %+v", tr)
	}
	if probes.Load() != 1 {
		t.Fatalf("status must be cached, probed %d times", probes.Load())
	}
}

// Behind Cloudflare (Render) the edge's CF-Connecting-IP names the client; the
// X-Forwarded-For hops nearest dwaar are Cloudflare and Render's own proxy.
func TestClientIPHeader(t *testing.T) {
	var seen atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("X-Forwarded-For"))
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	for _, c := range []struct{ header, cf, want string }{
		{"CF-Connecting-IP", "182.77.72.107", "182.77.72.107"},
		{"CF-Connecting-IP", "", "172.71.124.73"},        // header missing: X-Forwarded-For
		{"CF-Connecting-IP", "garbage", "172.71.124.73"}, // unparsable: X-Forwarded-For
		{"", "182.77.72.107", "172.71.124.73"},           // not configured: header ignored
	} {
		g := New(Options{JWTSecret: secret, Upstreams: Upstreams{"pehchaan": u}, Limiter: ratelimit.NewMemory(),
			Limits: Limits{PublicPerMinute: 100}, TrustedProxies: 1, ClientIPHeader: c.header})
		req := httptest.NewRequest("POST", "/api/pehchaan/v1/auth/otp/request", nil)
		req.Header.Set("X-Forwarded-For", "182.77.72.107, 172.71.124.73, 10.29.161.137")
		if c.cf != "" {
			req.Header.Set("CF-Connecting-IP", c.cf)
		}
		g.ServeHTTP(httptest.NewRecorder(), req)
		if got := seen.Load(); got != c.want {
			t.Errorf("%+v: upstream X-Forwarded-For = %q", c, got)
		}
	}
}
