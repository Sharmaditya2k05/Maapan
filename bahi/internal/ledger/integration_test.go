package ledger

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
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/bahi/internal/posting"
	"github.com/taraju/bahi/migrations"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// testNow is 15:30 IST on 2026-10-23: "today" is 2026-10-23, FY 26-27.
var testNow = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

// newTestPool connects to TEST_DATABASE_URL and returns a pool whose
// search_path is a fresh schema, dropped when the test ends. Skips when the
// URL is unset.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://taraju:taraju@localhost:5432/taraju_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("bahi_test_%d", rand.Int63())
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
	return pool
}

// newTestService runs bahi's migrations in a fresh schema and returns a Service.
func newTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	if err := db.Migrate(context.Background(), pool, migrations.FS, slog.Default()); err != nil {
		t.Fatal(err)
	}
	return &Service{DB: pool, Log: slog.Default(), Now: func() time.Time { return testNow }}, pool
}

func envelope(t *testing.T, typ, tenantID, storeID string, data any) events.Envelope {
	t.Helper()
	e, err := events.New(typ, "test", tenantID, storeID, data)
	if err != nil {
		t.Fatal(err)
	}
	e.OccurredAt = testNow
	return e
}

func seedTenant(t *testing.T, s *Service, tenantID, storeID string) {
	t.Helper()
	if err := s.EventRoutes()[events.TenantCreated](context.Background(),
		envelope(t, events.TenantCreated, tenantID, storeID, events.TenantCreatedPayload{TenantID: tenantID, StoreID: storeID})); err != nil {
		t.Fatal(err)
	}
}

