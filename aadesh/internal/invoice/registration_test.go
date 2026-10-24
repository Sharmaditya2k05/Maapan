package invoice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/taraju/aadesh/internal/clients"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

const (
	gstinMH = "27AAPFU0939F1ZV"
	gstinKA = "29AAPFU0939F1ZR"
)

// regStore registers a store with the fake pehchaan under a registration.
func regStore(f *fakes, tenant, state, gstin, regID string, primary bool) string {
	id := uuid.NewString()
	f.stores[id] = clients.Store{ID: id, TenantID: tenant, Name: "Store " + state, Code: "S" + state, StateCode: state, GSTIN: gstin,
		RegistrationID: regID, LegalName: "Sharma Traders", City: "City", PIN: "400001", RegistrationPrimary: primary}
	return id
}

func simpleLine(price int64) LineInput {
	l := LineInput{Name: "Notebook"}
	l.HSN, l.Qty, l.UnitPricePaise, l.TaxRateBP = "4820", 1, price, 1800
	return l
}

func TestNumberingPerRegistration(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, regMH, regKA := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mh := regStore(f, tenant, "27", gstinMH, regMH, true)
	ka := regStore(f, tenant, "29", gstinKA, regKA, false)
	sale := func(store string) *Invoice {
		t.Helper()
		inv, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}, CreateInput{DocType: DocSale, Lines: []LineInput{simpleLine(10000)}})
		if err != nil {
			t.Fatalf("sale at %s: %v", store, err)
		}
		return inv
	}

	// Two GSTINs both issue INV/26-27/00001: one series per registration.
	a1, k1 := sale(mh), sale(ka)
	if a1.Number != "INV/26-27/00001" || k1.Number != "INV/26-27/00001" {
		t.Fatalf("first numbers: %s %s", a1.Number, k1.Number)
	}
	if deref(a1.RegistrationID) != regMH || deref(k1.RegistrationID) != regKA || deref(k1.StoreGSTIN) != gstinKA || k1.StoreState != "29" {
		t.Fatalf("registration on documents: %+v / %+v", a1, k1)
	}
	if a2, k2 := sale(mh), sale(ka); a2.Number != "INV/26-27/00002" || k2.Number != "INV/26-27/00002" {
		t.Fatalf("second numbers: %s %s", a2.Number, k2.Number)
	}
	var keys []string
	rows, _ := pool.Query(ctx, `SELECT DISTINCT registration_key::text FROM invoices WHERE tenant_id=$1 ORDER BY 1`, tenant)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) != 2 || keys[0] != NilUUID {
		t.Fatalf("primary keeps the nil-key series, the other its own: %v", keys)
	}

	// Offline blocks come from the store's registration series too.
	blk, err := s.ReserveBlock(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: ka}, "", DocSale, "pos-ka", 10)
	if err != nil || blk.StartNo != 3 || blk.Prefix != "INV" {
		t.Fatalf("KA block: %+v %v", blk, err)
	}
	off, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: ka}, CreateInput{DocType: DocSale,
		Lines: []LineInput{simpleLine(10000)}, ClientUUID: "ka-off-1", DeviceID: "pos-ka", Number: blk.StartNo})
	if err != nil || off.Number != "INV/26-27/00003" {
		t.Fatalf("offline KA bill: %+v %v", off, err)
	}
	var offKey string
	_ = pool.QueryRow(ctx, `SELECT registration_key::text FROM invoices WHERE id=$1`, off.ID).Scan(&offKey)
	if offKey != regKA {
		t.Fatalf("offline bill registration key %s", offKey)
	}
	if rel, err := s.ReleaseBlock(ctx, &auth.Principal{Internal: true, TenantID: tenant}, blk.ID, blk.StartNo); err != nil || rel.Prefix != "INV" {
		t.Fatalf("release: %+v %v", rel, err)
	}
	if k := sale(ka); k.Number != "INV/26-27/00013" {
		t.Fatalf("after a block of 10 KA continues at 13: %s", k.Number)
	}

	// The owner switches the primary to Karnataka: each GSTIN keeps its own series.
	s.Dir = clients.NewDirectory(s.Dir.Pehchaan, s.Dir.Bahi, s.Dir.Bhandar, s.Dir.Lenden) // drop the store cache
	st := f.stores[mh]
	st.RegistrationPrimary = false
	f.stores[mh] = st
	st = f.stores[ka]
	st.RegistrationPrimary = true
	f.stores[ka] = st
	if a3, k3 := sale(mh), sale(ka); a3.Number != "INV/26-27/00003" || k3.Number != "INV/26-27/00014" {
		t.Fatalf("after primary switch: %s %s", a3.Number, k3.Number)
	}

	// A store-specific prefix still wins inside its registration.
	if err := s.SetNumbering(ctx, tenant, NumberingSetting{StoreID: ka, DocType: DocSale, Prefix: "BLR"}); err != nil {
		t.Fatal(err)
	}
	if k := sale(ka); k.Number != "BLR/26-27/00001" || deref(k.RegistrationID) != regKA {
		t.Fatalf("store prefix: %s", k.Number)
	}

	// Internal list filters.
	byReg, n, err := List(ctx, pool, tenant, Filter{RegistrationID: regKA, IncludeLines: true, Page: pageAll})
	if err != nil || n != 6 || len(byReg[0].Lines) != 1 {
		t.Fatalf("filter registration_id: n=%d %v", n, err)
	}
	if _, n, _ := List(ctx, pool, tenant, Filter{StoreGSTIN: "27aapfu0939f1zv", Page: pageAll}); n != 3 {
		t.Fatalf("filter store_gstin: n=%d", n)
	}
}

