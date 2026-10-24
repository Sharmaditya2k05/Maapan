package recon

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

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/migrations"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/httpx"
)

const (
	testTenant = "11111111-1111-1111-1111-111111111111"
	ourGSTIN   = "27AAPFU0939F1ZV"
)

// NewTestPool connects to TEST_DATABASE_URL with a fresh schema holding only
// the returns migration (0003), dropped when the test ends.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://taraju:taraju@localhost:5432/taraju_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("kar_recon_test_%d", rand.Int63())
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

func userCtx() context.Context {
	c := &auth.Claims{TenantID: testTenant, Role: auth.RoleAccountant, RegisteredClaims: jwt.RegisteredClaims{Subject: "acct-1"}}
	return auth.WithPrincipal(context.Background(), &auth.Principal{Claims: c, TenantID: testTenant})
}

type fakeFetcher struct{ raw []byte }

func (f fakeFetcher) FetchGSTR2B(context.Context, string, string) ([]byte, error) { return f.raw, nil }

func call(t *testing.T, h httpx.HandlerFunc, method, target string, body []byte, pathKey string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body)).WithContext(userCtx())
	if pathKey != "" {
		r.SetPathValue("doc_key", pathKey)
	}
	w := httptest.NewRecorder()
	httpx.H(h).ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func TestImportMatchIMSExportRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	s := &Service{DB: pool, Log: slog.Default(),
		Purchases:    func(context.Context, string, string, string) ([]Purchase, error) { return books(), nil },
		ResolveGSTIN: func(*http.Request) (string, error) { return ourGSTIN, nil }}
	raw := loadSample(t)
	q := "?gstin=" + ourGSTIN + "&period=102026"

	// Wrong period / missing GSP are rejected clearly.
	if w := call(t, s.Import, "POST", "/v1/returns/gstr2b/import?period=092026", raw, ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong period: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s.Fetch, "POST", "/v1/returns/gstr2b/fetch"+q, nil, ""); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "GST Suvidha Provider") {
		t.Fatalf("fetch without GSP: %d %s", w.Code, w.Body.String())
	}

	// Import.
	w := call(t, s.Import, "POST", "/v1/returns/gstr2b/import"+q, raw, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}

	// Reconcile.
	w = call(t, s.List, "GET", "/v1/returns/gstr2b"+q, nil, "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	rec := decode[Reconciliation](t, w)
	if rec.Import == nil || rec.Import.DocCount != 5 || rec.Import.Source != "upload" || rec.Counts[StatusMatched] != 3 ||
		rec.Counts[StatusMismatch] != 1 || rec.Counts[StatusMissingInBooks] != 1 || rec.Counts[StatusMissingIn2B] != 2 { // p5 and p8 (no other period imported yet)
		t.Fatalf("reconciliation %s", w.Body.String())
	}
	var mismatchKey, unknownKey string
	for _, it := range rec.Items {
		switch it.Status {
		case StatusMismatch:
			mismatchKey = it.DocKey
		case StatusMissingInBooks:
			unknownKey = it.DocKey
		}
	}

	// IMS: reject the mismatch, bulk-accept the matched ones.
	if w := call(t, s.SetAction, "PUT", "/v1/returns/ims/x"+q, []byte(`{"action":"reject","remark":"IGST overcharged"}`), mismatchKey); w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s.SetAction, "PUT", "/v1/returns/ims/x"+q, []byte(`{"action":"maybe"}`), unknownKey); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad action: %d", w.Code)
	}
	if w := call(t, s.SetAction, "PUT", "/v1/returns/ims/x"+q, []byte(`{"action":"accept"}`), "NOPE_INV_1_2627"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown doc: %d", w.Code)
	}
	w = call(t, s.AcceptMatched, "POST", "/v1/returns/ims/accept-matched"+q, nil, "")
	acc := decode[struct {
		Accepted int `json:"accepted"`
	}](t, w)
	if w.Code != 200 || acc.Accepted != 3 {
		t.Fatalf("accept matched: %d %s", w.Code, w.Body.String())
	}
	// A second run accepts nothing new.
	if acc := decode[struct {
		Accepted int `json:"accepted"`
	}](t, call(t, s.AcceptMatched, "POST", "/v1/returns/ims/accept-matched"+q, nil, "")); acc.Accepted != 0 {
		t.Fatalf("second accept-matched: %d", acc.Accepted)
	}

	// Re-import keeps the IMS actions (they belong to the document, not the file).
	if w := call(t, s.Import, "POST", "/v1/returns/gstr2b/import"+q, raw, ""); w.Code != http.StatusCreated {
		t.Fatalf("re-import: %d", w.Code)
	}
	rec = decode[Reconciliation](t, call(t, s.List, "GET", "/v1/returns/gstr2b"+q+"&status=matched", nil, ""))
	if len(rec.Items) != 3 {
		t.Fatalf("status filter %+v", rec.Items)
	}
	for _, it := range rec.Items {
		if it.IMS == nil || it.IMS.Action != ActionAccept || it.IMS.Actor != "acct-1" {
			t.Errorf("matched doc lost its action: %+v", it)
		}
	}
	// ITC per 2B after IMS: ST/0042 + GTA-118 accepted, credit note accepted (−IGST 180), BE invoice rejected, X-1 not available.
	if rec.ITC.Available != (Heads{IGST: -18000, CGST: 12000, SGST: 12000}) || rec.ITC.Rejected.IGST != 185000 ||
		rec.ITC.NotAvailable.Docs != 1 || rec.ITC.ReverseCharge != (Heads{CGST: 5000, SGST: 5000}) {
		t.Errorf("itc %+v", rec.ITC)
	}
	sum, ok, err := s.ITCFor2B(userCtx(), testTenant, ourGSTIN, "102026")
	if err != nil || !ok || sum.Available != rec.ITC.Available {
		t.Errorf("ITCFor2B %+v %v %v", sum, ok, err)
	}

	// IMS export.
	w = call(t, s.Export, "GET", "/v1/returns/ims/export"+q, nil, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "IMS_"+ourGSTIN+"_102026.json") {
		t.Fatalf("export: %d %v", w.Code, w.Header())
	}
	f := decode[IMSFile](t, w)
	if f.Rtin != ourGSTIN || len(f.InvData.B2B) != 3 || len(f.InvData.CDN) != 1 {
		t.Fatalf("ims file %s", w.Body.String())
	}
	got := map[string]string{}
	for _, d := range f.InvData.B2B {
		got[d.Inum] = d.Action
	}
	if got["ST/0042"] != "A" || got["GTA-118"] != "A" || got["BE/26-27/0901"] != "R" || f.InvData.CDN[0].Action != "A" {
		t.Errorf("ims actions %v %+v", got, f.InvData.CDN)
	}
	if _, listed := got["X-1"]; listed {
		t.Error("a document without action must not be exported")
	}

	// The audit log has every action.
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ims_action_log WHERE tenant_id=$1`, testTenant).Scan(&n); err != nil || n != 4 {
		t.Errorf("action log rows %d %v", n, err)
	}

	// Fetch through a GSP goes down the same import path.
	s.Fetcher = fakeFetcher{raw: raw}
	if w := call(t, s.Fetch, "POST", "/v1/returns/gstr2b/fetch"+q, nil, ""); w.Code != http.StatusCreated ||
		!strings.Contains(w.Body.String(), `"source":"gsp"`) {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	// An empty statement from the GSP (simulator, hiccup) never wipes an import.
	s.Fetcher = fakeFetcher{raw: []byte(`{"data":{"gstin":"` + ourGSTIN + `","rtnprd":"102026","docdata":{"b2b":[]}}}`)}
	if w := call(t, s.Fetch, "POST", "/v1/returns/gstr2b/fetch"+q, nil, ""); w.Code != http.StatusConflict {
		t.Fatalf("empty fetch: %d %s", w.Code, w.Body.String())
	}
	// Another period's statement makes its documents "reported elsewhere".
	other := bytes.Replace(raw, []byte(`"rtnprd": "102026"`), []byte(`"rtnprd": "112026"`), 1)
	if w := call(t, s.Import, "POST", "/v1/returns/gstr2b/import?gstin="+ourGSTIN+"&period=112026", other, ""); w.Code != http.StatusCreated {
		t.Fatalf("import 112026: %d %s", w.Code, w.Body.String())
	}
	elsewhere, err := Store{DB: pool}.ReportedElsewhere(context.Background(), testTenant, ourGSTIN, "102026")
	if err != nil || !elsewhere["27AAPFU1111A1Z1|ST42"] {
		t.Errorf("reported elsewhere %v %v", elsewhere, err)
	}
}
