package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/aadesh/migrations"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// fakes stands in for pehchaan, bahi, bhandar and lenden. A store missing
// from stores is served in the pre-registration shape (no registration_id),
// as an older pehchaan would.
type fakes struct {
	srv      *httptest.Server
	items    map[string]clients.Item
	parties  map[string]clients.Party
	stores   map[string]clients.Store
	requests int
}

func newFakes(t *testing.T) *fakes {
	t.Helper()
	f := &fakes{items: map[string]clients.Item{}, parties: map[string]clients.Party{}, stores: map[string]clients.Store{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/stores/{id}", func(w http.ResponseWriter, r *http.Request) {
		if st, ok := f.stores[r.PathValue("id")]; ok {
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		_ = json.NewEncoder(w).Encode(clients.Store{ID: r.PathValue("id"), Name: "Store", Code: "ST", StateCode: "27", GSTIN: "27AAPFU0939F1ZV"})
	})
	mux.HandleFunc("GET /internal/v1/parties/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.parties[r.PathValue("id")]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"party not found"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("POST /internal/v1/items/lookup", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		var out []clients.Item
		for _, id := range in.IDs {
			if it, ok := f.items[id]; ok {
				out = append(out, it)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
	})
	mux.HandleFunc("POST /internal/v1/payment-requests", func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		var in clients.PaymentRequestInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(clients.PaymentRequest{ID: fmt.Sprintf("pr-%d", f.requests), Status: "created", UPIIntent: fmt.Sprintf("upi://pay?am=%d", in.AmountPaise)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newTestService(t *testing.T) (*Service, *fakes, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("aadesh_test_%d", rand.Int63())
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
	f := newFakes(t)
	c := func() *svc.Client { return svc.New(f.srv.URL, "secret", false) }
	s := &Service{DB: pool, Dir: clients.NewDirectory(c(), c(), c(), c()), Log: slog.Default(),
		Now: func() time.Time { return time.Date(2026, 10, 23, 10, 0, 0, 0, IST) }}
	return s, f, pool
}

func status(err error) int { return httpx.AsError(err).Status }

func TestBillingEndToEnd(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, storeA, storeB := uuid.NewString(), uuid.NewString(), uuid.NewString()
	itemA, itemOff := uuid.NewString(), uuid.NewString()
	f.items[itemA] = clients.Item{ID: itemA, Name: "Parle-G 100g", HSN: "1905", Unit: "PCS", SalePricePaise: 1000, MRPPaise: 1000,
		TaxRateBP: 1800, TaxCategory: "taxable", PriceIncludesTax: true, Status: "active",
		AltUnits: []struct {
			Unit   string  `json:"unit"`
			Factor float64 `json:"factor"`
		}{{Unit: "BOX", Factor: 48}}}
	f.items[itemOff] = clients.Item{ID: itemOff, Name: "Gone", Unit: "PCS", TaxRateBP: 500, Status: "inactive"}
	supplier, customer := uuid.NewString(), uuid.NewString()
	f.parties[supplier] = clients.Party{ID: supplier, Name: "Britannia Depot", Type: "supplier", StateCode: "27", GSTIN: "27AAPFU0939F1ZV"}
	f.parties[customer] = clients.Party{ID: customer, Name: "Sharma Stores", Type: "customer", StateCode: "27", CreditLimitPaise: 50000, CreditDays: 15}

	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: storeA}
	line := func(qty float64, price int64) LineInput {
		l := LineInput{ItemID: itemA, Name: "Parle-G 100g"}
		l.Qty, l.UnitPricePaise, l.TaxRateBP, l.PriceIncludesTax = qty, price, 1800, true
		return l
	}

	// 1. Cash sale with change: ₹20 bill, ₹25 tendered.
	inv, created, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{line(2, 1000)}, Tenders: []Tender{{Mode: "cash", AmountPaise: 2500}}})
	if err != nil || !created {
		t.Fatalf("sale: %v", err)
	}
	if inv.Number != "INV/26-27/00001" || inv.GrandTotalPaise != 2000 || inv.PaidPaise != 2000 || inv.ChangePaise != 500 ||
		inv.PaymentStatus != "paid" || inv.BalancePaise != 0 || deref(inv.StoreGSTIN) != "27AAPFU0939F1ZV" || deref(inv.Lines[0].HSN) != "1905" || inv.Lines[0].TaxCategory != "taxable" {
		t.Fatalf("sale: %+v", inv)
	}

	// 2. Numbering is one series per GSTIN: store B continues the same sequence.
	invB, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: storeB}, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}})
	if err != nil || invB.Number != "INV/26-27/00002" || invB.PaymentStatus != "unpaid" || invB.BalancePaise != 1000 {
		t.Fatalf("store B: %+v %v", invB, err)
	}
	// A store with its own prefix gets its own series; the shared one carries on.
	if err := s.SetNumbering(ctx, tenant, NumberingSetting{StoreID: storeB, DocType: DocSale, Prefix: "BLR"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNumbering(ctx, tenant, NumberingSetting{StoreID: storeA, DocType: DocSale, Prefix: "BLR"}); status(err) != 409 {
		t.Fatalf("duplicate prefix must be 409: %v", err)
	}
	invB2, _, _ := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: storeB}, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}})
	invA3, _, _ := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}})
	if invB2 == nil || invA3 == nil || invB2.Number != "BLR/26-27/00001" || invA3.Number != "INV/26-27/00003" {
		t.Fatalf("series split: %v %v", invB2, invA3)
	}

	// 3. Item master validation.
	bad := line(1, 1000)
	bad.TaxRateBP = 500
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{bad}}); status(err) != 422 {
		t.Fatalf("wrong rate must be 422: %v", err)
	}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1200)}}); status(err) != 422 {
		t.Fatalf("above MRP must be 422: %v", err)
	}
	box := line(1, 40000) // 1 BOX of 48 at ₹400 = ₹8.33 each, under MRP
	box.Unit = "BOX"
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{box}}); err != nil {
		t.Fatalf("box price per piece under MRP must pass: %v", err)
	}
	off := LineInput{ItemID: itemOff, Name: "Gone"}
	off.Qty, off.UnitPricePaise, off.TaxRateBP = 1, 100, 500
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{off}}); status(err) != 422 {
		t.Fatalf("inactive item must be 422: %v", err)
	}
	ghost := LineInput{ItemID: uuid.NewString(), Name: "Ghost"}
	ghost.Qty, ghost.UnitPricePaise = 1, 100
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{ghost}}); status(err) != 422 {
		t.Fatalf("unknown item must be 422: %v", err)
	}
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, InvoiceDate: "2026-10-24", Lines: []LineInput{line(1, 1000)}}); status(err) != 422 {
		t.Fatalf("future date must be 422: %v", err)
	}

	// 4. Credit limit and due date.
	big := line(60, 1000)
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, PartyID: customer, Lines: []LineInput{big}, Tenders: []Tender{{Mode: "credit", AmountPaise: 60000}}}); status(err) != 422 {
		t.Fatalf("over credit limit must be 422: %v", err)
	}
	cr, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, PartyID: customer, Lines: []LineInput{big}, OverrideCreditLimit: true})
	if err != nil || deref(cr.DueDate) != "2026-11-07" || cr.PaymentStatus != "unpaid" {
		t.Fatalf("override: %+v %v", cr, err)
	}

	// 5. Purchase: supplier bill date required, duplicates rejected, a different rate is tolerated.
	pl := line(96, 820)
	pl.TaxRateBP, pl.PriceIncludesTax = 1200, false // supplier billed the old 12 % slab
	pur := CreateInput{DocType: DocPurchase, PartyID: supplier, SupplierInvoiceNo: "BR/551", SupplierInvoiceDate: "2025-08-20", InvoiceDate: "2025-08-21", Lines: []LineInput{pl}}
	p1, _, err := s.Create(ctx, admin, pur)
	if err != nil || p1.Number != "PUR/25-26/00001" || p1.FY != "25-26" {
		t.Fatalf("purchase in last FY with 12 %%: %+v %v", p1, err)
	}
	if _, _, err := s.Create(ctx, admin, pur); status(err) != 409 {
		t.Fatalf("duplicate supplier bill must be 409: %v", err)
	}

	// 6. Credit note against sale 1: quantities and prices are bound to the original.
	cnLine := LineInput{ItemID: itemA, Name: "Parle-G 100g"}
	cnLine.Qty = 1
	cn, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSaleReturn, RefInvoiceID: inv.ID, Lines: []LineInput{cnLine}})
	if err != nil || cn.Number != "CN/26-27/00001" || cn.GrandTotalPaise != 1000 || cn.Lines[0].TaxRateBP != 1800 {
		t.Fatalf("credit note: %+v %v", cn, err)
	}
	orig, _ := s.Get(ctx, tenant, inv.ID)
	if orig.CreditedPaise != 1000 || orig.BalancePaise != 0 || orig.PaymentStatus != "paid" {
		t.Fatalf("original after CN: credited=%d balance=%d status=%s", orig.CreditedPaise, orig.BalancePaise, orig.PaymentStatus)
	}
	cnLine.Qty = 2
	if _, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSaleReturn, RefInvoiceID: inv.ID, Lines: []LineInput{cnLine}}); status(err) != 422 {
		t.Fatalf("over-return must be 422: %v", err)
	}
	cnLine.Qty = 1
	if _, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: storeB}, CreateInput{DocType: DocSaleReturn, RefInvoiceID: inv.ID, Lines: []LineInput{cnLine}}); status(err) != 422 {
		t.Fatalf("return at another store must be 422: %v", err)
	}
	if _, err := s.Cancel(ctx, admin, inv.ID, "wrong"); status(err) != 409 {
		t.Fatalf("cancel with notes must be 409: %v", err)
	}
	if _, err := s.Cancel(ctx, admin, cn.ID, "entered twice"); err != nil {
		t.Fatal(err)
	}
	if orig, _ = s.Get(ctx, tenant, inv.ID); orig.CreditedPaise != 0 {
		t.Fatalf("credit must be undone: %d", orig.CreditedPaise)
	}

	// 7. Offline block: size capped by role, bills keep the day they were made, re-sync is a duplicate.
	cashier := &auth.Principal{Claims: &auth.Claims{TenantID: tenant, StoreID: storeA, Role: auth.RoleCashier, Stores: []string{storeA}}, TenantID: tenant, StoreID: storeA}
	if _, err := s.ReserveBlock(ctx, cashier, "", DocSale, "pos-1", 200); status(err) != 422 {
		t.Fatalf("cashier block cap: %v", err)
	}
	blk, err := s.ReserveBlock(ctx, cashier, "", DocSale, "pos-1", 50)
	if err != nil || blk.EndNo-blk.StartNo != 49 || blk.Prefix != "INV" {
		t.Fatalf("block: %+v %v", blk, err)
	}
	made := time.Date(2026, 10, 22, 21, 30, 0, 0, time.UTC) // 03:00 IST on 23 Oct
	off1 := CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}, ClientUUID: "pos-1-0001", DeviceID: "pos-1", Number: blk.StartNo, ClientCreatedAt: &made,
		Tenders: []Tender{{Mode: "cash", AmountPaise: 1000}}}
	o1, created, err := s.Create(ctx, cashier, off1)
	if err != nil || !created || o1.Number != FormatNumber("INV", "26-27", blk.StartNo) || o1.InvoiceDate != "2026-10-23" {
		t.Fatalf("offline bill: %+v %v", o1, err)
	}
	if o2, created, err := s.Create(ctx, cashier, off1); err != nil || created || o2.ID != o1.ID {
		t.Fatalf("re-sync must be a duplicate: %v", err)
	}
	if _, _, err := s.Create(ctx, cashier, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}, ClientUUID: "pos-1-0002", DeviceID: "pos-1", Number: blk.EndNo + 1}); status(err) != 422 {
		t.Fatalf("number outside block must be 422: %v", err)
	}
	rel, err := s.ReleaseBlock(ctx, cashier, blk.ID, blk.StartNo)
	if err != nil || rel.LastUsedNo == nil || *rel.LastUsedNo != blk.StartNo || rel.ReleasedAt == nil {
		t.Fatalf("release: %+v %v", rel, err)
	}
	if _, _, err := s.Create(ctx, cashier, CreateInput{DocType: DocSale, Lines: []LineInput{line(1, 1000)}, ClientUUID: "pos-1-0003", DeviceID: "pos-1", Number: blk.StartNo + 1}); status(err) != 422 {
		t.Fatalf("released numbers must be refused: %v", err)
	}

	// 8. Payments: a capture that answers a counter tender does not count twice; refunds are per refund id.
	paid, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{line(2, 1000)},
		Tenders: []Tender{{Mode: "upi", AmountPaise: 2000, PaymentRequestID: "pr-x"}}})
	if err != nil || paid.PaidPaise != 2000 {
		t.Fatal(err)
	}
	capture := func(invoiceID, paymentID, reqID string, amount int64) events.Envelope {
		e, _ := events.New(events.PaymentCaptured, "lenden", tenant, storeA, events.PaymentPayload{PaymentID: paymentID, PaymentRequestID: reqID, InvoiceID: invoiceID, AmountPaise: amount, Method: "upi", Gateway: "razorpay"})
		return e
	}
	if err := s.ApplyPayment(ctx, capture(paid.ID, "pay-1", "pr-x", 2000), false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, tenant, paid.ID); got.PaidPaise != 2000 || got.PaymentStatus != "paid" {
		t.Fatalf("tender-backed capture must not double count: %+v", got)
	}
	if err := s.ApplyPayment(ctx, capture(invB.ID, "pay-2", "", 400), false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, tenant, invB.ID); got.PaidPaise != 400 || got.PaymentStatus != "partial" || got.BalancePaise != 600 {
		t.Fatalf("capture: %+v", got)
	}
	refund := func(paymentID, refundID string, amount int64) events.Envelope {
		e, _ := events.New(events.PaymentRefunded, "lenden", tenant, storeA, events.PaymentPayload{PaymentID: paymentID, RefundID: refundID, InvoiceID: invB.ID, AmountPaise: amount})
		return e
	}
	_ = s.ApplyPayment(ctx, refund("pay-2", "rf-1", 100), true)
	_ = s.ApplyPayment(ctx, refund("pay-2", "rf-2", 100), true)
	if got, _ := s.Get(ctx, tenant, invB.ID); got.PaidPaise != 200 {
		t.Fatalf("two partial refunds: %+v", got)
	}
	var refunded int64
	_ = pool.QueryRow(ctx, `SELECT refunded_paise FROM invoice_receipts WHERE payment_id='pay-2'`).Scan(&refunded)
	if refunded != 200 {
		t.Fatalf("receipt refunded %d", refunded)
	}
	if err := s.ApplyPayment(ctx, capture(p1.ID, "pay-3", "", 500), false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, tenant, p1.ID); got.PaidPaise != 0 {
		t.Fatalf("customer payment on a purchase must not count: %+v", got)
	}
	if _, err := s.Cancel(ctx, admin, invB.ID, "test"); status(err) != 409 {
		t.Fatalf("cancel with unrefunded online payment must be 409: %v", err)
	}

	// 9. Counter QR for what is still due.
	req, err := s.PaymentRequest(ctx, admin, invB.ID, "")
	if err != nil || !strings.Contains(req.UPIIntent, "am=800") {
		t.Fatalf("payment request: %+v %v", req, err)
	}
	if _, err := s.PaymentRequest(ctx, admin, inv.ID, ""); status(err) != 409 {
		t.Fatalf("paid invoice must be 409: %v", err)
	}

	// Events were queued for every document.
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type IN ($1,$2,$3)`, events.InvoiceCreated, events.NoteCreated, events.InvoiceCancelled).Scan(&n)
	if n < 10 {
		t.Fatalf("expected outbox events, got %d", n)
	}
}
