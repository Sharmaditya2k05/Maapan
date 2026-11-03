package kpi

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/maapan/buddhi/internal/testdb"
	"github.com/maapan/mool/events"
)

const (
	tenant = "11111111-1111-1111-1111-111111111111"
	store1 = "22222222-2222-2222-2222-222222222222"
	itemA  = "33333333-3333-3333-3333-333333333333"
	day    = "2026-11-02"
)

func newService(t *testing.T, rdb *redis.Client) *Service {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, IST)
	return &Service{DB: testdb.New(t), Redis: rdb, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
}

func env(t *testing.T, typ string, p events.InvoicePayload) events.Envelope {
	t.Helper()
	e, err := events.New(typ, "aadesh", tenant, p.StoreID, p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func sale(id string, total, taxable, tax, qty int64) events.InvoicePayload {
	return events.InvoicePayload{InvoiceID: id, StoreID: store1, DocType: "sale", Number: "S-" + id[:4], InvoiceDate: day,
		TaxablePaise: taxable, CGSTPaise: tax / 2, SGSTPaise: tax - tax/2, GrandTotalPaise: total, CreatedBy: "u1",
		Lines:   []events.InvoiceLine{{ItemID: itemA, Name: "Tata Salt", Qty: float64(qty), TaxablePaise: taxable, TotalPaise: total}},
		Tenders: []events.Tender{{Mode: "cash", AmountPaise: total}}}
}

func mustDash(t *testing.T, s *Service, st string) *Dashboard {
	t.Helper()
	d, err := s.Dashboard(context.Background(), tenant, st, day)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSaleReturnReducesKPIs(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	inv := sale("aaaaaaaa-0000-0000-0000-000000000001", 11800, 10000, 1800, 10)
	if err := s.OnInvoiceCreated(ctx, env(t, events.InvoiceCreated, inv)); err != nil {
		t.Fatal(err)
	}
	note := events.InvoicePayload{InvoiceID: "bbbbbbbb-0000-0000-0000-000000000001", StoreID: store1, DocType: "sale_return",
		Number: "CN-1", InvoiceDate: day, RefInvoiceID: inv.InvoiceID, TaxablePaise: 2000, CGSTPaise: 180, SGSTPaise: 180,
		GrandTotalPaise: 2360, CreatedBy: "u1",
		Lines:   []events.InvoiceLine{{ItemID: itemA, Name: "Tata Salt", Qty: 2, TaxablePaise: 2000, TotalPaise: 2360}},
		Tenders: []events.Tender{{Mode: "cash", AmountPaise: 2360}}}
	e := env(t, events.NoteCreated, note)
	if err := s.OnNoteCreated(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.OnNoteCreated(ctx, e); err != nil { // redelivery is a no-op
		t.Fatal(err)
	}
	d := mustDash(t, s, AllStores)
	k := d.Today
	if k.SalesPaise != 11800-2360 || k.TaxPaise != 1800-360 || k.Bills != 1 || k.Returns != 1 || k.ReturnsPaise != 2360 {
		t.Fatalf("after return: %+v", k)
	}
	if len(d.TopItems) != 1 || d.TopItems[0].SalesPaise != 11800-2360 || d.TopItems[0].Qty != 8 {
		t.Fatalf("top items: %+v", d.TopItems)
	}
	if d.PaymentMix["cash"] != 11800-2360 {
		t.Fatalf("payment mix: %+v", d.PaymentMix)
	}
	if d.Source != "postgres" {
		t.Fatalf("source %s", d.Source)
	}

	// Purchase returns do not touch sales.
	pr := note
	pr.InvoiceID, pr.DocType = "bbbbbbbb-0000-0000-0000-000000000002", "purchase_return"
	if err := s.OnNoteCreated(ctx, env(t, events.NoteCreated, pr)); err != nil {
		t.Fatal(err)
	}
	// Cancelling the credit note adds the sale back.
	if err := s.OnInvoiceCancelled(ctx, env(t, events.InvoiceCancelled, note)); err != nil {
		t.Fatal(err)
	}
	k = mustDash(t, s, store1).Today
	if k.SalesPaise != 11800 || k.Returns != 0 || k.ReturnsPaise != 0 || k.Cancelled != 0 || k.Bills != 1 {
		t.Fatalf("after cancelling the note: %+v", k)
	}
	var seq int64
	if err := s.DB.QueryRow(ctx, `SELECT seq FROM kpi_versions WHERE tenant_id=$1 AND store_key='all' AND day=$2`, tenant, day).Scan(&seq); err != nil || seq != 3 {
		t.Fatalf("version %d %v (sale, return, cancel)", seq, err)
	}
	r, _, err := s.Range(ctx, tenant, AllStores, day, day)
	if err != nil || r.SalesPaise != 11800 {
		t.Fatalf("range %+v %v", r, err)
	}
}

func TestUseRedis(t *testing.T) {
	cases := []struct {
		have      bool
		rseq, pg  int64
		want      bool
		situation string
	}{
		{true, 5, 5, true, "in sync"},
		{true, 4, 5, false, "a mirror after commit failed"},
		{true, 6, 5, false, "impossible ahead state"},
		{false, 0, 5, false, "key expired or never written"},
		{true, 0, 0, false, "no activity: Postgres zeros are cheap"},
	}
	for _, c := range cases {
		if got := UseRedis(c.have, c.rseq, c.pg); got != c.want {
			t.Errorf("%s: got %v", c.situation, got)
		}
	}
}

// With a real Redis (TEST_REDIS_URL), a failed mirror must not leave the
// dashboard serving stale numbers, and the dashboard repairs the keys.
func TestRedisMirrorNeverServesStale(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set (e.g. redis://localhost:6379/15)")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	ctx := context.Background()
	s := newService(t, rdb)
	t.Cleanup(func() {
		for _, st := range []string{store1, AllStores} {
			rdb.Del(ctx, kpiKey(tenant, st, day), topKey(tenant, st, day), payKey(tenant, st, day))
		}
		rdb.Del(ctx, nameKey(tenant))
	})
	for _, st := range []string{store1, AllStores} {
		rdb.Del(ctx, kpiKey(tenant, st, day), topKey(tenant, st, day), payKey(tenant, st, day))
	}

	if err := s.OnInvoiceCreated(ctx, env(t, events.InvoiceCreated, sale("aaaaaaaa-0000-0000-0000-000000000011", 1000, 1000, 0, 1))); err != nil {
		t.Fatal(err)
	}
	if d := mustDash(t, s, AllStores); d.Source != "redis" || d.Today.SalesPaise != 1000 {
		t.Fatalf("first read: %s %+v", d.Source, d.Today)
	}
	// Simulate a lost mirror: commit to Postgres with Redis "down".
	s.Redis = nil
	if err := s.OnInvoiceCreated(ctx, env(t, events.InvoiceCreated, sale("aaaaaaaa-0000-0000-0000-000000000012", 500, 500, 0, 1))); err != nil {
		t.Fatal(err)
	}
	s.Redis = rdb
	// Next event mirrors fine, but Redis missed one version: it must not be trusted.
	if err := s.OnInvoiceCreated(ctx, env(t, events.InvoiceCreated, sale("aaaaaaaa-0000-0000-0000-000000000013", 200, 200, 0, 1))); err != nil {
		t.Fatal(err)
	}
	d := mustDash(t, s, AllStores)
	if d.Today.SalesPaise != 1700 || d.Source != "postgres" {
		t.Fatalf("stale mirror served: %s %+v", d.Source, d.Today)
	}
	// The read rebuilt Redis; the next read is from Redis and correct.
	d = mustDash(t, s, AllStores)
	if d.Source != "redis" || d.Today.SalesPaise != 1700 || d.PaymentMix["cash"] != 1700 || d.TopItems[0].SalesPaise != 1700 {
		t.Fatalf("after rebuild: %s %+v %+v %+v", d.Source, d.Today, d.PaymentMix, d.TopItems)
	}
}
