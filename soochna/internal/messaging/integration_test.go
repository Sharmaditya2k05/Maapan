package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/providers"
	"github.com/maapan/soochna/migrations"
)

// fakeSender records messages and fails while fail > 0.
type fakeSender struct {
	mu   sync.Mutex
	fail int
	err  error
	sent []providers.Message
}

func (f *fakeSender) Name() string { return "fake" }
func (f *fakeSender) Send(_ context.Context, m providers.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != 0 {
		f.fail--
		if f.err != nil {
			return "", f.err
		}
		return "", errors.New("provider down")
	}
	f.sent = append(f.sent, m)
	return fmt.Sprintf("ref-%d", len(f.sent)), nil
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []providers.Email
}

func (f *fakeMailer) Name() string { return "fakemail" }
func (f *fakeMailer) SendEmail(_ context.Context, e providers.Email) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, e)
	return "<id@x>", nil
}

type fakeTenants struct{}

func (fakeTenants) Get(_ context.Context, id string) (clients.Tenant, error) {
	return clients.Tenant{ID: id, BusinessName: "Sharma Store", OwnerPhone: "+919000000000"}, nil
}

const tenantA = "11111111-1111-1111-1111-111111111111"

type testEnv struct {
	s    *Service
	pool *pgxpool.Pool
	sms  *fakeSender
	wa   *fakeSender
	mail *fakeMailer
	now  *time.Time
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("soochna_test_%d", rand.Int63())
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, migrations.FS, log); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 11, 0, 0, 0, IST)
	quiet, _ := ParseQuietHours("21:00-09:00", IST)
	e := &testEnv{pool: pool, sms: &fakeSender{}, wa: &fakeSender{}, mail: &fakeMailer{}, now: &now}
	e.s = &Service{DB: pool, SMS: e.sms, WhatsApp: e.wa, Email: e.mail, Tenants: fakeTenants{}, Log: log,
		WebhookToken: "tok", Quiet: quiet, PromoRequiresOptIn: true, Now: func() time.Time { return *e.now }}
	return e
}

func (e *testEnv) status(t *testing.T, id string) (status string, attempts int) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT status, attempts FROM messages WHERE id=$1`, id).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return
}

// makeDue moves retry times into the past (the test clock does not drive SQL now()).
func (e *testEnv) makeDue(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE messages SET next_attempt_at = now() - interval '1 second'
		WHERE status IN ('failed','scheduled')`); err != nil {
		t.Fatal(err)
	}
}

func invoiceInput(to string) SendInput {
	in := SendInput{Channel: "whatsapp", To: to, Template: "invoice", Params: map[string]string{
		"party_name": "Ravi", "business": "Sharma Store", "number": "INV/1", "amount": "₹10.00", "date": "3 Nov 2026"}}
	_ = in.Validate()
	return in
}

func TestIntegrationRetryDeadLetterReplay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.wa.fail = 100
	res, err := e.s.Send(ctx, tenantA, invoiceInput("9876543210"))
	if err != nil || res.Status != StatusFailed {
		t.Fatalf("%+v %v", res, err)
	}
	for i := 0; i < MaxAttempts-1; i++ {
		e.makeDue(t)
		if _, err := e.s.RetryFailed(ctx, 50); err != nil {
			t.Fatal(err)
		}
	}
	if st, n := e.status(t, res.ID); st != StatusDead || n != MaxAttempts {
		t.Fatalf("want dead after %d attempts, got %s/%d", MaxAttempts, st, n)
	}
	// Dead messages are not retried any more.
	e.makeDue(t)
	if r, _ := e.s.RetryFailed(ctx, 50); r.Attempted != 0 {
		t.Fatalf("dead message retried: %+v", r)
	}
	dls, total, err := e.s.ListDead(ctx, DeadFilter{TenantID: tenantA})
	if err != nil || total != 1 || dls[0].MessageID != res.ID || dls[0].Reason != ReasonMaxAttempts {
		t.Fatalf("%+v %d %v", dls, total, err)
	}

	e.wa.fail = 0
	rr, err := e.s.ReplayDead(ctx, ReplayInput{IDs: []string{dls[0].ID}, Limit: 10}, "ops")
	if err != nil || rr.Replayed != 1 || rr.Statuses[StatusSent] != 1 {
		t.Fatalf("%+v %v", rr, err)
	}
	if st, _ := e.status(t, res.ID); st != StatusSent {
		t.Fatalf("after replay: %s", st)
	}
	if _, open, _ := e.s.ListDead(ctx, DeadFilter{}); open != 0 {
		t.Fatalf("replayed dead letter still open: %d", open)
	}
	// A second replay of the same row is a no-op.
	if rr, _ := e.s.ReplayDead(ctx, ReplayInput{IDs: []string{dls[0].ID}, Limit: 10}, "ops"); rr.Replayed != 0 {
		t.Fatalf("double replay: %+v", rr)
	}
}

