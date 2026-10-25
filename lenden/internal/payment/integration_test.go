package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/lenden/internal/creds"
	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/lenden/migrations"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// fakeGW is a controllable gateway named "razorpay" (so Razorpay webhooks apply to it).
type fakeGW struct {
	keyID string // set when built from tenant credentials

	mu           sync.Mutex
	refundErr    error
	refundStatus string // answer when refundErr is nil
	refundCalls  int
	status       map[string]gateway.RemoteStatus   // gateway_ref → what FetchQR/FetchLink answer
	fetchErr     map[string]error                  // gateway_ref → fetch error
	refunds      map[string][]gateway.RemoteRefund // payment_ref → FetchRefunds answer
}

func newFakeGW() *fakeGW {
	return &fakeGW{refundStatus: "processed", status: map[string]gateway.RemoteStatus{}, fetchErr: map[string]error{},
		refunds: map[string][]gateway.RemoteRefund{}}
}

func (g *fakeGW) Name() string { return "razorpay" }
func (g *fakeGW) CreateQR(_ context.Context, q gateway.QRRequest) (gateway.Created, error) {
	return gateway.Created{GatewayRef: "qr_" + q.RequestID[:8], UPIIntent: "upi://pay?pa=x"}, nil
}
func (g *fakeGW) CreateLink(_ context.Context, l gateway.LinkRequest) (gateway.Created, error) {
	return gateway.Created{GatewayRef: "plink_" + l.RequestID[:8], ShortURL: "https://rzp.io/x"}, nil
}
func (g *fakeGW) fetch(ref string) (gateway.RemoteStatus, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fetchErr[ref]; err != nil {
		return gateway.RemoteStatus{}, err
	}
	return g.status[ref], nil
}
func (g *fakeGW) FetchQR(_ context.Context, ref string) (gateway.RemoteStatus, error) {
	return g.fetch(ref)
}
func (g *fakeGW) FetchLink(_ context.Context, ref string) (gateway.RemoteStatus, error) {
	return g.fetch(ref)
}
func (g *fakeGW) Refund(_ context.Context, _ string, _ int64, receipt string) (gateway.Refund, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refundCalls++
	if g.refundErr != nil {
		return gateway.Refund{}, g.refundErr
	}
	return gateway.Refund{RefundRef: "rfnd_" + receipt[:8], Status: g.refundStatus}, nil
}
func (g *fakeGW) FetchRefunds(_ context.Context, payRef string) ([]gateway.RemoteRefund, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateway.RemoteRefund(nil), g.refunds[payRef]...), nil
}

type testEnv struct {
	svc   *Service
	gw    *fakeGW
	pool  *pgxpool.Pool
	store *creds.Store
	hooks *Webhooks
}

// newTestEnv connects to TEST_DATABASE_URL, creates a fresh schema, runs the
// migrations in it and wires a Service with a fake Razorpay. Skips when unset.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("lenden_test_%d", rand.Int63())
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
	if err := db.Migrate(ctx, pool, migrations.FS, slog.Default()); err != nil {
		t.Fatal(err)
	}

	cipher, err := creds.NewCipher(creds.DeriveKey("test-service-secret"))
	if err != nil {
		t.Fatal(err)
	}
	store := &creds.Store{DB: pool, Cipher: cipher}
	gw := newFakeGW()
	reg := gateway.NewRegistry()
	reg.Add(gw, gateway.RazorpayWebhooks{Secret: "whsec"})
	reg.Add(gateway.NewMock(""), nil)
	reg.Enable("razorpay", func(c gateway.Credentials) (gateway.Gateway, error) {
		g := newFakeGW()
		g.keyID = c.KeyID
		return g, nil
	}, nil)
	reg.Enable("cashfree", gateway.CashfreeFactory("", "", "", nil), gateway.CashfreeWebhooks{})
	if err := reg.SetDefault("razorpay"); err != nil {
		t.Fatal(err)
	}
	reg.Creds = store
	ledger := &PGLedger{DB: pool}
	svc := &Service{DB: pool, Gateways: reg, Ledger: ledger, Log: slog.Default(), Creds: store, PayeeName: "Test Store", Now: time.Now}
	hooks := &Webhooks{Source: gateway.RazorpayWebhooks{Secret: "whsec"}, Ledger: ledger, Secrets: store, Log: slog.Default()}
	return &testEnv{svc: svc, gw: gw, pool: pool, store: store, hooks: hooks}
}

