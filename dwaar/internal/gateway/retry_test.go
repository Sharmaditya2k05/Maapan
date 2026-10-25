package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An edge 502 (no X-Request-ID) is retried with the body intact; a service's
// own 502 (stamped) is passed through untouched.
func TestEdgeRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"x":1}` {
			t.Errorf("body on attempt %d = %q", calls.Load()+1, b)
		}
		switch calls.Add(1) {
		case 1, 2:
			w.WriteHeader(http.StatusBadGateway) // edge: no request id
		default:
			w.Header().Set("X-Request-ID", "svc")
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	rt := edgeRetry{next: http.DefaultTransport, budget: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"x":1}`))
	res, err := rt.RoundTrip(req)
	if err != nil || res.StatusCode != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("res=%v err=%v calls=%d", res, err, calls.Load())
	}

	var own atomic.Int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		own.Add(1)
		w.Header().Set("X-Request-ID", "svc")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer svc.Close()
	req2, _ := http.NewRequest(http.MethodPost, svc.URL, strings.NewReader("{}"))
	res2, err := rt.RoundTrip(req2)
	if err != nil || res2.StatusCode != http.StatusBadGateway || own.Load() != 1 {
		t.Fatalf("service 502 must not be retried: res=%v err=%v calls=%d", res2, err, own.Load())
	}

	// No budget: the first answer is returned.
	calls.Store(0)
	req3, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"x":1}`))
	res3, _ := edgeRetry{next: http.DefaultTransport}.RoundTrip(req3)
	if res3.StatusCode != http.StatusBadGateway || calls.Load() != 1 {
		t.Fatalf("budget 0 must not retry: %d after %d calls", res3.StatusCode, calls.Load())
	}
}
