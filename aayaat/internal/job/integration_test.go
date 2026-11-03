package job

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/aayaat/internal/clients"
	"github.com/maapan/aayaat/internal/storage"
	"github.com/maapan/aayaat/migrations"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// fake is a stand-in for bahi / bhandar: it records every call and answers
// with what the test's handler returns.
type fake struct {
	mu      sync.Mutex
	calls   []call
	handler func(path string, body map[string]any) (int, any)
	srv     *httptest.Server
}

type call struct {
	Path string
	Body map[string]any
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.handler = func(path string, body map[string]any) (int, any) { return 200, okFor(body) }
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if r.Header.Get(auth.HeaderTenantID) == "" {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, call{r.URL.Path, body})
		h := f.handler
		f.mu.Unlock()
		status, out := h(r.URL.Path, body)
		if status == 404 {
			http.NotFound(w, r) // Go mux's plain-text 404, as a service without the route answers
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// records returns the array of records of a bulk body.
func records(body map[string]any) []any {
	for _, k := range []string{"parties", "balances", "items", "lines", "bills", "invoices", "records"} {
		if v, ok := body[k].([]any); ok {
			return v
		}
	}
	return nil
}

func okFor(body map[string]any) clients.BulkResult {
	return clients.BulkResult{Created: len(records(body)), Errors: []clients.BulkError{}}
}

func (f *fake) callsTo(path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	s       *Service
	pool    *pgxpool.Pool
	bahi    *fake
	bhandar *fake
	p       *auth.Principal
	dir     string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://taraju:taraju@localhost:5432/taraju_test_aayaat?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("aayaat_test_%d", rand.Int63())
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Logf("pgcrypto: %v", err) // gen_random_uuid is built in from Postgres 13
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
	if err := db.Migrate(ctx, pool, migrations.FS, slog.Default()); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, bahi: newFake(t), bhandar: newFake(t), dir: t.TempDir()}
	now := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	e.s = &Service{DB: pool, Store: storage.Local{Dir: e.dir}, Log: slog.Default(), Now: func() time.Time { return now }, sync: true,
		Targets: clients.Targets{Bahi: svc.New(e.bahi.srv.URL, "secret", false), Bhandar: svc.New(e.bhandar.srv.URL, "secret", false)}}
	claims := &auth.Claims{TenantID: uuid.NewString(), Role: "accountant"}
	claims.Subject = "user-1"
	e.p = &auth.Principal{Claims: claims, TenantID: claims.TenantID}
	return e
}

func (e *env) upload(t *testing.T, file, kind string, params Params) *Job {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	j, err := e.s.Create(context.Background(), e.p, kind, filepath.Base(file), "", f, params)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// mapAndValidate applies the suggested mapping (plus params) and validates.
func (e *env) mapAndValidate(t *testing.T, j *Job, params Params) *ValidateSummary {
	t.Helper()
	ctx := context.Background()
	pv, err := e.s.Preview(ctx, e.p.TenantID, j.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetMapping(ctx, e.p, j.ID, MappingInput{Sheet: pv.Sheet, Mapping: pv.SuggestedMapping, Params: &params}); err != nil {
		t.Fatal(err)
	}
	sum, err := e.s.Validate(ctx, e.p, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func (e *env) get(t *testing.T, id string) *Job {
	t.Helper()
	j, err := e.s.Get(context.Background(), e.p.TenantID, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e *env) rowStatus(t *testing.T, id string) map[string]int {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT status, count(*) FROM import_rows WHERE job_id=$1 GROUP BY 1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		out[s] = n
	}
	return out
}

func TestPartiesKeepAuditAndResume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.s.BatchSize = 3
	j := e.upload(t, "marg/PartyMaster.csv", "parties", Params{})
	sum := e.mapAndValidate(t, j, Params{AsOf: "2026-04-01"})
	if sum.Job.TotalRows != 10 || sum.Job.ValidRows != 8 || sum.Job.ErrorRows != 2 || sum.Job.WarningRows < 4 || len(sum.SampleWarnings) == 0 {
		t.Fatalf("summary %+v", sum.Job)
	}
	// Second bulk call fails as a whole (4xx, not retried): the job fails after 3 rows.
	n := 0
	e.bahi.handler = func(path string, body map[string]any) (int, any) {
		n++
		if n == 2 {
			return 422, map[string]any{"error": httpx.Validation("bahi is having a bad day")}
		}
		return 200, okFor(body)
	}
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	j = e.get(t, j.ID)
	if j.Status != StatusFailed || j.Imported != 3 || j.Error == nil {
		t.Fatalf("after failure %+v", j)
	}
	first := e.bahi.callsTo(clients.PathParties)[0]
	p0 := records(first.Body)[0].(map[string]any)
	if p0["opening_as_of"] != "2026-04-01" || p0["credit_days"] != float64(30) || p0["pan"] != nil {
		t.Fatalf("party payload %v (pan is not sent until BAHI_EXTRAS)", p0)
	}

	// Changing columns on a partly imported job is refused…
	if _, err := e.s.SetMapping(ctx, e.p, j.ID, MappingInput{Mapping: map[string]string{"name": "Party Name"}}); err == nil {
		t.Fatal("re-mapping a partly imported job must conflict")
	}
	// …but validating again keeps the audit of the 3 imported rows.
	if _, err := e.s.Validate(ctx, e.p, j.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.rowStatus(t, j.ID); st[RowImported] != 3 || st[RowValid] != 5 || st[RowInvalid] != 2 {
		t.Fatalf("statuses after re-validate %v", st)
	}
	if j = e.get(t, j.ID); j.Imported != 3 || j.Status != StatusValidated {
		t.Fatalf("%+v", j)
	}

	// Resume: only the 5 remaining rows are sent.
	e.bahi.handler = func(path string, body map[string]any) (int, any) { return 200, okFor(body) }
	before := len(e.bahi.callsTo(clients.PathParties))
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, c := range e.bahi.callsTo(clients.PathParties)[before:] {
		sent += len(records(c.Body))
	}
	if j = e.get(t, j.ID); j.Status != StatusCompleted || j.Imported != 8 || sent != 5 {
		t.Fatalf("resume: %+v sent %d", j, sent)
	}
	bs, _ := e.s.Batches(ctx, e.p.TenantID, j.ID)
	if len(bs) != 4 || bs[1].Status != "failed" || bs[0].Status != "recorded" || bs[3].Records != 2 {
		t.Fatalf("batches %+v", bs)
	}
	var outbox int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type='aayaat.import.completed'`).Scan(&outbox)
	if outbox != 1 {
		t.Fatalf("outbox %d", outbox)
	}
}

func TestCrashBetweenPostAndRecordIsResent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.upload(t, "marg/PartyMaster.csv", "parties", Params{})
	e.mapAndValidate(t, j, Params{AsOf: "2026-04-01"})
	// A previous instance posted batch 1 and died before recording it.
	if _, err := e.pool.Exec(ctx, `INSERT INTO import_batches (job_id, tenant_id, batch_no, path, first_row, last_row, records, status)
		VALUES ($1,$2,1,$3,5,14,8,'sent')`, j.ID, e.p.TenantID, clients.PathParties); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE import_jobs SET status='running', updated_at=now() - interval '1 hour' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.s.RecoverStale(ctx, 15*time.Minute); n != 1 {
		t.Fatalf("recovered %d", n)
	}
	if j = e.get(t, j.ID); j.Status != StatusFailed || !strings.Contains(*j.Error, "interrupted") {
		t.Fatalf("%+v", j)
	}
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	bs, _ := e.s.Batches(ctx, e.p.TenantID, j.ID)
	if len(bs) != 2 || bs[0].Status != "failed" || !strings.Contains(*bs[0].Error, "re-sent") || bs[1].Status != "recorded" {
		t.Fatalf("batches %+v", bs)
	}
	if j = e.get(t, j.ID); j.Status != StatusCompleted || j.Imported != 8 {
		t.Fatalf("%+v", j)
	}
}

func TestOpeningStockPerStoreDryRunAndCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	main, counter, cold := uuid.NewString(), uuid.NewString(), uuid.NewString()
	params := Params{AsOf: "2026-04-01", StoreMap: map[string]string{"Main Godown": main, "Counter": counter, "Cold Room": cold}}
	j := e.upload(t, "marg/StockBatch.csv", "opening_stock", params)
	sum := e.mapAndValidate(t, j, params)
	// 8 rows: zero qty skipped; bare-year expiry, unmapped store, duplicate invalid.
	if sum.Job.ValidRows != 4 || sum.Job.SkippedRows != 1 || sum.Job.ErrorRows != 3 {
		t.Fatalf("%+v %+v", sum.Job, sum.SampleErrors)
	}
	dr, err := e.s.DryRunCommit(ctx, e.p.TenantID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dr.WouldSend != 4 || len(dr.Calls) != 3 || dr.Calls[0].StoreID != main || dr.Calls[0].Records != 2 || dr.Calls[1].StoreID != counter {
		t.Fatalf("dry run calls %+v", dr.Calls)
	}
	if len(dr.Unresolved) != 3 || dr.Unresolved[0].Ref != "I0001" {
		t.Fatalf("items are in no items import: %+v", dr.Unresolved)
	}
	if strings.Join(dr.NotSentFields, ",") != "mfg_date,mrp_paise" {
		t.Fatalf("not sent %v", dr.NotSentFields)
	}
	if len(e.bhandar.calls) != 0 || e.get(t, j.ID).Status != StatusValidated {
		t.Fatal("a dry run sends nothing and changes nothing")
	}
	// With BHANDAR_EXTRAS the batch fields go along.
	e.s.Targets.BhandarExtras = true
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	calls := e.bhandar.callsTo(clients.PathOpeningStock)
	if len(calls) != 3 || calls[0].Body["store_id"] != main || calls[0].Body["as_of"] != "2026-04-01" {
		t.Fatalf("calls %+v", calls)
	}
	l0 := records(calls[0].Body)[0].(map[string]any)
	if l0["mfg_date"] != "2026-01-31" || l0["mrp_paise"] != float64(1000) || l0["store_id"] != nil {
		t.Fatalf("line %v", l0)
	}
	if j = e.get(t, j.ID); j.Status != StatusCompleted || j.Imported != 4 {
		t.Fatalf("%+v", j)
	}
}

func TestItemsWithOpeningStockAndRollback(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	store := uuid.NewString()
	j := e.upload(t, "marg/ItemMaster.xlsx", "items", Params{})
	sum := e.mapAndValidate(t, j, Params{StoreID: store})
	if sum.Job.ValidRows != 4 || sum.Job.ErrorRows != 3 {
		t.Fatalf("%+v %+v", sum.Job, sum.SampleErrors)
	}
	// bhandar refuses the opening stock of one item.
	e.bhandar.handler = func(path string, body map[string]any) (int, any) {
		res := okFor(body)
		if path == clients.PathOpeningStock {
			for i, l := range records(body) {
				if l.(map[string]any)["item_external_ref"] == "I0004" {
					res.Errors = append(res.Errors, clients.BulkError{Index: i, Message: "batch number required for a batch-wise item"})
					res.Created--
				}
			}
		}
		return 200, res
	}
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	items := e.bhandar.callsTo(clients.PathItems)
	opening := e.bhandar.callsTo(clients.PathOpeningStock)
	if len(items) != 1 || len(opening) != 1 || len(records(opening[0].Body)) != 3 || opening[0].Body["store_id"] != store {
		t.Fatalf("items %d opening %+v", len(items), opening)
	}
	it := records(items[0].Body)[0].(map[string]any)
	if it["category"] != "Biscuits" || it["brand"] != "Parle" || it["track_batches"] != true || it["alt_units"] != nil || it["opening_qty"] != nil {
		t.Fatalf("item payload %v", it)
	}
	j = e.get(t, j.ID)
	if j.Status != StatusCompleted || j.Imported != 3 || j.Failed != 1 {
		t.Fatalf("%+v", j)
	}
	failed, _, _ := e.s.Rows(ctx, e.p.TenantID, j.ID, RowFailed, httpx.Page{Limit: 5})
	if len(failed) != 1 || !strings.Contains(failed[0].Errors[0], "item imported; opening stock failed") {
		t.Fatalf("%+v", failed)
	}

	// bhandar cannot roll back yet: 503, nothing changes.
	e.bhandar.handler = func(path string, body map[string]any) (int, any) { return 404, nil }
	if _, err := e.s.Rollback(ctx, e.p.TenantID, j.ID, 0); err == nil || httpx.AsError(err).Status != 503 {
		t.Fatalf("rollback without the endpoint: %v", err)
	}
	if e.get(t, j.ID).Imported != 3 {
		t.Fatal("nothing rolled back")
	}
	// Once bhandar offers it: opening stock first, then the items.
	e.bhandar.handler = func(path string, body map[string]any) (int, any) { return 200, okFor(body) }
	res, err := e.s.Rollback(ctx, e.p.TenantID, j.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	rb := e.bhandar.callsTo(clients.PathRollback)
	if res.RolledBack != 3 || res.Job.Status != StatusRolledBack || len(rb) < 2 || rb[len(rb)-2].Body["kind"] != "opening_stock" ||
		rb[len(rb)-1].Body["kind"] != "items" {
		t.Fatalf("rollback %+v calls %+v", res, rb)
	}
}

func TestOpenBillsModes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.upload(t, "marg/Outstanding.csv", "open_bills", Params{})
	sum := e.mapAndValidate(t, j, Params{AsOf: "2026-04-01"})
	if sum.Job.ValidRows != 4 || sum.Job.SkippedRows != 1 || sum.Job.ErrorRows != 2 || sum.Job.Recon == nil {
		t.Fatalf("%+v", sum.Job)
	}
	// Today's bahi has no bill-wise endpoint: a clear failure, rows kept.
	e.bahi.handler = func(path string, body map[string]any) (int, any) {
		if path == clients.PathOpenBills {
			return 404, nil
		}
		return 200, okFor(body)
	}
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	j = e.get(t, j.ID)
	if j.Status != StatusFailed || !strings.Contains(*j.Error, "bills_mode=party_totals") || j.Imported != 0 {
		t.Fatalf("%+v", j)
	}
	// party_totals: one opening balance per party, net of its bills.
	sum = e.mapAndValidate(t, j, Params{AsOf: "2026-04-01", BillsMode: BillsPartyTotals})
	dr, err := e.s.DryRunCommit(ctx, e.p.TenantID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dr.Calls) != 1 || dr.Calls[0].Path != clients.PathOpeningBalances || dr.Calls[0].Records != 3 {
		t.Fatalf("dry run %+v", dr.Calls)
	}
	if _, err := e.s.Commit(ctx, e.p.TenantID, j.ID); err != nil {
		t.Fatal(err)
	}
	calls := e.bahi.callsTo(clients.PathOpeningBalances)
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	lines := records(calls[0].Body)
	l0 := lines[0].(map[string]any)
	if len(lines) != 3 || l0["party_external_ref"] != "C0001" || l0["amount_paise"] != float64(1245000) || l0["type"] != "dr" || l0["external_ref"] != nil {
		t.Fatalf("lines %v", lines)
	}
	if j = e.get(t, j.ID); j.Status != StatusCompleted || j.Imported != 4 {
		t.Fatalf("%+v", j)
	}
}

func TestTallyXMLAndTemplates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.upload(t, "tally/Master-and-Vouchers.xml", "invoices", Params{})
	if j.Source != "tally" {
		t.Fatalf("source %q", j.Source)
	}
	pv, err := e.s.Preview(ctx, e.p.TenantID, j.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Sheet != "Vouchers" || pv.Source != "tally" || len(pv.Rows) != 4 {
		t.Fatalf("preview %+v", pv)
	}
	if _, err := e.s.SetMapping(ctx, e.p, j.ID, MappingInput{Mapping: pv.SuggestedMapping, Params: &Params{AsOf: "2026-04-01"},
		SaveAs: "Tally vouchers"}); err != nil {
		t.Fatal(err)
	}
	sum, err := e.s.Validate(ctx, e.p, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Job.ValidRows != 4 {
		t.Fatalf("%+v %+v", sum.Job, sum.SampleErrors)
	}
	// Invoice history needs a bahi endpoint that does not exist yet.
	e.bahi.handler = func(string, map[string]any) (int, any) { return 404, nil }
	_, _ = e.s.Commit(ctx, e.p.TenantID, j.ID)
	if j = e.get(t, j.ID); j.Status != StatusFailed || !strings.Contains(*j.Error, "invoice-history") {
		t.Fatalf("%+v", j)
	}
	inv := records(e.bahi.callsTo(clients.PathInvoiceHistory)[0].Body)[0].(map[string]any)
	if inv["doc_type"] != "sale" || inv["total_paise"] != float64(1180000) || inv["external_ref"] != "inv:sale:GT/2627/0001:2026-04-01" {
		t.Fatalf("invoice payload %v", inv)
	}

	// The saved template is offered (and matches) on the next Tally upload.
	tpls, _ := e.s.ListTemplates(ctx, e.p.TenantID, "invoices")
	if len(tpls) != 1 || tpls[0].Name != "Tally vouchers" || tpls[0].Params.AsOf != "" {
		t.Fatalf("templates %+v", tpls)
	}
	j2 := e.upload(t, "tally/Master-and-Vouchers.xml", "invoices", Params{})
	pv2, _ := e.s.Preview(ctx, e.p.TenantID, j2.ID, "")
	if len(pv2.Templates) != 1 || !pv2.Templates[0].Matches {
		t.Fatalf("%+v", pv2.Templates)
	}
	j2b, err := e.s.SetMapping(ctx, e.p, j2.ID, MappingInput{TemplateID: tpls[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if j2b.TemplateID == nil || j2b.Mapping["total"] != "Total" {
		t.Fatalf("%+v", j2b)
	}
	// A template of another kind is refused; delete works.
	jp := e.upload(t, "tally/Master-and-Vouchers.xml", "parties", Params{})
	if _, err := e.s.SetMapping(ctx, e.p, jp.ID, MappingInput{TemplateID: tpls[0].ID}); err == nil {
		t.Fatal("template kind mismatch must fail")
	}
	if err := e.s.DeleteTemplate(ctx, e.p.TenantID, tpls[0].ID); err != nil {
		t.Fatal(err)
	}
	// Discarding a job that never reached bahi.
	res, err := e.s.Rollback(ctx, e.p.TenantID, jp.ID, 0)
	if err != nil || res.Job.Status != StatusRolledBack {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestMaintainReleasesStaleJobs(t *testing.T) {
	e := newEnv(t)
	j := e.upload(t, "marg/PartyMaster.csv", "parties", Params{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE import_jobs SET status='queued', updated_at=now() - interval '2 hours' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.s.Maintain(ctx, 10*time.Millisecond, 15*time.Minute); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.get(t, j.ID).Status != StatusFailed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if j = e.get(t, j.ID); j.Status != StatusFailed || !strings.Contains(*j.Error, "never started") {
		t.Fatalf("%+v", j)
	}
}