func TestIntegrationNotConfiguredAndExpired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.wa.fail, e.wa.err = 1, fmt.Errorf("%w: no template", providers.ErrNotConfigured)
	res, _ := e.s.Send(ctx, tenantA, invoiceInput("9876543210"))
	if res.Status != StatusDead {
		t.Fatalf("not-configured must dead-letter at once, got %s", res.Status)
	}
	e.wa.fail, e.wa.err = 1, nil
	res2, _ := e.s.Send(ctx, tenantA, invoiceInput("9876543211"))
	_, _ = e.pool.Exec(ctx, `UPDATE messages SET created_at = now() - interval '25 hours' WHERE id=$1`, res2.ID)
	r, err := e.s.RetryFailed(ctx, 50)
	if err != nil || r.Expired != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if st, _ := e.status(t, res2.ID); st != StatusDead {
		t.Fatalf("expired failure must be dead-lettered, got %s", st)
	}
}

func TestIntegrationOptOutViaWebhook(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wr, err := e.s.ApplyWebhook(ctx, "gupshup", "tok",
		[]byte(`{"type":"message","payload":{"id":"x","source":"919876543210","type":"text","payload":{"text":"STOP"}}}`), "application/json")
	if err != nil || wr.Consent != ConsentOptedOut {
		t.Fatalf("%+v %v", wr, err)
	}
	if _, err := e.s.ApplyWebhook(ctx, "gupshup", "wrong", []byte(`{}`), ""); err == nil {
		t.Fatal("bad token accepted")
	}
	res, err := e.s.Send(ctx, tenantA, invoiceInput("9876543210"))
	if err != nil || res.Status != StatusSuppressed || len(e.wa.sent) != 0 {
		t.Fatalf("customer WhatsApp to opted-out number: %+v %v", res, err)
	}
	// SMS on the same number is a different channel (STOP arrived on WhatsApp).
	sms := invoiceInput("9876543210")
	sms.Channel = "sms"
	if res, _ := e.s.Send(ctx, tenantA, sms); res.Status != StatusSent {
		t.Fatalf("sms should still go: %+v", res)
	}
	// Owner alerts are unaffected even to an opted-out number.
	alert := SendInput{Channel: "whatsapp", To: "9876543210", Template: "stock_low", Params: map[string]string{
		"item": "Sugar", "on_hand": "1", "reorder_level": "5", "business": "S"}}
	_ = alert.Validate()
	if res, _ := e.s.Send(ctx, tenantA, alert); res.Status != StatusSent {
		t.Fatalf("alerts must ignore consent: %+v", res)
	}
	// START restores.
	if _, err := e.s.ApplyWebhook(ctx, "msg91", "tok", []byte(`sender=919876543210&message=START`), "application/x-www-form-urlencoded"); err != nil {
		t.Fatal(err)
	}
	in := invoiceInput("9876543210")
	in.IdempotencyKey = "after-start"
	if res, _ := e.s.Send(ctx, tenantA, in); res.Status != StatusSent {
		t.Fatalf("after START: %+v", res)
	}
	// A store-level opt-out only affects that store.
	if err := e.s.SetConsent(ctx, tenantA, ConsentInput{Address: "+919876543299", Channel: "all", Status: ConsentOptedOut}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.s.Send(ctx, tenantA, invoiceInput("9876543299")); res.Status != StatusSuppressed {
		t.Fatalf("tenant opt-out ignored: %+v", res)
	}
	if res, _ := e.s.Send(ctx, "22222222-2222-2222-2222-222222222222", invoiceInput("9876543299")); res.Status != StatusSent {
		t.Fatalf("other tenant must still send: %+v", res)
	}
}

func TestIntegrationPromotionalQuietHoursAndDND(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	promo := func(to, key string) SendInput {
		in := SendInput{Channel: "sms", To: to, Template: "promotion", IdempotencyKey: key,
			Params: map[string]string{"business": "Sharma Store", "text": "Diwali offer: 10% off"}}
		_ = in.Validate()
		return in
	}
	// No opt-in → suppressed.
	if res, _ := e.s.Send(ctx, tenantA, promo("9876543210", "p1")); res.Status != StatusSuppressed {
		t.Fatalf("promo without opt-in: %+v", res)
	}
	_ = e.s.SetConsent(ctx, tenantA, ConsentInput{Address: "+919876543210", Channel: "all", Status: ConsentOptedIn})
	// Inside quiet hours → scheduled.
	*e.now = time.Date(2026, 11, 3, 22, 0, 0, 0, IST)
	res, _ := e.s.Send(ctx, tenantA, promo("9876543210", "p2"))
	if res.Status != StatusScheduled || len(e.sms.sent) != 0 {
		t.Fatalf("quiet hours: %+v", res)
	}
	// Still quiet at the retry run → stays scheduled.
	e.makeDue(t)
	_, _ = e.s.RetryFailed(ctx, 50)
	if st, _ := e.status(t, res.ID); st != StatusScheduled {
		t.Fatalf("released during quiet hours: %s", st)
	}
	// Morning → sent.
	*e.now = time.Date(2026, 11, 4, 9, 5, 0, 0, IST)
	e.makeDue(t)
	r, _ := e.s.RetryFailed(ctx, 50)
	if st, _ := e.status(t, res.ID); st != StatusSent || r.Scheduled != 1 {
		t.Fatalf("morning release: %s %+v", st, r)
	}
	// DND blocks promotional but not transactional.
	if _, err := e.s.BulkConsent(ctx, BulkConsentInput{Addresses: []string{"9876543211", "bogus"}, Scope: ScopeGlobal, Status: ConsentDND}); err != nil {
		t.Fatal(err)
	}
	_ = e.s.SetConsent(ctx, tenantA, ConsentInput{Address: "+919876543211", Channel: "all", Status: ConsentOptedIn})
	if res, _ := e.s.Send(ctx, tenantA, promo("9876543211", "p3")); res.Status != StatusSuppressed {
		t.Fatalf("DND promo: %+v", res)
	}
	tx := invoiceInput("9876543211")
	tx.Channel = "sms"
	if res, _ := e.s.Send(ctx, tenantA, tx); res.Status != StatusSent {
		t.Fatalf("DND must not block transactional: %+v", res)
	}
}

func TestIntegrationRegistryEmailAndReminders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := SenderSettings{SMSSenderID: "SHRMST", WhatsAppSource: "9111111111", WhatsAppAppName: "sharma",
		EmailFrom: "bills@sharma.example", EmailFromName: "Sharma Store", PaymentReminders: true,
		ReminderDays: []int{0, 7}, ReminderChannels: []string{"whatsapp", "email"},
		Templates: []TenantTemplate{{Template: "payment_reminder", Channel: "whatsapp", ProviderTemplateID: "wa-rem-sharma"}}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := e.s.PutSenders(ctx, tenantA, "u1", in)
	if err != nil || got.WhatsAppSource != "+919111111111" || len(got.Templates) != 1 {
		t.Fatalf("%+v %v", got, err)
	}

	e.s.Bahi = &stubBahi{overdue: []clients.OverdueParty{
		{PartyID: "p1", PartyName: "Ravi", Phone: "9876543210", Email: "ravi@example.com", OverduePaise: 50000, OldestDueDate: "2026-10-27", DaysOverdue: 7},
		{PartyID: "p2", PartyName: "Mohan", Phone: "9876543212", OverduePaise: 10000, OldestDueDate: "2026-10-30", DaysOverdue: 4}, // not a reminder day
		{PartyID: "p3", PartyName: "Walk-in", OverduePaise: 1000, OldestDueDate: "2026-11-03", DaysOverdue: 0},                     // no contact
	}}
	dry, err := e.s.PaymentReminders(ctx, RemindersInput{AsOf: "2026-11-03", DryRun: true})
	if err != nil || len(dry.Tenants) != 1 || dry.Tenants[0].Statuses["would_send"] != 2 || len(e.wa.sent) != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	res, err := e.s.PaymentReminders(ctx, RemindersInput{AsOf: "2026-11-03"})
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tenants[0]
	if tr.Overdue != 3 || tr.Due != 2 || tr.NoContact != 1 || tr.Statuses[StatusSent] != 2 {
		t.Fatalf("%+v", tr)
	}
	if len(e.wa.sent) != 1 || e.wa.sent[0].SenderID != "+919111111111" || e.wa.sent[0].AppName != "sharma" || e.wa.sent[0].TemplateID != "wa-rem-sharma" {
		t.Fatalf("registry overrides not applied: %+v", e.wa.sent)
	}
	if len(e.mail.sent) != 1 || e.mail.sent[0].FromAddress != "bills@sharma.example" || e.mail.sent[0].Subject == "" {
		t.Fatalf("email: %+v", e.mail.sent)
	}
	// Re-running the same day sends nothing new.
	again, _ := e.s.PaymentReminders(ctx, RemindersInput{AsOf: "2026-11-03"})
	if len(e.wa.sent) != 1 || again.Tenants[0].Statuses[StatusSent] != 2 {
		t.Fatalf("rerun resent: %+v", again)
	}
	// bahi without the endpoint → reported per tenant, job still succeeds.
	e.s.Bahi = &stubBahi{err: clients.ErrOverdueUnsupported}
	res, err = e.s.PaymentReminders(ctx, RemindersInput{AsOf: "2026-11-04"})
	if err != nil || res.Tenants[0].Error == "" {
		t.Fatalf("%+v %v", res, err)
	}
	// Tenants that did not opt in get nothing.
	if _, err := e.s.PutSenders(ctx, tenantA, "u1", SenderSettings{ReminderDays: []int{0}, ReminderChannels: []string{"sms"}}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.s.PaymentReminders(ctx, RemindersInput{}); len(res.Tenants) != 0 {
		t.Fatalf("reminders off but ran: %+v", res)
	}
}
