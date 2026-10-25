package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/maapan/mool/httpx"
)

// Wake reports each upstream's /healthz and is ready only when all are up.
func TestWake(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("wake must call /healthz, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	u1, _ := url.Parse(up.URL)
	u2, _ := url.Parse(down.URL)

	check := func(ups Upstreams, wantReady bool, want map[string]string) {
		t.Helper()
		g := New(Options{Upstreams: ups})
		rec := httptest.NewRecorder()
		httpx.H(g.Wake).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wake", nil))
		var got struct {
			Ready    bool              `json:"ready"`
			Services map[string]string `json:"services"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s err %v", rec.Code, rec.Body, err)
		}
		if got.Ready != wantReady || len(got.Services) != len(want) {
			t.Fatalf("got %+v", got)
		}
		for k, v := range want {
			if got.Services[k] != v {
				t.Fatalf("%s = %q, want %q", k, got.Services[k], v)
			}
		}
	}
	check(Upstreams{"kar": u1, "bahi": u1}, true, map[string]string{"kar": "up", "bahi": "up"})
	check(Upstreams{"kar": u1, "bahi": u2}, false, map[string]string{"kar": "up", "bahi": "down"})
}