var pageAll = httpx.Page{Limit: 500}

// TestPrimaryContinuesExistingSeries: a tenant billed before registrations
// existed; its primary keeps counting and a branch GSTIN that shared the old
// series starts after its own last number there.
func TestPrimaryContinuesExistingSeries(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, regMH, regKA := uuid.NewString(), uuid.NewString(), uuid.NewString()
	legacyStore := uuid.NewString()
	// Legacy state: the shared series is at 42 and the Karnataka branch issued 00007 from it.
	if _, err := pool.Exec(ctx, `INSERT INTO number_series (tenant_id, store_id, doc_type, fy, prefix, next_no) VALUES ($1,$2,'sale','26-27','INV',42)`, tenant, NilUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invoices (tenant_id, store_id, doc_type, number, seq_no, fy, invoice_date, store_state, store_gstin,
		place_of_supply, inter_state, taxable_paise, grand_total_paise, created_by)
		VALUES ($1,$2,'sale','INV/26-27/00007',7,'26-27','2026-06-01','29',$3,'29',false,100,100,'legacy')`, tenant, legacyStore, gstinKA); err != nil {
		t.Fatal(err)
	}
	mh := regStore(f, tenant, "27", gstinMH, regMH, true)
	ka := regStore(f, tenant, "29", gstinKA, regKA, false)
	a, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: mh}, CreateInput{DocType: DocSale, Lines: []LineInput{simpleLine(500)}})
	if err != nil || a.Number != "INV/26-27/00042" {
		t.Fatalf("primary continues the existing series: %+v %v", a, err)
	}
	k, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: ka}, CreateInput{DocType: DocSale, Lines: []LineInput{simpleLine(500)}})
	if err != nil || k.Number != "INV/26-27/00008" {
		t.Fatalf("branch GSTIN continues after its last shared number: %+v %v", k, err)
	}
	// A pre-registration pehchaan (no registration_id) stays on the shared series.
	old, _, err := s.Create(ctx, &auth.Principal{Internal: true, TenantID: tenant, StoreID: uuid.NewString()}, CreateInput{DocType: DocSale, Lines: []LineInput{simpleLine(500)}})
	if err != nil || old.Number != "INV/26-27/00043" || old.RegistrationID != nil {
		t.Fatalf("old pehchaan: %+v %v", old, err)
	}
}

func TestITCEligibility(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant, supplier := uuid.NewString(), uuid.NewString()
	f.parties[supplier] = clients.Party{ID: supplier, Name: "Office Mart", Type: "supplier", StateCode: "27", GSTIN: gstinMH}
	store := regStore(f, tenant, "27", gstinMH, uuid.NewString(), true)
	p := &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}

	stock, car, bad := simpleLine(10000), simpleLine(500000), simpleLine(100)
	stock.Name, car.Name, bad.Name = "Paper", "Staff car", "Gift"
	car.ITCEligibility = ITCIneligible175
	bad.ITCEligibility = "maybe"
	if err := (&CreateInput{DocType: DocPurchase, PartyID: supplier, Lines: []LineInput{bad}}).Validate(); status(err) != 422 {
		t.Fatalf("unknown itc_eligibility must be 422: %v", err)
	}
	if err := (&CreateInput{DocType: DocSale, Lines: []LineInput{car}}).Validate(); status(err) != 422 {
		t.Fatalf("itc_eligibility on a sale must be 422: %v", err)
	}
	in := CreateInput{DocType: DocPurchase, PartyID: supplier, SupplierInvoiceNo: "OM/1", SupplierInvoiceDate: "2026-10-20", Lines: []LineInput{stock, car}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	pur, _, err := s.Create(ctx, p, in)
	if err != nil || pur.Lines[0].ITCEligibility != ITCEligible || pur.Lines[1].ITCEligibility != ITCIneligible175 {
		t.Fatalf("purchase lines: %+v %v", pur, err)
	}
	got, _ := s.Get(ctx, tenant, pur.ID)
	if got.Lines[0].ITCEligibility != ITCEligible || got.Lines[1].ITCEligibility != ITCIneligible175 {
		t.Fatalf("stored lines: %+v", got.Lines)
	}
	list, _, _ := List(ctx, pool, tenant, Filter{DocType: DocPurchase, IncludeLines: true, Page: pageAll})
	if len(list) != 1 || list[0].Lines[1].ITCEligibility != ITCIneligible175 {
		t.Fatalf("list lines: %+v", list)
	}
	var raw []byte
	_ = pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE event_type=$1 ORDER BY created_at DESC LIMIT 1`, events.InvoiceCreated).Scan(&raw)
	var env events.Envelope
	var pl events.InvoicePayload
	if err := json.Unmarshal(raw, &env); err != nil || env.Decode(&pl) != nil || pl.Lines[1].ITCEligibility != ITCIneligible175 || pl.Lines[0].ITCEligibility != ITCEligible {
		t.Fatalf("event lines: %s", raw)
	}

	// The debit note inherits the purchase line's eligibility; sales carry none.
	ret := LineInput{Name: "Staff car"}
	ret.Qty = 1
	dn, _, err := s.Create(ctx, p, CreateInput{DocType: DocPurchaseReturn, RefInvoiceID: pur.ID, Lines: []LineInput{ret}})
	if err != nil || dn.Lines[0].ITCEligibility != ITCIneligible175 {
		t.Fatalf("debit note: %+v %v", dn, err)
	}
	sl, _, err := s.Create(ctx, p, CreateInput{DocType: DocSale, Lines: []LineInput{stock}})
	if err != nil || sl.Lines[0].ITCEligibility != "" {
		t.Fatalf("sale line: %+v %v", sl, err)
	}
}

