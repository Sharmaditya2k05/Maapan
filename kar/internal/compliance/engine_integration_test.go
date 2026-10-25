package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/kar/internal/clients"
	"github.com/maapan/kar/internal/gsp"
	"github.com/maapan/kar/migrations"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("kar_engine_test_%d", rand.Int63())
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
	return pool
}

func validGSTIN(t *testing.T, first14 string) string {
	t.Helper()
	for _, c := range "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		if g := first14 + string(c); gst.ValidateGSTIN(g) == nil {
			return g
		}
	}
	t.Fatalf("no check char for %s", first14)
	return ""
}

// fakeDirectory serves pehchaan, bahi and aadesh internal APIs from memory.
type fakeDirectory struct {
	mu       sync.Mutex
	stores   map[string]clients.Store
	parties  map[string]clients.Party
	invoices map[string]clients.Invoice
}

func (f *fakeDirectory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // internal v1 <kind> <id>
	if len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	var v any
	var ok bool
	switch parts[2] {
	case "stores":
		v, ok = f.stores[parts[3]]
	case "parties":
		v, ok = f.parties[parts[3]]
	case "invoices":
		v, ok = f.invoices[parts[3]]
	case "tenants":
		v, ok = clients.Tenant{ID: parts[3], BusinessName: "Acme Traders Pvt Ltd"}, true
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	engine   *Engine
	sim      *gsp.Simulator
	dir      *fakeDirectory
	clock    *clock
	tenant   string
	store    string // e-invoice applicable registration
	storeB2C string // registration below the threshold
	seller   string
	buyer    string
	party    string
}

func newFixture(t *testing.T) *fixture {
	pool := testPool(t)
	f := &fixture{t: t, pool: pool, tenant: uuid.NewString(), store: uuid.NewString(), storeB2C: uuid.NewString(),
		seller: "27AAPFU0939F1ZV", buyer: validGSTIN(t, "29AAACB1234C1Z"), party: uuid.NewString(),
		clock: &clock{t: time.Date(2026, 10, 24, 10, 0, 0, 0, gsp.IST)}}
	f.dir = &fakeDirectory{
		stores: map[string]clients.Store{
			f.store: {ID: f.store, TenantID: f.tenant, Name: "Acme Pune", StateCode: "27", GSTIN: f.seller, Address: "12 MG Road, Shivajinagar",
				Phone: "+91 98765 43210", RegistrationID: uuid.NewString(), LegalName: "Acme Traders Pvt Ltd", TradeName: "Acme Mart",
				City: "Pune", Pin: "411005", EInvoiceApplicable: true, RegistrationPrimary: true},
			f.storeB2C: {ID: f.storeB2C, TenantID: f.tenant, Name: "Acme Small", StateCode: "27", GSTIN: f.seller, Address: "1 FC Road",
				RegistrationID: uuid.NewString(), LegalName: "Acme Small", City: "Pune", Pin: "411004", EInvoiceApplicable: false},
		},
		parties:  map[string]clients.Party{f.party: {ID: f.party, Name: "Bharat Stores", GSTIN: f.buyer, StateCode: "29", Address: "4 Church St, Bengaluru 560001"}},
		invoices: map[string]clients.Invoice{},
	}
	srv := httptest.NewServer(f.dir)
	t.Cleanup(srv.Close)
	c := svc.New(srv.URL, "secret", false)
	f.sim = gsp.NewSimulator(&gsp.PGStore{DB: pool}, []byte("sim-key"))
	f.sim.Now = f.clock.Now
	f.engine = &Engine{DB: pool, Dir: &clients.Directory{Aadesh: c, Pehchaan: c, Bahi: c}, GSP: f.sim, Log: slog.Default(),
		Now: f.clock.Now, MaxAttempts: 3}
	return f
}

func (f *fixture) payload(number, docType, storeID string) events.InvoicePayload {
	return events.InvoicePayload{
		InvoiceID: uuid.NewString(), StoreID: storeID, StoreState: "27", StoreGSTIN: f.seller, DocType: docType, Number: number,
		InvoiceDate: "2026-10-23", PartyID: f.party, PartyName: "Bharat Stores", PartyGSTIN: f.buyer, PlaceOfSupply: "29", InterState: true,
		TaxablePaise: 6000000, IGSTPaise: 1080000, GrandTotalPaise: 7080000,
		Lines: []events.InvoiceLine{{LineNo: 1, Name: "Steel Almirah", HSN: "940320", Unit: "pcs", Qty: 2, TaxRateBP: 1800,
			TaxablePaise: 6000000, IGSTPaise: 1080000, TotalPaise: 7080000}},
	}
}

func (f *fixture) deliver(typ string, p events.InvoicePayload) (events.Envelope, error) {
	env, err := events.New(typ, "aadesh", f.tenant, p.StoreID, p)
	if err != nil {
		f.t.Fatal(err)
	}
	return env, f.engine.OnInvoiceEvent(context.Background(), env)
}

type outboxRow struct {
	Type string
	Data map[string]any
}

func (f *fixture) outbox(eventType string) []outboxRow {
	rows, err := f.pool.Query(context.Background(), `SELECT event_type, payload FROM outbox WHERE event_type=$1 ORDER BY created_at, id`, eventType)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			f.t.Fatal(err)
		}
		var env events.Envelope
		_ = json.Unmarshal(raw, &env)
		var data map[string]any
		_ = json.Unmarshal(env.Data, &data)
		if env.Source != "kar" || env.TenantID != f.tenant {
			f.t.Fatalf("envelope %+v", env)
		}
		out = append(out, outboxRow{Type: typ, Data: data})
	}
	return out
}