func principal(tenantID string) *auth.Principal {
	return &auth.Principal{Internal: true, TenantID: tenantID}
}

func (e *testEnv) request(t *testing.T, tenantID string, amount int64) *Request {
	t.Helper()
	req, err := e.svc.CreateRequest(context.Background(), principal(tenantID), RequestInput{
		InvoiceID: uuid.NewString(), AmountPaise: amount, Purpose: "invoice", Kind: "upi_qr"})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func (e *testEnv) capture(t *testing.T, req *Request, payRef string, amount int64) *Payment {
	t.Helper()
	res, err := e.svc.Ledger.Capture(context.Background(), "evt_"+payRef, []byte(`{}`), Capture{RequestID: req.ID, Gateway: "razorpay",
		PaymentRef: payRef, AmountPaise: amount, Method: "upi", CapturedAt: time.Now()})
	if err != nil || res != Applied {
		t.Fatalf("capture %s: %s %v", payRef, res, err)
	}
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM payments WHERE gateway='razorpay' AND gateway_ref=$1`, payRef).Scan(&id); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.GetPayment(context.Background(), req.tenant(t, e), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (r *Request) tenant(t *testing.T, e *testEnv) string {
	t.Helper()
	var tenant string
	if err := e.pool.QueryRow(context.Background(), `SELECT tenant_id FROM payment_requests WHERE id=$1`, r.ID).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

// payloads returns the decoded PaymentPayloads of every outbox event of a type, oldest first.
func (e *testEnv) payloads(t *testing.T, typ string) []events.PaymentPayload {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT payload FROM outbox WHERE event_type=$1 ORDER BY created_at, id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.PaymentPayload
	for rows.Next() {
		var raw []byte
		var env events.Envelope
		var p events.PaymentPayload
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := decodeEnvelope(raw, &env, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func decodeEnvelope(raw []byte, env *events.Envelope, dst any) error {
	if err := json.Unmarshal(raw, env); err != nil {
		return err
	}
	return env.Decode(dst)
}

func (e *testEnv) refundRow(t *testing.T, id string) (status string, gatewayRef *string, source string) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT status, gateway_ref, source FROM refunds WHERE id=$1`, id).Scan(&status, &gatewayRef, &source); err != nil {
		t.Fatal(err)
	}
	return
}

// Item 5 + 3: short and excess captures, request partial → paid, request id on the event.
func TestCaptureShortExcessAndRequestID(t *testing.T) {
	e := newTestEnv(t)
	tenant := uuid.NewString()
	req := e.request(t, tenant, 10000)

	p1 := e.capture(t, req, "pay_short", 6000)
	if p1.ShortPaise != 4000 || p1.ExcessPaise != 0 {
		t.Fatalf("short capture %+v", p1)
	}
	r, _ := e.svc.GetRequest(context.Background(), tenant, req.ID)
	if r.Status != "partial" || r.PaidPaise != 6000 {
		t.Fatalf("after short capture %+v", r)
	}
	p2 := e.capture(t, req, "pay_excess", 5000)
	if p2.ShortPaise != 0 || p2.ExcessPaise != 1000 {
		t.Fatalf("excess capture %+v", p2)
	}
	r, _ = e.svc.GetRequest(context.Background(), tenant, req.ID)
	if r.Status != "paid" || r.PaidPaise != 11000 {
		t.Fatalf("after excess capture %+v", r)
	}
	evs := e.payloads(t, events.PaymentCaptured)
	if len(evs) != 2 {
		t.Fatalf("want 2 captured events, got %d", len(evs))
	}
	for _, ev := range evs {
		if ev.PaymentRequestID != req.ID || ev.Gateway != "razorpay" || ev.RefundID != "" {
			t.Fatalf("captured payload %+v", ev)
		}
	}
	// The same payment cannot be booked twice (Capture is a no-op, status unchanged).
	res, err := e.svc.Ledger.Capture(context.Background(), "evt_again", []byte(`{}`), Capture{RequestID: req.ID, Gateway: "razorpay",
		PaymentRef: "pay_short", AmountPaise: 6000, Method: "upi", CapturedAt: time.Now()})
	if err != nil || res != Duplicate {
		t.Fatalf("duplicate capture: %s %v", res, err)
	}
}