func TestEInvoiceAndEWayEvents(t *testing.T) {
	s, f, pool := newTestService(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	store := regStore(f, tenant, "27", gstinMH, uuid.NewString(), true)
	admin := &auth.Principal{Internal: true, TenantID: tenant, StoreID: store}
	inv, _, err := s.Create(ctx, admin, CreateInput{DocType: DocSale, Party: &PartyInput{Name: "Buyer", GSTIN: gstinKA}, Lines: []LineInput{simpleLine(100000)}})
	if err != nil {
		t.Fatal(err)
	}
	irn := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	einv := func(pl events.EInvoicePayload) events.Envelope {
		pl.InvoiceID, pl.Number, pl.GSTIN = inv.ID, inv.Number, gstinMH
		e, _ := events.New(events.EInvoiceGenerated, "kar", tenant, store, pl)
		return e
	}
	generated := einv(events.EInvoicePayload{Status: EInvoiceGenerated, IRN: irn, AckNo: "112610000012345", AckDate: "2026-10-23T11:00:00+05:30", SignedQR: "eyJhbGciOiJSUzI1NiJ9.qr.sig", GeneratedVia: "simulator"})
	if err := s.ApplyEInvoice(ctx, generated); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, tenant, inv.ID)
	if deref(got.IRN) != irn || deref(got.AckNo) != "112610000012345" || got.AckDate == nil || !got.AckDate.Equal(time.Date(2026, 10, 23, 5, 30, 0, 0, time.UTC)) ||
		deref(got.SignedQR) == "" || deref(got.EInvoiceStatus) != EInvoiceGenerated {
		t.Fatalf("after generated: %+v", got)
	}
	b, _ := json.Marshal(got)
	var asMap map[string]any
	_ = json.Unmarshal(b, &asMap)
	for _, k := range []string{"irn", "ack_no", "ack_date", "signed_qr", "einvoice_status", "registration_id"} {
		if _, ok := asMap[k]; !ok {
			t.Errorf("GET invoice JSON is missing %q", k)
		}
	}

	// A failure (say, of a cancel attempt) is recorded without losing the live IRN.
	if err := s.ApplyEInvoice(ctx, einv(events.EInvoicePayload{Status: EInvoiceFailed, Error: "cancel window over", ErrorCode: "2270"})); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(ctx, tenant, inv.ID); deref(got.EInvoiceStatus) != EInvoiceGenerated || deref(got.EInvoiceErrorCode) != "2270" {
		t.Fatalf("failure on a live IRN: %+v", got)
	}

	// Cancel is blocked while the IRN is live.
	if _, err := s.Cancel(ctx, admin, inv.ID, "wrong buyer"); status(err) != 409 {
		t.Fatalf("cancel with a live IRN must be 409: %v", err)
	}

	// e-Way bill: generated, then Part-B update; redelivery is a no-op.
	ewb := func(pl events.EWayPayload) events.Envelope {
		pl.InvoiceID, pl.Number = inv.ID, inv.Number
		e, _ := events.New(events.EWayGenerated, "kar", tenant, store, pl)
		return e
	}
	ewbGen := ewb(events.EWayPayload{Status: "generated", EWBNo: "331001234567", EWBDate: "2026-10-23T12:00:00+05:30", ValidUpto: "2026-10-24T23:59:00+05:30", DistanceKM: 150})
	if err := s.ApplyEWay(ctx, ewbGen); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEWay(ctx, ewb(events.EWayPayload{Status: "updated", EWBNo: "331001234567", VehicleNo: "MH12AB1234"})); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEWay(ctx, ewbGen); err != nil { // duplicate delivery
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, tenant, inv.ID)
	if deref(got.EWBNo) != "331001234567" || deref(got.EWBStatus) != "updated" || got.EWBDate == nil || got.EWBValidUpto == nil ||
		deref(got.EWBVehicleNo) != "MH12AB1234" || got.EWBDistanceKM == nil || *got.EWBDistanceKM != 150 {
		t.Fatalf("e-way: %+v", got)
	}
	if err := s.ApplyEWay(ctx, ewb(events.EWayPayload{Status: "cancelled", EWBNo: "331001234567"})); err != nil {
		t.Fatal(err)
	}

	// IRN cancelled: the invoice can now be cancelled. Redelivering the old
	// "generated" (same event id) or a stale copy of it (new id) changes nothing.
	if err := s.ApplyEInvoice(ctx, einv(events.EInvoicePayload{Status: EInvoiceCancelled, IRN: irn, CancelledAt: "2026-10-23T15:00:00+05:30"})); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEInvoice(ctx, generated); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEInvoice(ctx, einv(events.EInvoicePayload{Status: EInvoiceGenerated, IRN: irn, AckNo: "x"})); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, tenant, inv.ID)
	if deref(got.EInvoiceStatus) != EInvoiceCancelled || got.IRNCancelledAt == nil || deref(got.AckNo) != "112610000012345" || deref(got.EWBStatus) != "cancelled" {
		t.Fatalf("after IRN cancel: %+v", got)
	}
	if _, err := s.Cancel(ctx, admin, inv.ID, "wrong buyer"); err != nil {
		t.Fatalf("cancel after IRN cancellation: %v", err)
	}
	var processed int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM processed_events WHERE event_type IN ($1,$2)`, events.EInvoiceGenerated, events.EWayGenerated).Scan(&processed)
	if processed != 7 {
		t.Fatalf("processed events: %d", processed)
	}

	// Events for unknown invoices or mismatched numbers are dropped, not retried.
	e, _ := events.New(events.EInvoiceGenerated, "kar", tenant, store, events.EInvoicePayload{InvoiceID: uuid.NewString(), Status: EInvoiceGenerated, IRN: irn})
	if err := s.ApplyEInvoice(ctx, e); err != nil {
		t.Fatalf("unknown invoice: %v", err)
	}
	other, _, _ := s.Create(ctx, admin, CreateInput{DocType: DocSale, Lines: []LineInput{simpleLine(100)}})
	e, _ = events.New(events.EInvoiceGenerated, "kar", tenant, store, events.EInvoicePayload{InvoiceID: other.ID, Number: "INV/26-27/99999", Status: EInvoiceGenerated, IRN: irn})
	if err := s.ApplyEInvoice(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, tenant, other.ID); got.IRN != nil {
		t.Fatalf("mismatched number must be ignored: %+v", got)
	}
}