func (f *fixture) processed(id string) bool {
	var ok bool
	_ = f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, id).Scan(&ok)
	return ok
}

func TestEventGeneratesIRNAndOutbox(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.payload("INV/26-27/00042", "sale", f.store)
	env, err := f.deliver(events.InvoiceCreated, p)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := f.engine.EInvoice(ctx, f.tenant, p.InvoiceID)
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	wantIRN := gsp.IRNFor(f.seller, "2026-27", "INV", "INV/26-27/00042")
	if rec.Status != EInvGenerated || rec.IRN != wantIRN || len(rec.AckNo) != 15 || rec.SignedQR == "" || rec.TriggeredBy != "event" ||
		rec.GSTIN != f.seller || rec.SupplyType != "B2B" || rec.Provider != "simulator" || rec.QR == nil || rec.QR.Irn != wantIRN {
		t.Fatalf("record %+v", rec)
	}
	var sellerDtls struct {
		SellerDtls EInvParty `json:"SellerDtls"`
		BuyerDtls  EInvBuyer `json:"BuyerDtls"`
	}
	_ = json.Unmarshal(rec.Payload, &sellerDtls)
	if s := sellerDtls.SellerDtls; s.LglNm != "Acme Traders Pvt Ltd" || s.TrdNm != "Acme Mart" || s.Loc != "Pune" || s.Pin != 411005 || s.Ph != "9876543210" {
		t.Fatalf("seller from registration: %+v", s)
	}
	if b := sellerDtls.BuyerDtls; b.Pin != 560001 || b.Loc != "Bengaluru" || b.Gstin != f.buyer {
		t.Fatalf("buyer from bahi: %+v", b)
	}
	ob := f.outbox(events.EInvoiceGenerated)
	if len(ob) != 1 || ob[0].Data["status"] != "generated" || ob[0].Data["irn"] != wantIRN || ob[0].Data["invoice_id"] != p.InvoiceID ||
		ob[0].Data["ack_no"] != rec.AckNo || ob[0].Data["signed_qr"] == "" || ob[0].Data["gstin"] != f.seller {
		t.Fatalf("outbox %+v", ob)
	}
	if !f.processed(env.ID) {
		t.Fatal("event must be marked processed")
	}
	// Redelivery is a no-op.
	if err := f.engine.OnInvoiceEvent(ctx, env); err != nil {
		t.Fatal(err)
	}
	if n := len(f.outbox(events.EInvoiceGenerated)); n != 1 {
		t.Fatalf("redelivery published again: %d", n)
	}
	// Manual generate of the same invoice returns the stored IRN.
	inv := invoiceFromEvent(p)
	again, err := f.engine.GenerateEInvoice(ctx, GenerateRequest{TenantID: f.tenant, Invoice: inv, By: "u1"})
	if err != nil || again.IRN != wantIRN {
		t.Fatalf("manual regenerate: %v %+v", err, again)
	}
	// Another event for the same invoice (new id, e.g. a replay): the simulator answers 2150, kar keeps the IRN.
	if _, err := f.engine.DB.Exec(ctx, `UPDATE einvoices SET status='pending' WHERE invoice_id=$1`, p.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deliver(events.InvoiceCreated, p); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = f.engine.EInvoice(ctx, f.tenant, p.InvoiceID)
	if rec.Status != EInvGenerated || rec.IRN != wantIRN {
		t.Fatalf("2150 recovery: %+v", rec)
	}
}

func TestEventSkipsAndFailures(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// B2C: never e-invoiced.
	b2c := f.payload("INV/26-27/00100", "sale", f.store)
	b2c.PartyGSTIN, b2c.PartyID = "", ""
	env, err := f.deliver(events.InvoiceCreated, b2c)
	if err != nil || !f.processed(env.ID) {
		t.Fatalf("b2c: %v", err)
	}
	if _, ok, _ := f.engine.EInvoice(ctx, f.tenant, b2c.InvoiceID); ok {
		t.Fatal("B2C must not be e-invoiced")
	}
	// Registration below the threshold.
	small := f.payload("INV/26-27/00101", "sale", f.storeB2C)
	if _, err := f.deliver(events.InvoiceCreated, small); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.engine.EInvoice(ctx, f.tenant, small.InvoiceID); ok {
		t.Fatal("non-applicable registration must not be e-invoiced")
	}
	// Purchases are not e-invoiced by the buyer.
	pur := f.payload("PUR/1", "purchase", f.store)
	if _, err := f.deliver(events.InvoiceCreated, pur); err != nil {
		t.Fatal(err)
	}
	// A 4-digit HSN fails permanently: stored as failed with the reason, published, not retried.
	bad := f.payload("INV/26-27/00102", "sale", f.store)
	bad.Lines[0].HSN = "9403"
	env, err = f.deliver(events.InvoiceCreated, bad)
	if err != nil {
		t.Fatalf("permanent failures are acked: %v", err)
	}
	rec, ok, _ := f.engine.EInvoice(ctx, f.tenant, bad.InvoiceID)
	if !ok || rec.Status != EInvFailed || !strings.Contains(rec.ErrorMessage, "4 digits") || !f.processed(env.ID) {
		t.Fatalf("failed record %+v", rec)
	}
	ob := f.outbox(events.EInvoiceGenerated)
	if len(ob) != 1 || ob[0].Data["status"] != "failed" || !strings.Contains(ob[0].Data["error"].(string), "4 digits") {
		t.Fatalf("failed outbox %+v", ob)
	}
	// A second event for the failed invoice does not retry it.
	if _, err := f.deliver(events.InvoiceCreated, bad); err != nil {
		t.Fatal(err)
	}
	if n := len(f.outbox(events.EInvoiceGenerated)); n != 1 {
		t.Fatalf("failed invoice retried by event: %d", n)
	}
	// An IRP rejection (future date per the IRP clock) is stored with the NIC code.
	fut := f.payload("INV/26-27/00103", "sale", f.store)
	fut.InvoiceDate = "2026-10-30"
	if _, err := f.deliver(events.InvoiceCreated, fut); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = f.engine.EInvoice(ctx, f.tenant, fut.InvoiceID)
	if rec.Status != EInvFailed || rec.ErrorCode != gsp.CodeFutureDate {
		t.Fatalf("IRP rejection %+v", rec)
	}
}

// downGSP fails every call as unavailable.
type downGSP struct{ gsp.Client }

func (downGSP) Name() string { return "down" }
func (downGSP) GenerateIRN(context.Context, gsp.Credentials, any) (gsp.IRNResult, error) {
	return gsp.IRNResult{}, &gsp.Error{Kind: gsp.KindUnavailable, Code: gsp.CodeProviderDown, Message: "gsp timeout"}
}

func TestTransientFailuresRetryThenFail(t *testing.T) {
	f := newFixture(t)
	f.engine.GSP = downGSP{}
	p := f.payload("INV/26-27/00200", "sale", f.store)
	env, _ := events.New(events.InvoiceCreated, "aadesh", f.tenant, p.StoreID, p)
	for i := 1; i < 3; i++ {
		err := f.engine.OnInvoiceEvent(context.Background(), env)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != 503 {
			t.Fatalf("attempt %d must ask for redelivery: %v", i, err)
		}
		rec, _, _ := f.engine.EInvoice(context.Background(), f.tenant, p.InvoiceID)
		if rec.Status != EInvPending || rec.Attempts != i {
			t.Fatalf("attempt %d: %+v", i, rec)
		}
	}
	if err := f.engine.OnInvoiceEvent(context.Background(), env); err != nil {
		t.Fatalf("last attempt is acked: %v", err)
	}
	rec, _, _ := f.engine.EInvoice(context.Background(), f.tenant, p.InvoiceID)
	if rec.Status != EInvFailed || rec.Attempts != 3 || !f.processed(env.ID) {
		t.Fatalf("after max attempts: %+v", rec)
	}
	if ob := f.outbox(events.EInvoiceGenerated); len(ob) != 1 || ob[0].Data["status"] != "failed" {
		t.Fatalf("one failed event: %+v", ob)
	}
}

func TestCreditNoteAndCancelWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orig := f.payload("INV/26-27/00300", "sale", f.store)
	if _, err := f.deliver(events.InvoiceCreated, orig); err != nil {
		t.Fatal(err)
	}
	f.dir.invoices[orig.InvoiceID] = invoiceFromEvent(orig)
	cn := f.payload("CN/26-27/00001", "sale_return", f.store)
	cn.RefInvoiceID = orig.InvoiceID
	if _, err := f.deliver(events.NoteCreated, cn); err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := f.engine.EInvoice(ctx, f.tenant, cn.InvoiceID)
	if !ok || rec.Status != EInvGenerated || rec.DocType != "CRN" || rec.IRN != gsp.IRNFor(f.seller, "2026-27", "CRN", "CN/26-27/00001") {
		t.Fatalf("credit note %+v", rec)
	}
	var pl EInvoice
	_ = json.Unmarshal(rec.Payload, &pl)
	if pl.RefDtls == nil || pl.RefDtls.PrecDocDtls[0].InvNo != "INV/26-27/00300" {
		t.Fatalf("RefDtls %+v", pl.RefDtls)
	}

	// Within 24 hours: cancelled, published.
	f.clock.Add(2 * time.Hour)
	if _, err := f.engine.CancelEInvoice(ctx, f.tenant, cn.InvoiceID, 9, "x", "u1"); err == nil {
		t.Fatal("bad reason code must fail")
	}
	c, err := f.engine.CancelEInvoice(ctx, f.tenant, cn.InvoiceID, 2, "wrong quantity", "u1")
	if err != nil || c.Status != EInvCancelled || c.CancelledAt == nil || *c.CancelReason != 2 {
		t.Fatalf("cancel: %v %+v", err, c)
	}
	ob := f.outbox(events.EInvoiceGenerated)
	last := ob[len(ob)-1]
	if last.Data["status"] != "cancelled" || last.Data["invoice_id"] != cn.InvoiceID || last.Data["cancelled_at"] == "" {
		t.Fatalf("cancel event %+v", last)
	}
	if _, err := f.engine.GenerateEInvoice(ctx, GenerateRequest{TenantID: f.tenant, Invoice: invoiceFromEvent(cn), By: "u1"}); err == nil {
		t.Fatal("a cancelled document number cannot be registered again")
	}

	// After 24 hours: refused.
	f.clock.Add(23 * time.Hour) // 25 h after the original's acknowledgement
	_, err = f.engine.CancelEInvoice(ctx, f.tenant, orig.InvoiceID, 3, "order cancelled", "u1")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 409 || !strings.Contains(he.Message, "24 hours") {
		t.Fatalf("cancel after 24h: %v", err)
	}
	list, total, err := f.engine.ListEInvoices(ctx, f.tenant, EInvoiceFilter{Status: EInvGenerated, From: "2026-10-01", To: "2026-10-31"})
	if err != nil || total != 1 || list[0].InvoiceID != orig.InvoiceID || list[0].Payload != nil {
		t.Fatalf("list %v %d %+v", err, total, list)
	}
}

func TestEWayFromIRNLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.payload("INV/26-27/00400", "sale", f.store)
	if _, err := f.deliver(events.InvoiceCreated, p); err != nil {
		t.Fatal(err)
	}
	inv := invoiceFromEvent(p)
	ei, _, _ := f.engine.EInvoice(ctx, f.tenant, p.InvoiceID)

	if _, err := f.engine.GenerateEWay(ctx, f.tenant, inv, Transport{Mode: "1", DistanceKM: 840, VehicleNo: "MH12AB12"}, "u1"); err == nil {
		t.Fatal("bad vehicle must fail")
	}
	rec, err := f.engine.GenerateEWay(ctx, f.tenant, inv, Transport{Mode: "1", DistanceKM: 840, VehicleNo: "22 BH 1234 AA", TransporterName: "Speedy"}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != EWBGenerated || len(rec.EWBNo) != 12 || rec.IRN != ei.IRN || rec.VehicleNo != "22BH1234AA" || rec.ValidUpto == nil ||
		!rec.ValidUpto.Equal(time.Date(2026, 10, 29, 23, 59, 59, 0, gsp.IST)) {
		t.Fatalf("eway %+v", rec)
	}
	ob := f.outbox(events.EWayGenerated)
	if len(ob) != 1 || ob[0].Data["status"] != "generated" || ob[0].Data["ewb_no"] != rec.EWBNo || ob[0].Data["distance_km"] != 840.0 {
		t.Fatalf("eway outbox %+v", ob)
	}
	if _, err := f.engine.GenerateEWay(ctx, f.tenant, inv, Transport{Mode: "1", DistanceKM: 840, VehicleNo: "MH12AB1234"}, "u1"); err == nil {
		t.Fatal("second active e-way bill must be refused")
	}
	// The IRN cannot be cancelled while the e-way bill is active.
	if _, err := f.engine.CancelEInvoice(ctx, f.tenant, p.InvoiceID, 1, "duplicate", "u1"); err == nil || !strings.Contains(err.Error(), rec.EWBNo) {
		t.Fatalf("irn cancel with ewb: %v", err)
	}
	pb, err := f.engine.UpdatePartB(ctx, f.tenant, p.InvoiceID, PartBInput{Mode: "road", VehicleNo: "KA01AB1234", FromPlace: "Hubli",
		FromState: "29", ReasonCode: "1", ReasonRemark: "breakdown"}, "u1")
	if err != nil || pb.VehicleNo != "KA01AB1234" || !pb.ValidUpto.Equal(*rec.ValidUpto) {
		t.Fatalf("part-b %v %+v", err, pb)
	}
	f.clock.Add(time.Hour)
	c, err := f.engine.CancelEWay(ctx, f.tenant, p.InvoiceID, 2, "order cancelled", "u1")
	if err != nil || c.Status != EWBCancelled {
		t.Fatalf("cancel eway %v %+v", err, c)
	}
	statuses := []string{}
	for _, r := range f.outbox(events.EWayGenerated) {
		statuses = append(statuses, r.Data["status"].(string))
	}
	if strings.Join(statuses, ",") != "generated,updated,cancelled" {
		t.Fatalf("eway events %v", statuses)
	}
	var hist []map[string]any
	_ = json.Unmarshal(c.History, &hist)
	if len(hist) != 3 {
		t.Fatalf("history %s", c.History)
	}
	if _, err := f.engine.CancelEInvoice(ctx, f.tenant, p.InvoiceID, 1, "duplicate", "u1"); err != nil {
		t.Fatalf("irn cancel after ewb cancel: %v", err)
	}
}