// Item 4: manual receipts need an idempotency key; a replay returns the first receipt.
func TestManualReceiptIdempotent(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	in := ManualInput{InvoiceID: uuid.NewString(), AmountPaise: 25000, Method: "cheque", Reference: "CHQ 000123", IdempotencyKey: "pos-7-chq-000123"}

	if _, _, err := e.svc.Manual(ctx, principal(tenant), ManualInput{InvoiceID: in.InvoiceID, AmountPaise: 1, Method: "cash"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("missing key must be 422: %v", err)
	}
	first, created, err := e.svc.Manual(ctx, principal(tenant), in)
	if err != nil || !created {
		t.Fatalf("first: %v created=%v", err, created)
	}
	again, created, err := e.svc.Manual(ctx, principal(tenant), in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %v created=%v id=%s/%s", err, created, again.ID, first.ID)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE tenant_id=$1`, tenant).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 payment, got %d", n)
	}
	evs := e.payloads(t, events.PaymentCaptured)
	if len(evs) != 1 || evs[0].Gateway != "manual" || evs[0].Method != "cheque" || evs[0].PaymentID != first.ID {
		t.Fatalf("captured events %+v", evs)
	}
	// Same key, different receipt: refused rather than silently returning another payment.
	diff := in
	diff.AmountPaise = 30000
	if _, _, err := e.svc.Manual(ctx, principal(tenant), diff); httpx.AsError(err).Status != http.StatusConflict {
		t.Fatalf("different body under the same key must be 409: %v", err)
	}
	// Another tenant may reuse the key.
	if _, created, err := e.svc.Manual(ctx, principal(uuid.NewString()), in); err != nil || !created {
		t.Fatalf("other tenant: %v %v", err, created)
	}
}

