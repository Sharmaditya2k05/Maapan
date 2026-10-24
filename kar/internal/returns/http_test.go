package returns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/migrations"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/httpx"
)

func serve(t *testing.T, ctx context.Context, h httpx.HandlerFunc, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	httpx.H(h).ServeHTTP(w, r)
	return w
}

func TestRegistrationResolution(t *testing.T) {
	s := newService(Deps{Invoices: &fakeSource{}, Registrations: regs})
	ctx := principalCtx()
	if r, err := s.registration(ctx, ""); err != nil || r.GSTIN != ourGSTIN {
		t.Fatalf("default primary %+v %v", r, err)
	}
	if r, err := s.registration(ctx, strings.ToLower(otherGSTIN)); err != nil || r.GSTIN != otherGSTIN || r.StateCode != "27" {
		t.Fatalf("second registration %+v %v", r, err)
	}
	if _, err := s.registration(ctx, "29AAACB1234C1Z5"); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("foreign gstin must be rejected: %v", err)
	}
	none := newService(Deps{Invoices: &fakeSource{}, Registrations: fakeRegs{{GSTIN: "", StateCode: "27", IsPrimary: true}}})
	if _, err := none.registration(ctx, ""); err == nil {
		t.Fatal("an unregistered business has no return")
	}
}

func TestReturnHandlers(t *testing.T) {
	src := &fakeSource{invs: fixture()}
	s := newService(Deps{Invoices: src, Registrations: regs})
	acct := principalCtx()

	w := serve(t, acct, s.gstr1, "GET", "/v1/returns/gstr1?period=102026", nil)
	if w.Code != 200 {
		t.Fatalf("gstr1 %d %s", w.Code, w.Body.String())
	}
	var g GSTR1
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || g.Gstin != ourGSTIN || len(g.B2B) != 4 || g.Summary == nil {
		t.Fatalf("gstr1 body %v %s", err, w.Body.String())
	}

	// The second registration's return holds only its own invoice.
	w = serve(t, acct, s.gstr1, "GET", "/v1/returns/gstr1?period=102026&gstin="+otherGSTIN, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || len(g.B2CS) != 1 || g.B2CS[0].Txval != 777700 {
		t.Fatalf("other gstin %s", w.Body.String())
	}

	if w := serve(t, acct, s.gstr1, "GET", "/v1/returns/gstr1?period=2026-10", nil); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad period %d", w.Code)
	}
	// Downloads need gst:file — a store manager has report:read only.
	if w := serve(t, principalAs(auth.RoleStoreManager), s.gstr1, "GET", "/v1/returns/gstr1?period=102026&format=csv", nil); w.Code != http.StatusForbidden {
		t.Fatalf("store manager download %d", w.Code)
	}
	w = serve(t, acct, s.gstr1Export, "GET", "/v1/returns/gstr1/export?period=102026&format=csv&section=hsn_b2c", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "HSN,Description,UQC") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "GSTR1_"+ourGSTIN+"_102026_hsn_b2c.csv") {
		t.Fatalf("hsn csv %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if w := serve(t, acct, s.gstr1Export, "GET", "/v1/returns/gstr1/export?period=102026&format=csv&section=b2x", nil); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad section %d", w.Code)
	}
	w = serve(t, acct, s.gstr1Export, "GET", "/v1/returns/gstr1/export?period=102026", nil)
	var portal map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &portal); err != nil || portal["summary"] != nil || portal["warnings"] != nil || portal["b2b"] == nil {
		t.Fatalf("portal json %v %s", err, w.Body.String())
	}

	w = serve(t, acct, s.gstr3b, "GET", "/v1/returns/gstr3b?period=102026", nil)
	var g3 GSTR3B
	if err := json.Unmarshal(w.Body.Bytes(), &g3); err != nil || w.Code != 200 || g3.Computation.TotalCash.IGST != 2437500 {
		t.Fatalf("gstr3b %v %s", err, w.Body.String())
	}
	if w := serve(t, acct, s.gstr3b, "GET", "/v1/returns/gstr3b?period=102026&format=csv", nil); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "4(A)(3)") {
		t.Fatalf("gstr3b csv %d", w.Code)
	}

	// Register mounts every route without colliding.
	mux := http.NewServeMux()
	Register(mux, auth.NewVerifier("jwt-secret", "svc-secret"), Deps{Invoices: src, Registrations: regs})
	r := httptest.NewRequest("GET", "/v1/returns/gstr1?period=102026", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, r)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("routes need a user token: %d", rw.Code)
	}
}

func newReturnsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("kar_returns_test_%d", rand.Int63())
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sql, err := fs.ReadFile(migrations.FS, "0003_returns.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, fstest.MapFS{"0003_returns.up.sql": {Data: sql}}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestOpeningFeedsSetOff(t *testing.T) {
	pool := newReturnsTestPool(t)
	s := newService(Deps{DB: pool, Invoices: &fakeSource{invs: fixture()}, Registrations: regs})
	acct := principalCtx()

	if w := serve(t, acct, s.putOpening, "PUT", "/v1/returns/itc-opening", []byte(`{"period":"102026","igst":-1}`)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("negative opening %d", w.Code)
	}
	if w := serve(t, acct, s.putOpening, "PUT", "/v1/returns/itc-opening", []byte(`{"gstin":"29AAACB1234C1Z5","period":"102026","igst":1}`)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("foreign gstin %d", w.Code)
	}
	w := serve(t, acct, s.putOpening, "PUT", "/v1/returns/itc-opening", []byte(`{"period":"102026","igst":30000,"cgst":0,"sgst":0,"cess":0}`))
	if w.Code != 200 {
		t.Fatalf("put opening %d %s", w.Code, w.Body.String())
	}
	w = serve(t, acct, s.getOpening, "GET", "/v1/returns/itc-opening?period=102026", nil)
	if !strings.Contains(w.Body.String(), `"igst":30000.00`) || !strings.Contains(w.Body.String(), `"set":true`) {
		t.Fatalf("get opening %s", w.Body.String())
	}
	w = serve(t, acct, s.gstr3b, "GET", "/v1/returns/gstr3b?period=102026", nil)
	var g GSTR3B
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	c := g.Computation
	if c.OpeningITC.IGST != 3000000 || c.CashPayable != (Taxes{}) || c.TotalCash != (Taxes{CGST: 5000, SGST: 5000}) {
		t.Fatalf("opening not applied: %s", w.Body.String())
	}
	if g.ITC2B == nil || g.ITC2B.Imported {
		t.Fatalf("no 2B imported yet: %+v", g.ITC2B)
	}
}