func TestStandaloneEWayAndExtend(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A B2C sale has no IRN: the bill is generated from the standalone NIC payload.
	p := f.payload("INV/26-27/00500", "sale", f.store)
	p.PartyGSTIN = ""
	inv := invoiceFromEvent(p)
	rec, err := f.engine.GenerateEWay(ctx, f.tenant, inv, Transport{Mode: "1", DistanceKM: 150, VehicleNo: "MH12AB1234", VehicleType: "R"}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.IRN != "" || rec.Status != EWBGenerated || !rec.ValidUpto.Equal(time.Date(2026, 10, 25, 23, 59, 59, 0, gsp.IST)) {
		t.Fatalf("standalone %+v", rec)
	}
	in := ExtendInput{FromPlace: "Kolhapur", FromState: "27", FromPincode: 416001, RemainingDistanceKM: 250, ReasonCode: 1, Remark: "flood"}
	if _, err := f.engine.ExtendEWay(ctx, f.tenant, p.InvoiceID, in, "u1"); err == nil {
		t.Fatal("extension is only allowed near expiry")
	}
	f.clock.Add(rec.ValidUpto.Sub(f.clock.Now()) - time.Hour) // one hour before expiry
	ext, err := f.engine.ExtendEWay(ctx, f.tenant, p.InvoiceID, in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	// 250 km → 2 days from the extension (25 Oct) → 27 Oct 23:59:59.
	if ext.Extensions != 1 || !ext.ValidUpto.Equal(time.Date(2026, 10, 27, 23, 59, 59, 0, gsp.IST)) {
		t.Fatalf("extended %+v", ext)
	}
	// Cancel after 24 hours of generation is refused.
	if _, err := f.engine.CancelEWay(ctx, f.tenant, p.InvoiceID, 2, "late", "u1"); err == nil {
		t.Fatal("cancel after 24h must fail")
	}
	// 180-day rule.
	old := f.payload("INV/26-27/00501", "sale", f.store)
	old.InvoiceDate = "2026-04-01"
	if _, err := f.engine.GenerateEWay(ctx, f.tenant, invoiceFromEvent(old), Transport{Mode: "1", DistanceKM: 10, VehicleNo: "MH12AB1234"}, "u1"); err == nil ||
		!strings.Contains(err.Error(), "180") {
		t.Fatalf("180-day rule: %v", err)
	}
	list, total, err := f.engine.ListEWays(ctx, f.tenant, EInvoiceFilter{Status: EWBGenerated})
	if err != nil || total != 1 || list[0].EWBNo != rec.EWBNo {
		t.Fatalf("list %v %d", err, total)
	}
}