// Item 2 + 3: refunds lock the payment, over-refunds are 422, each refund event carries its id.
func TestRefundLockingAndEvents(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	req := e.request(t, tenant, 10000)
	pay := e.capture(t, req, "pay_lock", 10000)

	// Two concurrent 60% refunds: exactly one may pass.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = e.svc.Refund(ctx, principal(tenant), pay.ID, RefundInput{AmountPaise: 6000, Reason: "race"})
		}(i)
	}
	wg.Wait()
	okCount := 0
	for _, err := range results {
		if err == nil {
			okCount++
		} else if httpx.AsError(err).Status != http.StatusUnprocessableEntity {
			t.Fatalf("loser must be 422, got %v", err)
		}
	}
	if okCount != 1 {
		t.Fatalf("exactly one concurrent refund must pass, got %d", okCount)
	}
	p, _ := e.svc.GetPayment(ctx, tenant, pay.ID)
	if p.RefundedPaise != 6000 || p.Status != "partially_refunded" {
		t.Fatalf("after race %+v", p)
	}
	if _, err := e.svc.Refund(ctx, principal(tenant), pay.ID, RefundInput{AmountPaise: 5000, Reason: "too much"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("over-refund must be 422: %v", err)
	}
	res, err := e.svc.Refund(ctx, principal(tenant), pay.ID, RefundInput{Reason: "rest"}) // 0 = all refundable
	if err != nil || res.Refund.AmountPaise != 4000 || res.Refund.Status != "processed" || res.Payment.Status != "refunded" || res.RefundedPaise != 10000 {
		t.Fatalf("final refund %+v %v", res, err)
	}
	if e.gw.refundCalls != 2 {
		t.Fatalf("gateway must be called once per accepted refund, got %d", e.gw.refundCalls)
	}
	evs := e.payloads(t, events.PaymentRefunded)
	if len(evs) != 2 || evs[0].RefundID == "" || evs[1].RefundID == "" || evs[0].RefundID == evs[1].RefundID ||
		evs[0].PaymentID != pay.ID || evs[0].AmountPaise+evs[1].AmountPaise != 10000 || evs[1].PaymentRequestID != req.ID {
		t.Fatalf("refunded events %+v", evs)
	}
	// Manual payments refund instantly, and the refund response carries the refund.
	m, _, err := e.svc.Manual(ctx, principal(tenant), ManualInput{PartyID: uuid.NewString(), AmountPaise: 700, Method: "cash", IdempotencyKey: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	mr, err := e.svc.Refund(ctx, principal(tenant), m.ID, RefundInput{AmountPaise: 200, Reason: "change"})
	if err != nil || mr.Refund.Status != "processed" || mr.RefundedPaise != 200 || mr.Refund.Source != "maapan" {
		t.Fatalf("manual refund %+v %v", mr, err)
	}
}

// Item 1: a refund whose gateway call timed out is 'unknown' (not failed); the
// refund.processed webhook promotes it; a definitive 4xx marks it failed and
// frees the amount.
func TestRefundUnknownThenWebhook(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	req := e.request(t, tenant, 10000)
	pay := e.capture(t, req, "pay_unknown", 10000)

	e.gw.refundErr = &gateway.Error{Gateway: "razorpay", Op: "POST refund", Err: errors.New("context deadline exceeded")}
	res, err := e.svc.Refund(ctx, principal(tenant), pay.ID, RefundInput{AmountPaise: 10000, Reason: "timeout"})
	if err != nil || res.Refund.Status != "unknown" || res.RefundedPaise != 0 {
		t.Fatalf("timeout must leave the refund unknown: %+v %v", res, err)
	}
	refundID := res.Refund.ID
	// The amount stays reserved: nothing more can be refunded meanwhile.
	if _, err := e.svc.Refund(ctx, principal(tenant), pay.ID, RefundInput{AmountPaise: 1, Reason: "again"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown refund must reserve the amount: %v", err)
	}
	if len(e.payloads(t, events.PaymentRefunded)) != 0 {
		t.Fatal("no event before the gateway confirms")
	}

	// The gateway did execute it: refund.processed arrives (matched by our refund id in notes).
	body := []byte(`{"event":"refund.processed","payload":{"refund":{"entity":{"id":"rfnd_late","payment_id":"pay_unknown","amount":10000,"notes":{"refund_id":"` + refundID + `"}}}}}`)
	out, err := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_rf1"))
	if err != nil || out.Result != Applied {
		t.Fatalf("webhook: %+v %v", out, err)
	}
	status, ref, _ := e.refundRow(t, refundID)
	if status != "processed" || ref == nil || *ref != "rfnd_late" {
		t.Fatalf("refund row %s %v", status, ref)
	}
	p, _ := e.svc.GetPayment(ctx, tenant, pay.ID)
	if p.Status != "refunded" || p.RefundedPaise != 10000 {
		t.Fatalf("payment %+v", p)
	}
	evs := e.payloads(t, events.PaymentRefunded)
	if len(evs) != 1 || evs[0].RefundID != refundID || evs[0].GatewayRef != "pay_unknown" {
		t.Fatalf("refunded event %+v", evs)
	}
	// Redelivery and the same refund under another event id are both no-ops.
	if out, _ := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_rf1")); out.Result != Duplicate {
		t.Fatalf("redelivery %s", out.Result)
	}
	if out, _ := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_rf2")); out.Result != Duplicate {
		t.Fatalf("same refund again %s", out.Result)
	}
	if len(e.payloads(t, events.PaymentRefunded)) != 1 {
		t.Fatal("one event per refund")
	}

	// Definitive rejection: failed, amount free again.
	req2 := e.request(t, tenant, 5000)
	pay2 := e.capture(t, req2, "pay_reject", 5000)
	e.gw.refundErr = &gateway.Error{Gateway: "razorpay", Op: "POST refund", Status: 400, Code: "BAD_REQUEST_ERROR", Message: "nope"}
	if _, err := e.svc.Refund(ctx, principal(tenant), pay2.ID, RefundInput{AmountPaise: 5000, Reason: "x"}); httpx.AsError(err).Status != http.StatusBadGateway {
		t.Fatalf("definitive rejection must be 502: %v", err)
	}
	var failed int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_id=$1 AND status='failed'`, pay2.ID).Scan(&failed)
	if failed != 1 {
		t.Fatalf("want 1 failed refund, got %d", failed)
	}
	e.gw.refundErr = nil
	if res, err := e.svc.Refund(ctx, principal(tenant), pay2.ID, RefundInput{AmountPaise: 5000, Reason: "retry"}); err != nil || res.Refund.Status != "processed" {
		t.Fatalf("after a failed refund the amount is refundable again: %+v %v", res, err)
	}
	// A webhook for a refund made from the gateway dashboard creates a 'gateway' row.
	req3 := e.request(t, tenant, 3000)
	e.capture(t, req3, "pay_dash", 3000)
	body = []byte(`{"event":"refund.processed","payload":{"refund":{"entity":{"id":"rfnd_dash","payment_id":"pay_dash","amount":1000,"notes":[]}}}}`)
	if out, err := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_dash")); err != nil || out.Result != Applied {
		t.Fatalf("dashboard refund: %+v %v", out, err)
	}
	var src, st string
	_ = e.pool.QueryRow(ctx, `SELECT source, status FROM refunds WHERE gateway_ref='rfnd_dash'`).Scan(&src, &st)
	if src != "gateway" || st != "processed" {
		t.Fatalf("dashboard refund row %s %s", src, st)
	}
}

// Item 6: reconcile expires requests past expires_at (also old ones), books
// gateway-side refunds, and leaves unreachable requests alone.
func TestReconcileExpiryAndGatewayRefunds(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	past := time.Now().Add(-time.Hour)

	expired := e.request(t, tenant, 1000) // past expiry, nothing paid
	_, _ = e.pool.Exec(ctx, `UPDATE payment_requests SET expires_at=$2 WHERE id=$1`, expired.ID, past)

	refunded := e.request(t, tenant, 10000) // paid and partly refunded at the gateway, webhooks lost
	e.gw.status[*refunded.GatewayRef] = gateway.RemoteStatus{Closed: true, Payments: []gateway.RemotePayment{{
		PaymentRef: "pay_rec", AmountPaise: 10000, Method: "upi", Status: "captured", CapturedAt: time.Now(), RefundedPaise: 3000,
		Refunds: []gateway.RemoteRefund{{RefundRef: "rfnd_rec", AmountPaise: 3000, Status: "processed"}, {RefundRef: "rfnd_pend", AmountPaise: 100, Status: "pending"}},
	}}}

	old := e.request(t, tenant, 1000) // outside the 7-day window: swept
	_, _ = e.pool.Exec(ctx, `UPDATE payment_requests SET created_at=now() - interval '10 days', expires_at=now() - interval '9 days' WHERE id=$1`, old.ID)

	down := e.request(t, tenant, 1000) // gateway unreachable: must not be expired blind
	_, _ = e.pool.Exec(ctx, `UPDATE payment_requests SET expires_at=$2 WHERE id=$1`, down.ID, past)
	e.gw.fetchErr[*down.GatewayRef] = errors.New("503")

	live := e.request(t, tenant, 1000) // still within expiry: untouched

	rep, err := e.svc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 'old' is outside the 7-day scan window, so it is swept, not checked.
	if rep.Checked != 4 || rep.Captured != 1 || rep.Refunded != 1 || rep.Expired != 2 || rep.Errors != 1 {
		t.Fatalf("report %+v", rep)
	}
	want := map[string]string{expired.ID: "expired", refunded.ID: "paid", old.ID: "expired", down.ID: "created", live.ID: "created"}
	for id, st := range want {
		r, _ := e.svc.GetRequest(ctx, tenant, id)
		if r.Status != st {
			t.Errorf("request %s: want %s got %s", id, st, r.Status)
		}
	}
	var payStatus string
	var refundedPaise int64
	_ = e.pool.QueryRow(ctx, `SELECT status, refunded_paise FROM payments WHERE gateway_ref='pay_rec'`).Scan(&payStatus, &refundedPaise)
	if payStatus != "partially_refunded" || refundedPaise != 3000 {
		t.Fatalf("reconciled payment %s %d", payStatus, refundedPaise)
	}
	var src string
	_ = e.pool.QueryRow(ctx, `SELECT source FROM refunds WHERE gateway_ref='rfnd_rec'`).Scan(&src)
	if src != "gateway" {
		t.Fatalf("gateway refund source %q", src)
	}
	if evs := e.payloads(t, events.PaymentRefunded); len(evs) != 1 || evs[0].RefundID == "" || evs[0].AmountPaise != 3000 {
		t.Fatalf("refund events %+v", evs)
	}
	// A second run changes nothing.
	rep, _ = e.svc.Reconcile(ctx)
	if rep.Captured != 0 || rep.Refunded != 0 || rep.Expired != 0 {
		t.Fatalf("second run %+v", rep)
	}
}

// Item 1 + 6: reconcile settles refunds still unknown/pending by asking the gateway.
func TestReconcileSettlesOpenRefunds(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	e.gw.refundErr = &gateway.Error{Gateway: "razorpay", Op: "POST refund", Err: errors.New("timeout")}

	// Executed at the gateway despite the timeout.
	p1 := e.capture(t, e.request(t, tenant, 4000), "pay_s1", 4000)
	r1, _ := e.svc.Refund(ctx, principal(tenant), p1.ID, RefundInput{AmountPaise: 4000, Reason: "a"})
	e.gw.refunds["pay_s1"] = []gateway.RemoteRefund{{RefundRef: "rfnd_s1", Receipt: r1.Refund.ID, AmountPaise: 4000, Status: "processed"}}

	// Failed at the gateway.
	p2 := e.capture(t, e.request(t, tenant, 4000), "pay_s2", 4000)
	r2, _ := e.svc.Refund(ctx, principal(tenant), p2.ID, RefundInput{AmountPaise: 4000, Reason: "b"})
	e.gw.refunds["pay_s2"] = []gateway.RemoteRefund{{RefundRef: "rfnd_s2", Receipt: r2.Refund.ID, AmountPaise: 4000, Status: "failed"}}

	// Never reached the gateway: unknown for over an hour → failed, amount freed.
	p3 := e.capture(t, e.request(t, tenant, 4000), "pay_s3", 4000)
	r3, _ := e.svc.Refund(ctx, principal(tenant), p3.ID, RefundInput{AmountPaise: 4000, Reason: "c"})

	// Too fresh to ask about yet.
	p4 := e.capture(t, e.request(t, tenant, 4000), "pay_s4", 4000)
	r4, _ := e.svc.Refund(ctx, principal(tenant), p4.ID, RefundInput{AmountPaise: 4000, Reason: "d"})

	for _, id := range []string{r1.Refund.ID, r2.Refund.ID} {
		_, _ = e.pool.Exec(ctx, `UPDATE refunds SET created_at=now() - interval '5 minutes' WHERE id=$1`, id)
	}
	_, _ = e.pool.Exec(ctx, `UPDATE refunds SET created_at=now() - interval '2 hours' WHERE id=$1`, r3.Refund.ID)

	rep, err := e.svc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RefundsChecked != 3 || rep.RefundsSettled != 3 || rep.Errors != 0 {
		t.Fatalf("report %+v", rep)
	}
	for id, want := range map[string]string{r1.Refund.ID: "processed", r2.Refund.ID: "failed", r3.Refund.ID: "failed", r4.Refund.ID: "unknown"} {
		if st, _, _ := e.refundRow(t, id); st != want {
			t.Errorf("refund %s: want %s got %s", id, want, st)
		}
	}
	if evs := e.payloads(t, events.PaymentRefunded); len(evs) != 1 || evs[0].RefundID != r1.Refund.ID {
		t.Fatalf("events %+v", evs)
	}
	e.gw.refundErr = nil
	if res, err := e.svc.Refund(ctx, principal(tenant), p3.ID, RefundInput{AmountPaise: 4000, Reason: "retry"}); err != nil || res.Refund.Status != "processed" {
		t.Fatalf("after give-up the amount is refundable again: %+v %v", res, err)
	}
}

// Item 8 + 9: settlement webhook verified with the tenant's own secret → row + event;
// idempotent on gateway_ref; platform-signed settlements are kept without a tenant.
func TestSettlementWebhookEndToEnd(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	if err := e.store.Put(ctx, gateway.Credentials{TenantID: tenant, Gateway: "razorpay", KeyID: "rzp_live_abcd1234",
		KeySecret: "ks", WebhookSecret: "tenant-wh", Env: "live"}, "owner"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event":"settlement.processed","created_at":1760000100,"payload":{"settlement":{"entity":{
		"id":"setl_e2e","amount":97640,"status":"processed","fees":2000,"tax":360,"utr":"UTR777","created_at":1760000000}}}}`)

	if _, err := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "nope"), "evt_x")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("bad signature: %v", err)
	}
	out, err := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "tenant-wh"), "evt_setl1"))
	if err != nil || out.Result != Applied {
		t.Fatalf("settlement: %+v %v", out, err)
	}
	list, total, err := e.svc.ListSettlements(ctx, tenant, SettlementFilter{Page: httpx.Page{Limit: 10}})
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("list %v %d %v", list, total, err)
	}
	s := list[0]
	if s.GatewayRef != "setl_e2e" || deref(s.UTR) != "UTR777" || s.NetPaise != 97640 || s.FeePaise != 2000 || s.FeeTaxPaise != 360 || s.GrossPaise != 100000 ||
		s.GrossPaise-s.RefundsPaise != s.NetPaise+s.FeePaise+s.FeeTaxPaise {
		t.Fatalf("settlement %+v", s)
	}
	var raw []byte
	if err := e.pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE event_type=$1`, events.SettlementProcessed).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env events.Envelope
	var sp events.SettlementPayload
	if err := decodeEnvelope(raw, &env, &sp); err != nil {
		t.Fatal(err)
	}
	if env.TenantID != tenant || sp.SettlementID != s.ID || sp.Gateway != "razorpay" || sp.UTR != "UTR777" || sp.NetPaise != 97640 || sp.FeeTaxPaise != 360 || sp.SettledAt != "2025-10-09T08:53:20Z" {
		t.Fatalf("event %+v %+v", env, sp)
	}
	// Redelivered, and the same settlement under a new event id: no second row or event.
	if out, _ := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "tenant-wh"), "evt_setl1")); out.Result != Duplicate {
		t.Fatalf("redelivery %s", out.Result)
	}
	if out, _ := e.hooks.Process(ctx, body, rzpHeader(gateway.Sign(body, "tenant-wh"), "evt_setl2")); out.Result != Duplicate {
		t.Fatalf("same settlement %s", out.Result)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type=$1`, events.SettlementProcessed).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 settlement event, got %d", n)
	}
	// Platform-signed settlement: stored for audit without a tenant, no event, not listed.
	body2 := []byte(`{"event":"settlement.processed","payload":{"settlement":{"entity":{"id":"setl_platform","amount":500,"status":"processed","fees":10,"tax":2,"created_at":1760000000}}}}`)
	if out, err := e.hooks.Process(ctx, body2, rzpHeader(gateway.Sign(body2, "whsec"), "evt_setl3")); err != nil || out.Result != Applied {
		t.Fatalf("platform settlement %+v %v", out, err)
	}
	var tenantNull bool
	_ = e.pool.QueryRow(ctx, `SELECT tenant_id IS NULL FROM settlements WHERE gateway_ref='setl_platform'`).Scan(&tenantNull)
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type=$1`, events.SettlementProcessed).Scan(&n)
	if !tenantNull || n != 1 {
		t.Fatalf("platform settlement: tenant null=%v events=%d", tenantNull, n)
	}
	// A capture webhook for this tenant's request verifies with the tenant secret too.
	req := e.request(t, tenant, 11800)
	cap := []byte(`{"event":"qr_code.credited","payload":{"payment":{"entity":{"id":"pay_tn","amount":11800,"method":"upi","status":"captured","created_at":1760000000}},
		"qr_code":{"entity":{"id":"` + *req.GatewayRef + `","notes":{"request_id":"` + req.ID + `"}}}}}`)
	if out, err := e.hooks.Process(ctx, cap, rzpHeader(gateway.Sign(cap, "tenant-wh"), "evt_cap_t")); err != nil || out.Result != Applied {
		t.Fatalf("tenant-signed capture %+v %v", out, err)
	}
}

// Item 9: credentials round-trip through the store and drive gateway resolution.
func TestCredentialsStoreAndResolution(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenant, other := uuid.NewString(), uuid.NewString()

	sm, err := e.svc.PutGatewayCredentials(ctx, principal(tenant), "razorpay", GatewayCredentialInput{KeyID: "rzp_test_XYZ9876", KeySecret: "s3cr3t", WebhookSecret: "wh"})
	if err != nil || sm.KeyIDMasked != "****9876" || sm.Env != "test" || !sm.HasWebhookSecret {
		t.Fatalf("put %+v %v", sm, err)
	}
	if _, err := e.svc.PutGatewayCredentials(ctx, principal(tenant), "razorpay", GatewayCredentialInput{KeyID: "rzp_live_x", KeySecret: "s"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("razorpay without webhook secret must be 422: %v", err)
	}
	if _, err := e.svc.PutGatewayCredentials(ctx, principal(tenant), "mock", GatewayCredentialInput{KeyID: "a", KeySecret: "b"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("mock is not tenant-configurable: %v", err)
	}
	c, ok, err := e.store.Credentials(ctx, tenant, "razorpay")
	if err != nil || !ok || c.KeySecret != "s3cr3t" || c.WebhookSecret != "wh" || c.KeyID != "rzp_test_XYZ9876" {
		t.Fatalf("round trip %+v %v %v", c, ok, err)
	}
	var enc []byte
	_ = e.pool.QueryRow(ctx, `SELECT key_secret_enc FROM gateway_credentials WHERE tenant_id=$1`, tenant).Scan(&enc)
	if string(enc) == "s3cr3t" || len(enc) < 20 {
		t.Fatal("secret stored in clear")
	}
	list, _ := e.svc.ListGatewayCredentials(ctx, tenant)
	if len(list) != 1 || list[0].KeyIDMasked != "****9876" {
		t.Fatalf("list %+v", list)
	}
	if l, _ := e.svc.ListGatewayCredentials(ctx, other); len(l) != 0 {
		t.Fatal("tenant isolation")
	}
	// Resolution: the tenant gets a driver built from its keys, others the platform one.
	g, err := e.svc.Gateways.For(ctx, tenant, "razorpay")
	if err != nil || g.(*fakeGW).keyID != "rzp_test_XYZ9876" {
		t.Fatalf("tenant driver %+v %v", g, err)
	}
	if g, err := e.svc.Gateways.For(ctx, other, "razorpay"); err != nil || g != gateway.Gateway(e.gw) {
		t.Fatalf("platform driver %+v %v", g, err)
	}
	// Cashfree only for tenants with their own keys (no platform keys here).
	if _, err := e.svc.PutGatewayCredentials(ctx, principal(tenant), "cashfree", GatewayCredentialInput{KeyID: "TEST123", KeySecret: "cs"}); err != nil {
		t.Fatal(err)
	}
	names, _ := e.svc.Gateways.NamesFor(ctx, tenant)
	if len(names) != 3 { // cashfree, mock, razorpay
		t.Fatalf("names %v", names)
	}
	sec, ok, _ := e.store.WebhookSecret(ctx, tenant, "cashfree")
	if !ok || sec != "cs" {
		t.Fatalf("cashfree webhook secret falls back to the client secret: %q %v", sec, ok)
	}
	all, _ := e.store.AllWebhookSecrets(ctx, "razorpay")
	if len(all) != 1 || all[0].TenantID != tenant || all[0].Secret != "wh" {
		t.Fatalf("all secrets %+v", all)
	}
	if _, err := e.svc.CreateRequest(ctx, principal(other), RequestInput{InvoiceID: uuid.NewString(), AmountPaise: 100, Purpose: "invoice", Kind: "upi_qr", Gateway: "cashfree"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Fatalf("other tenant cannot use cashfree: %v", err)
	}
	if err := e.svc.DeleteGatewayCredentials(ctx, principal(tenant), "razorpay"); err != nil {
		t.Fatal(err)
	}
	if g, _ := e.svc.Gateways.For(ctx, tenant, "razorpay"); g != gateway.Gateway(e.gw) {
		t.Fatal("after delete the platform keys apply again")
	}
	if err := e.svc.DeleteGatewayCredentials(ctx, principal(tenant), "razorpay"); httpx.AsError(err).Status != http.StatusNotFound {
		t.Fatalf("second delete: %v", err)
	}
}