// balance is debit − credit of the tenant's account with code.
func balance(t *testing.T, pool *pgxpool.Pool, tenantID, code string) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(e.debit_paise - e.credit_paise),0) FROM voucher_entries e
		JOIN accounts a ON a.id=e.account_id WHERE a.tenant_id=$1 AND a.code=$2`, tenantID, code).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func partyBalance(t *testing.T, pool *pgxpool.Pool, tenantID, partyID string) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(e.debit_paise - e.credit_paise),0) FROM voucher_entries e
		JOIN parties p ON p.account_id=e.account_id WHERE p.tenant_id=$1 AND p.id=$2`, tenantID, partyID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func accountID(t *testing.T, pool *pgxpool.Pool, tenantID, code string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM accounts WHERE tenant_id=$1 AND code=$2`, tenantID, code).Scan(&id); err != nil {
		t.Fatalf("account %s: %v", code, err)
	}
	return id
}

func countVouchers(t *testing.T, pool *pgxpool.Pool, tenantID, source string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM vouchers WHERE tenant_id=$1 AND source=$2`, tenantID, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// quarantined returns the reason and resolution of a quarantined event.
func quarantined(t *testing.T, pool *pgxpool.Pool, eventID string) (reason string, resolved, found bool) {
	t.Helper()
	var at *time.Time
	err := pool.QueryRow(context.Background(), `SELECT COALESCE(reason,''), resolved_at FROM quarantined_events WHERE event_id=$1`, eventID).Scan(&reason, &at)
	if db.IsNotFound(err) {
		return "", false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return reason, at != nil, true
}

func attempts(t *testing.T, pool *pgxpool.Pool, eventID string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `SELECT attempts FROM event_attempts WHERE event_id=$1`, eventID).Scan(&n)
	if db.IsNotFound(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newParty(t *testing.T, s *Service, tenantID string, in PartyInput) string {
	t.Helper()
	id, err := tryParty(s, tenantID, in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func tryParty(s *Service, tenantID string, in PartyInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	var id string
	err := s.WithTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		id, err = s.createParty(context.Background(), tx, tenantID, "u1", in, nil)
		return err
	})
	return id, err
}

// partyWithID inserts a customer whose id is chosen by the test (events from
// aadesh carry party ids bahi must already know).
func partyWithID(t *testing.T, pool *pgxpool.Pool, tenantID, partyID, name string) {
	t.Helper()
	ctx := context.Background()
	var acct string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (tenant_id, code, name, grp, parent_id)
		SELECT $1, $2, $3, grp, id FROM accounts WHERE tenant_id=$1 AND code=$4 RETURNING id`,
		tenantID, "P-"+partyID[:8], name, posting.SundryDebtors).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO parties (id, tenant_id, account_id, name, type) VALUES ($1,$2,$3,$4,'customer')`,
		partyID, tenantID, acct, name); err != nil {
		t.Fatal(err)
	}
}

func trialBalanced(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	var dr, cr int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(debit_paise),0), COALESCE(sum(credit_paise),0)
		FROM voucher_entries WHERE tenant_id=$1`, tenantID).Scan(&dr, &cr); err != nil {
		t.Fatal(err)
	}
	if dr != cr {
		t.Fatalf("trial balance off: debit %d credit %d", dr, cr)
	}
}

// api drives the HTTP layer with a super_admin token (and the service secret
// for /internal routes).
type api struct {
	t   *testing.T
	srv *httptest.Server
	tok string
}

const testServiceSecret = "svc-secret"

func newAPI(t *testing.T, s *Service, tenantID, storeID string) *api {
	t.Helper()
	jwtSecret := "test-jwt-secret-that-is-32-chars-long!!"
	mux := http.NewServeMux()
	s.Routes(mux, auth.NewVerifier(jwtSecret, testServiceSecret), false)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tok, err := auth.Issue([]byte(jwtSecret), auth.Claims{TenantID: tenantID, StoreID: storeID, Role: auth.RoleSuperAdmin,
		RegisteredClaims: auth.RegisteredClaimsFor("u1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &api{t: t, srv: srv, tok: tok}
}

// do sends body as JSON and decodes the (object) response.
func (a *api) do(method, path string, body any, headers map[string]string) (int, map[string]any) {
	a.t.Helper()
	var rdr bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&rdr).Encode(body); err != nil {
			a.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, a.srv.URL+path, &rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.tok)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	if res.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			a.t.Fatalf("%s %s: decode %d response: %v", method, path, res.StatusCode, err)
		}
	}
	return res.StatusCode, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestPostIsIdempotentUnderConcurrentDelivery(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	seedTenant(t, s, tenant, uuid.NewString())
	v := posting.Voucher{Type: posting.VJournal, Date: "2026-10-20", Source: "test", SourceID: "doc-1", Narration: "same document, many deliveries",
		Entries: []posting.Entry{{AccountCode: posting.Cash, Debit: 100}, {AccountCode: posting.Capital, Credit: 100}}}

	const n = 8
	var wg sync.WaitGroup
	results := make(chan Posted, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var p Posted
			err := s.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				p, err = Post(ctx, tx, tenant, "u1", v)
				return err
			})
			if err != nil {
				errs <- err
				return
			}
			results <- p
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent post failed: %v", err)
	}
	var ids = map[string]bool{}
	created := 0
	for p := range results {
		ids[p.ID] = true
		if p.Created {
			created++
		}
	}
	if len(ids) != 1 || created != 1 {
		t.Fatalf("want exactly one voucher created once, got %d ids, %d created", len(ids), created)
	}
	if countVouchers(t, pool, tenant, "test") != 1 {
		t.Fatal("duplicate voucher rows")
	}
}

func TestInvoicePaymentSettlementFlow(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedTenant(t, s, tenant, store)
	party := newParty(t, s, tenant, PartyInput{Name: "Ramesh Traders", Type: "customer", CreditDays: 15})

	// Sale ₹1180: ₹180 cash at the counter, ₹1000 through a lenden UPI QR → the QR part stays on the party.
	inv := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale", Number: "INV/26-27/00001", InvoiceDate: "2026-10-20",
		PartyID: party, PartyName: "Ramesh Traders", TaxablePaise: 100000, CGSTPaise: 9000, SGSTPaise: 9000, GrandTotalPaise: 118000,
		Tenders: []events.Tender{{Mode: "cash", AmountPaise: 18000}, {Mode: "upi", AmountPaise: 100000, PaymentRequestID: "pr-1"}}}
	must(router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, inv)))
	if b := balance(t, pool, tenant, posting.Cash); b != 18000 {
		t.Fatalf("cash %d", b)
	}
	if b := balance(t, pool, tenant, posting.Bank); b != 0 {
		t.Fatalf("a UPI tender behind a payment request must not hit Bank on the sale: %d", b)
	}
	if b := partyBalance(t, pool, tenant, party); b != 100000 {
		t.Fatalf("party owes the QR part until lenden confirms: %d", b)
	}

	// lenden captures the QR payment → Dr Gateway Clearing, Cr party. Redeliveries (same id, new id) change nothing.
	capture := events.PaymentPayload{PaymentID: "pay-1", PaymentRequestID: "pr-1", InvoiceID: inv.InvoiceID, PartyID: party,
		AmountPaise: 100000, Method: "upi", Gateway: "razorpay", GatewayRef: "pay_X", CapturedAt: "2026-10-20T06:00:00Z"}
	capEnv := envelope(t, events.PaymentCaptured, tenant, store, capture)
	must(router[events.PaymentCaptured](ctx, capEnv))
	must(router[events.PaymentCaptured](ctx, capEnv))
	must(router[events.PaymentCaptured](ctx, envelope(t, events.PaymentCaptured, tenant, store, capture)))
	if b := balance(t, pool, tenant, posting.GatewayClearing); b != 100000 {
		t.Fatalf("clearing %d", b)
	}
	if b := partyBalance(t, pool, tenant, party); b != 0 {
		t.Fatalf("party after capture %d", b)
	}
	if n := countVouchers(t, pool, tenant, posting.SrcPayment); n != 1 {
		t.Fatalf("receipt vouchers %d", n)
	}

	// Two partial refunds of one payment are two vouchers.
	for _, rf := range []string{"rf-1", "rf-2"} {
		refund := capture
		refund.RefundID, refund.AmountPaise = rf, 10000
		must(router[events.PaymentRefunded](ctx, envelope(t, events.PaymentRefunded, tenant, store, refund)))
	}
	if n := countVouchers(t, pool, tenant, posting.SrcRefund); n != 2 {
		t.Fatalf("refund vouchers %d (second partial refund dropped?)", n)
	}
	if b := balance(t, pool, tenant, posting.GatewayClearing); b != 80000 {
		t.Fatalf("clearing after refunds %d", b)
	}
	if b := partyBalance(t, pool, tenant, party); b != 20000 {
		t.Fatalf("party after refunds %d", b)
	}

	// Settlement: gross 1000 − refunds 200 = 800 = net 782.30 + fee 15 + GST 2.70.
	settle := events.SettlementPayload{SettlementID: "setl-1", Gateway: "razorpay", GatewayRef: "setl_X", UTR: "UTR1",
		GrossPaise: 100000, RefundsPaise: 20000, FeePaise: 1500, FeeTaxPaise: 270, NetPaise: 78230, SettledAt: "2026-10-22T10:00:00Z"}
	must(router[events.SettlementProcessed](ctx, envelope(t, events.SettlementProcessed, tenant, store, settle)))
	must(router[events.SettlementProcessed](ctx, envelope(t, events.SettlementProcessed, tenant, store, settle)))
	for code, want := range map[string]int64{posting.GatewayClearing: 0, posting.Bank: 78230, posting.BankCharges: 1500,
		posting.InputCGST: 135, posting.InputSGST: 135} {
		if b := balance(t, pool, tenant, code); b != want {
			t.Errorf("%s = %d, want %d", code, b, want)
		}
	}
	if n := countVouchers(t, pool, tenant, posting.SrcSettlement); n != 1 {
		t.Fatalf("settlement vouchers %d", n)
	}
	var vdate time.Time
	must(pool.QueryRow(ctx, `SELECT vdate FROM vouchers WHERE tenant_id=$1 AND source=$2`, tenant, posting.SrcSettlement).Scan(&vdate))
	if vdate.Format(time.DateOnly) != "2026-10-22" {
		t.Fatalf("settlement dated %s", vdate.Format(time.DateOnly))
	}

	// A settlement whose numbers do not add up is quarantined and acknowledged.
	bad := settle
	bad.SettlementID, bad.NetPaise = "setl-2", 1
	badEnv := envelope(t, events.SettlementProcessed, tenant, store, bad)
	must(router[events.SettlementProcessed](ctx, badEnv))
	reason, resolved, found := quarantined(t, pool, badEnv.ID)
	if !found || resolved || !strings.Contains(reason, "does not balance") {
		t.Fatalf("quarantine: found=%v resolved=%v reason=%q", found, resolved, reason)
	}
	if b := balance(t, pool, tenant, posting.Bank); b != 78230 {
		t.Fatal("quarantined settlement must not post")
	}
	trialBalanced(t, pool, tenant)
}

func TestStockMovedPostsCOGS(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedTenant(t, s, tenant, store)
	moved := func(source, sourceID, reason string, value int64, qty float64, reversal bool) events.Envelope {
		return envelope(t, events.StockMoved, tenant, store, events.StockMovedPayload{StoreID: store, Source: source, SourceID: sourceID,
			Reason: reason, ValuePaise: value, Reversal: reversal, OccurredAt: "2026-10-20T05:00:00Z",
			Movements: []events.StockMovement{{MovementID: uuid.NewString(), ItemID: "i1", Qty: qty, UnitCostPaise: 820, ValuePaise: value, Reason: reason}}})
	}

	// Purchase bill ₹787.20 (no tax, cash supplier) + its stock movement: Purchases nets to zero, Inventory carries the cost.
	must(router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, events.InvoicePayload{InvoiceID: "pur-1", StoreID: store,
		DocType: "purchase", Number: "PUR/1", InvoiceDate: "2026-10-20", PartyName: "Local wholesaler", TaxablePaise: 78720, GrandTotalPaise: 78720})))
	must(router[events.StockMoved](ctx, moved("aadesh.invoice", "pur-1", "purchase", 78720, 96, false)))
	if b := balance(t, pool, tenant, posting.Inventory); b != 78720 {
		t.Fatalf("inventory %d", b)
	}
	if b := balance(t, pool, tenant, posting.Purchases); b != 0 {
		t.Fatalf("purchases of stock-tracked goods must net to zero: %d", b)
	}

	// Cash sale of 10 pcs for ₹100 (no tax) → Sales 10000; stock.moved → COGS 8200. A redelivery with a new id is a no-op.
	must(router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, events.InvoicePayload{InvoiceID: "inv-1", StoreID: store,
		DocType: "sale", Number: "INV/1", InvoiceDate: "2026-10-20", PartyName: "Walk-in", TaxablePaise: 10000, GrandTotalPaise: 10000,
		Tenders: []events.Tender{{Mode: "cash", AmountPaise: 10000}}})))
	must(router[events.StockMoved](ctx, moved("aadesh.invoice", "inv-1", "sale", -8200, -10, false)))
	must(router[events.StockMoved](ctx, moved("aadesh.invoice", "inv-1", "sale", -8200, -10, false)))
	if b := balance(t, pool, tenant, posting.COGS); b != 8200 {
		t.Fatalf("cogs %d", b)
	}
	if b := balance(t, pool, tenant, posting.Inventory); b != 70520 {
		t.Fatalf("inventory after sale %d", b)
	}
	pl, err := s.profitLoss(ctx, tenant, "2026-04-01", "2026-10-23")
	must(err)
	if pl.GrossProfitPaise != 1800 || pl.NetProfitPaise != 1800 || pl.TotalExpenses != 8200 {
		t.Fatalf("P&L gross %d net %d expenses %d (COGS must count as a direct expense)", pl.GrossProfitPaise, pl.NetProfitPaise, pl.TotalExpenses)
	}

	// Cancellation reverses the cost; transfers post nothing; a write-off is a stock loss.
	must(router[events.StockMoved](ctx, moved("aadesh.cancel", "inv-1", "sale", 8200, 10, true)))
	if b := balance(t, pool, tenant, posting.COGS); b != 0 {
		t.Fatalf("cogs after cancel %d", b)
	}
	must(router[events.StockMoved](ctx, moved("transfer", "tr-1", "transfer_out", -1640, -2, false)))
	must(router[events.StockMoved](ctx, moved("adjustment", "adj-1", "expiry_writeoff", -4100, -5, false)))
	if n := countVouchers(t, pool, tenant, posting.SrcStockMoved); n != 4 {
		t.Fatalf("stock vouchers %d (transfer must not post)", n)
	}
	if b := balance(t, pool, tenant, posting.StockLoss); b != 4100 {
		t.Fatalf("stock loss %d", b)
	}
	if b := balance(t, pool, tenant, posting.Inventory); b != 74620 {
		t.Fatalf("inventory at end %d", b)
	}
	// Posting to the groups themselves is refused; their statements include the children.
	ids, err := s.descendants(ctx, tenant, accountID(t, pool, tenant, posting.DirectExpenses))
	must(err)
	if len(ids) != 3 {
		t.Fatalf("Direct Expenses group leaves: %d", len(ids))
	}

	// An unknown reason is quarantined, not dropped.
	badEnv := moved("adjustment", "adj-2", "teleport", -1, -1, false)
	must(router[events.StockMoved](ctx, badEnv))
	if reason, _, found := quarantined(t, pool, badEnv.ID); !found || !strings.Contains(reason, "teleport") {
		t.Fatalf("quarantine: %v %q", found, reason)
	}
	trialBalanced(t, pool, tenant)
}

func TestUnknownPartyRetriesThenQuarantinesAndRetryEndpoint(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	seedTenant(t, s, tenant, store)
	ghost := uuid.NewString()
	inv := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale", Number: "INV/26-27/00007", InvoiceDate: "2026-10-21",
		PartyID: ghost, PartyName: "Not yet replicated", TaxablePaise: 100000, CGSTPaise: 9000, SGSTPaise: 9000, GrandTotalPaise: 118000}
	env := envelope(t, events.InvoiceCreated, tenant, store, inv)

	for i := 1; i < maxEventAttempts; i++ {
		if err := router[events.InvoiceCreated](ctx, env); err == nil {
			t.Fatalf("delivery %d must fail so Pub/Sub retries", i)
		}
		if got := attempts(t, pool, env.ID); got != i {
			t.Fatalf("attempts after delivery %d = %d", i, got)
		}
	}
	if err := router[events.InvoiceCreated](ctx, env); err != nil {
		t.Fatalf("delivery %d must quarantine and acknowledge: %v", maxEventAttempts, err)
	}
	reason, resolved, found := quarantined(t, pool, env.ID)
	if !found || resolved || !strings.Contains(reason, "not known") || !strings.Contains(reason, "gave up after 3") {
		t.Fatalf("quarantine: found=%v resolved=%v reason=%q", found, resolved, reason)
	}
	if balance(t, pool, tenant, posting.WalkInCustomers) != 0 || countVouchers(t, pool, tenant, posting.SrcInvoice) != 0 {
		t.Fatal("an unknown party must never be re-pointed at Walk-in Customers")
	}

	a := newAPI(t, s, tenant, store)
	st, body := a.do("GET", "/v1/quarantine", nil, nil)
	if st != 200 || body["total"] != float64(1) {
		t.Fatalf("list: %d %v", st, body)
	}
	items := body["items"].([]any)
	if items[0].(map[string]any)["event_id"] != env.ID {
		t.Fatalf("list items %v", items)
	}

	// Retrying before the party exists fails again and leaves it quarantined.
	if st, body = a.do("POST", "/v1/quarantine/"+env.ID+"/retry", nil, nil); st != 409 {
		t.Fatalf("retry without party: %d %v", st, body)
	}
	if _, resolved, _ := quarantined(t, pool, env.ID); resolved {
		t.Fatal("failed retry must leave the event unresolved")
	}

	// Once the party is replicated the retry posts the voucher and resolves the row.
	partyWithID(t, pool, tenant, ghost, "Now replicated")
	if st, body = a.do("POST", "/v1/quarantine/"+env.ID+"/retry", nil, nil); st != 200 || body["resolved_at"] == nil {
		t.Fatalf("retry: %d %v", st, body)
	}
	if b := partyBalance(t, pool, tenant, ghost); b != 118000 {
		t.Fatalf("receivable reached the party: %d", b)
	}
	if st, body = a.do("GET", "/v1/quarantine", nil, nil); st != 200 || body["total"] != float64(0) {
		t.Fatalf("list after retry: %d %v", st, body)
	}
	if st, _ = a.do("POST", "/v1/quarantine/"+env.ID+"/retry", nil, nil); st != 409 {
		t.Fatalf("retrying a resolved event: %d", st)
	}
	if st, _ = a.do("POST", "/v1/quarantine/nope/retry", nil, nil); st != 404 {
		t.Fatalf("unknown event: %d", st)
	}
	// Permanent failures (an invoice that cannot balance) are quarantined on the first delivery.
	broken := inv
	broken.InvoiceID, broken.GrandTotalPaise = uuid.NewString(), 118001
	brokenEnv := envelope(t, events.InvoiceCreated, tenant, store, broken)
	if err := router[events.InvoiceCreated](ctx, brokenEnv); err != nil {
		t.Fatal(err)
	}
	if reason, _, found := quarantined(t, pool, brokenEnv.ID); !found || !strings.Contains(reason, "does not balance") {
		t.Fatalf("broken invoice: %v %q", found, reason)
	}
}

func TestPeriodLock(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)

	if st, body := a.do("GET", "/v1/settings/period-lock", nil, nil); st != 200 || body["locked_until"] != "" {
		t.Fatalf("no lock yet: %d %v", st, body)
	}
	if st, _ := a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": "2026-13-01"}, nil); st != 422 {
		t.Fatalf("bad date: %d", st)
	}
	if st, _ := a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": "2027-01-01"}, nil); st != 422 {
		t.Fatalf("future lock: %d", st)
	}
	st, body := a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": "2026-09-30"}, nil)
	if st != 200 || body["locked_until"] != "2026-09-30" || body["locked_by"] != "u1" {
		t.Fatalf("lock: %d %v", st, body)
	}

	cash, capital, bank := accountID(t, pool, tenant, posting.Cash), accountID(t, pool, tenant, posting.Capital), accountID(t, pool, tenant, posting.Bank)
	journal := func(date string) map[string]any {
		return map[string]any{"type": "journal", "date": date, "narration": "late entry",
			"entries": []map[string]any{{"account_id": cash, "debit_paise": 100}, {"account_id": capital, "credit_paise": 100}}}
	}
	if st, body = a.do("POST", "/v1/vouchers", journal("2026-09-30"), nil); st != 409 || errCode(body) != ErrCodePeriodLocked {
		t.Fatalf("journal inside the lock: %d %v", st, body)
	}
	if st, body = a.do("POST", "/v1/vouchers", journal("2026-10-01"), nil); st != 201 {
		t.Fatalf("journal after the lock: %d %v", st, body)
	}
	if st, body = a.do("POST", "/v1/bank/transactions", map[string]any{"bank_account_id": bank, "kind": "deposit", "amount_paise": 500,
		"date": "2026-09-15"}, nil); st != 409 || errCode(body) != ErrCodePeriodLocked {
		t.Fatalf("bank txn inside the lock: %d %v", st, body)
	}

	// An event dated inside the lock is quarantined with reason "period locked", not posted and not retried.
	router := s.EventRoutes()
	inv := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale", Number: "INV/26-27/00002", InvoiceDate: "2026-09-15",
		PartyName: "Walk-in", TaxablePaise: 10000, GrandTotalPaise: 10000, Tenders: []events.Tender{{Mode: "cash", AmountPaise: 10000}}}
	env := envelope(t, events.InvoiceCreated, tenant, store, inv)
	if err := router[events.InvoiceCreated](ctx, env); err != nil {
		t.Fatal(err)
	}
	reason, resolved, found := quarantined(t, pool, env.ID)
	if !found || resolved || !strings.HasPrefix(reason, "period locked") {
		t.Fatalf("quarantine: found=%v resolved=%v reason=%q", found, resolved, reason)
	}
	if countVouchers(t, pool, tenant, posting.SrcInvoice) != 0 {
		t.Fatal("locked invoice must not post")
	}

	// Unlock, retry → posted.
	if st, body = a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": ""}, nil); st != 200 || body["locked_until"] != "" {
		t.Fatalf("unlock: %d %v", st, body)
	}
	if st, body = a.do("POST", "/v1/quarantine/"+env.ID+"/retry", nil, nil); st != 200 {
		t.Fatalf("retry after unlock: %d %v", st, body)
	}
	if countVouchers(t, pool, tenant, posting.SrcInvoice) != 1 {
		t.Fatal("retried invoice must post")
	}
}

func TestReverseMarksOriginalNotTheReversal(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	seedTenant(t, s, tenant, store)
	party := newParty(t, s, tenant, PartyInput{Name: "P", Type: "customer"})
	post := func(v posting.Voucher) Posted {
		t.Helper()
		var p Posted
		if err := s.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			p, err = Post(ctx, tx, tenant, "u1", v)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orig := post(posting.Voucher{Type: posting.VReceipt, Date: "2026-10-10", Narration: "cash from P", Source: posting.SrcManual,
		Entries: []posting.Entry{{AccountCode: posting.Cash, Debit: 700}, {PartyID: party, Credit: 700}}})
	var rev Posted
	if err := s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		rev, err = Reverse(ctx, tx, tenant, "u1", orig.ID, "2026-10-23", posting.SrcReversal, orig.ID, "keyed twice")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o, err := s.getVoucher(ctx, tenant, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.getVoucher(ctx, tenant, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "reversed" || o.ReversedBy != rev.ID || o.ReversalOf != "" {
		t.Fatalf("original: %+v", o)
	}
	if r.Status != "posted" || r.ReversalOf != orig.ID || r.ReversedBy != "" || r.Type != posting.VReceipt {
		t.Fatalf("reversal: %+v", r)
	}
	if b := partyBalance(t, pool, tenant, party); b != 0 {
		t.Fatalf("party after reversal %d", b)
	}
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := Reverse(ctx, tx, tenant, "u1", orig.ID, "2026-10-23", posting.SrcReversal, orig.ID+"-again", "twice")
		return err
	})
	if httpx.AsError(err).Status != 409 {
		t.Fatalf("reversing twice: %v", err)
	}
	// Reversing the reversal is not possible through the API, but Reverse itself
	// must still accept a posted voucher: the reversal is posted, the original is not.
	if _, err := s.getVoucher(ctx, tenant, rev.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOpeningBalanceDefaultsToFinancialYearStart(t *testing.T) {
	s, pool := newTestService(t)
	tenant := uuid.NewString()
	id := newParty(t, s, tenant, PartyInput{Name: "Opening Co", Type: "customer", OpeningBalancePaise: 50000, OpeningType: "dr"})
	var vdate time.Time
	if err := pool.QueryRow(context.Background(), `SELECT vdate FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_id=$3`,
		tenant, posting.SrcOpening, "party:"+id).Scan(&vdate); err != nil {
		t.Fatal(err)
	}
	if got := vdate.Format(time.DateOnly); got != "2026-04-01" {
		t.Fatalf("opening dated %s, want the FY start", got)
	}
	if _, err := tryParty(s, tenant, PartyInput{Name: "Bad", Type: "customer", OpeningBalancePaise: 1, OpeningType: "cr", OpeningAsOf: "01/04/2026"}); httpx.AsError(err).Status != 422 {
		t.Fatalf("malformed opening_as_of: %v", err)
	}
	if _, err := tryParty(s, tenant, PartyInput{Name: "Explicit", Type: "supplier", OpeningBalancePaise: 1, OpeningType: "cr", OpeningAsOf: "2026-04-01"}); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, pool, tenant, posting.OpeningDiff); b != -50000+1 {
		t.Fatalf("opening difference %d", b)
	}
}

func TestInternalPartyCarriesCreditTerms(t *testing.T) {
	s, _ := newTestService(t)
	tenant, store := uuid.NewString(), uuid.NewString()
	id := newParty(t, s, tenant, PartyInput{Name: "Terms Ltd", Type: "customer", CreditDays: 30, CreditLimitPaise: 500000})
	a := newAPI(t, s, tenant, store)
	st, body := a.do("GET", "/internal/v1/parties/"+id, nil, map[string]string{auth.HeaderServiceSecret: testServiceSecret, auth.HeaderTenantID: tenant})
	if st != 200 || body["credit_days"] != float64(30) || body["credit_limit_paise"] != float64(500000) || body["name"] != "Terms Ltd" {
		t.Fatalf("internal party: %d %v", st, body)
	}
}

// TestMigrationUpgradesExistingTenant applies 0001 only, builds a tenant the way
// the old seed did (5500/5200 postable, no clearing/COGS accounts, a reversal
// pair marked the old way), then applies the rest and checks the repairs.
func TestMigrationUpgradesExistingTenant(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	first, err := fs.ReadFile(migrations.FS, "0001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, fstest.MapFS{"0001_init.up.sql": {Data: first}}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.NewString()
	added := map[string]bool{posting.ChequesInHand: true, posting.GatewayClearing: true, posting.StockGain: true, posting.COGS: true,
		posting.StockLoss: true, posting.OtherDirectExpenses: true, posting.OtherIndirectExpenses: true}
	for _, a := range posting.DefaultChart {
		if added[a.Code] {
			continue
		}
		isGroup := a.IsGroup && a.Code != posting.IndirectExpenses && a.Code != posting.DirectExpenses
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_system, is_cash, is_bank)
			VALUES ($1,$2,$3,$4,(SELECT id FROM accounts WHERE tenant_id=$1 AND code=NULLIF($5,'')),$6,true,$7,$8)`,
			tenant, a.Code, a.Name, a.Group, a.Parent, isGroup, a.IsCash, a.IsBank); err != nil {
			t.Fatal(err)
		}
	}
	voucher := func(number, status string, reversalOf *string, total int64, drCode, crCode string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO vouchers (tenant_id, vtype, number, fy, vdate, source, status, reversal_of, total_paise, created_by)
			VALUES ($1,'journal',$2,'26-27','2026-09-01','manual',$3,$4,$5,'u1') RETURNING id`, tenant, number, status, reversalOf, total).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO voucher_entries (tenant_id, voucher_id, line_no, account_id, vdate, debit_paise, credit_paise) VALUES
			($1,$2,1,(SELECT id FROM accounts WHERE tenant_id=$1 AND code=$3),'2026-09-01',$5,0),
			($1,$2,2,(SELECT id FROM accounts WHERE tenant_id=$1 AND code=$4),'2026-09-01',0,$5)`, tenant, id, drCode, crCode, total); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Numbers outside the JV/26-27 series Post allocates from.
	direct := voucher("OLD/00001", "posted", nil, 700, posting.IndirectExpenses, posting.Cash)
	orig := voucher("OLD/00002", "reversed", nil, 500, posting.Cash, posting.Capital)
	rev := voucher("OLD/00003", "reversed", &orig, 500, posting.Capital, posting.Cash)

	if err := db.Migrate(ctx, pool, migrations.FS, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, migrations.FS, slog.Default()); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}

	var isGroup bool
	for _, code := range []string{posting.IndirectExpenses, posting.DirectExpenses} {
		if err := pool.QueryRow(ctx, `SELECT is_group FROM accounts WHERE tenant_id=$1 AND code=$2`, tenant, code).Scan(&isGroup); err != nil || !isGroup {
			t.Fatalf("%s is_group=%v err=%v", code, isGroup, err)
		}
	}
	if b := balance(t, pool, tenant, posting.IndirectExpenses); b != 0 {
		t.Fatalf("entries left on 5500: %d", b)
	}
	if b := balance(t, pool, tenant, posting.OtherIndirectExpenses); b != 700 {
		t.Fatalf("entries moved to 5590: %d", b)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM voucher_entries WHERE voucher_id=$1`, direct).Scan(&n); err != nil || n != 2 {
		t.Fatalf("moved voucher still has %d entries (%v)", n, err)
	}
	for code, parent := range map[string]string{posting.GatewayClearing: posting.GroupAssets, posting.ChequesInHand: posting.GroupAssets,
		posting.COGS: posting.DirectExpenses, posting.StockLoss: posting.DirectExpenses, posting.OtherDirectExpenses: posting.DirectExpenses,
		posting.StockGain: posting.GroupIncome, posting.OtherIndirectExpenses: posting.IndirectExpenses} {
		var got string
		var system bool
		if err := pool.QueryRow(ctx, `SELECT p.code, a.is_system FROM accounts a JOIN accounts p ON p.id=a.parent_id WHERE a.tenant_id=$1 AND a.code=$2`,
			tenant, code).Scan(&got, &system); err != nil || got != parent || !system {
			t.Fatalf("%s: parent %q system=%v err=%v", code, got, system, err)
		}
	}
	var status string
	var reversedBy *string
	if err := pool.QueryRow(ctx, `SELECT status, reversed_by::text FROM vouchers WHERE id=$1`, orig).Scan(&status, &reversedBy); err != nil ||
		status != "reversed" || reversedBy == nil || *reversedBy != rev {
		t.Fatalf("original after repair: %s %v %v", status, reversedBy, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM vouchers WHERE id=$1`, rev).Scan(&status); err != nil || status != "posted" {
		t.Fatalf("reversal after repair: %s %v", status, err)
	}

	// The upgraded tenant posts to the new accounts without re-seeding.
	s := &Service{DB: pool, Log: slog.Default(), Now: func() time.Time { return testNow }}
	if err := s.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, tenant, "u1", posting.Voucher{Type: posting.VJournal, Date: "2026-10-23", Source: "test", SourceID: "x",
			Entries: []posting.Entry{{AccountCode: posting.COGS, Debit: 10}, {AccountCode: posting.Inventory, Credit: 10}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, tenant, "u1", posting.Voucher{Type: posting.VJournal, Date: "2026-10-23",
			Entries: []posting.Entry{{AccountCode: posting.IndirectExpenses, Debit: 10}, {AccountCode: posting.Cash, Credit: 10}}})
		return err
	}); httpx.AsError(err).Status != 422 {
		t.Fatalf("posting to the 5500 group must be refused: %v", err)
	}
}
