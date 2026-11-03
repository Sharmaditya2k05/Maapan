package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maapan/mool/auth"
	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/jobwork"
	"github.com/maapan/nirmaan/internal/production"
)

// TestRoutesRegister registers every route on one mux (a pattern conflict
// panics) and checks a few resolve to an authenticated handler.
func TestRoutesRegister(t *testing.T) {
	mux := http.NewServeMux()
	v := auth.NewVerifier("test-jwt-secret-test-jwt-secret-32", "svc")
	(&bom.Service{}).Routes(mux, v)
	(&production.Service{}).Routes(mux, v)
	(&jobwork.Service{}).Routes(mux, v)
	for _, path := range []string{"/v1/boms", "/v1/production-runs/wip", "/v1/production-runs/x", "/v1/job-work/itc04", "/v1/job-work/pending"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}
