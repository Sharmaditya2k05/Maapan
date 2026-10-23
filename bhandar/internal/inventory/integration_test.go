package inventory

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

	"github.com/taraju/bhandar/migrations"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// newTestService connects to TEST_DATABASE_URL, creates a fresh schema, runs
// the migrations in it and returns a Service. Skips when the URL is unset.
func newTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://taraju:taraju@localhost:5432/taraju_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("bhandar_test_%d", rand.Int63())
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
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	s := &Service{DB: pool, Log: slog.Default(), Now: func() time.Time { return now }, ExpiryAlertDays: 30}
	return s, pool
}

func envelope(t *testing.T, typ, tenantID, storeID string, data any) events.Envelope {
	t.Helper()
	e, err := events.New(typ, "test", tenantID, storeID, data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE event_type=$1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func balance(t *testing.T, s *Service, tenantID, storeID, itemID string) Balance {
	t.Helper()
	rows, _, err := s.Stock(context.Background(), tenantID, StockFilter{StoreID: storeID, ItemID: itemID, Page: httpx.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return Balance{}
	}
	return rows[0]
}

func TestStockEngineEndToEnd(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenantID, storeA, storeB := uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := s.Router()

	// Tenant created → default units seeded.
	if err := router[events.TenantCreated](ctx, envelope(t, events.TenantCreated, tenantID, storeA, events.TenantCreatedPayload{TenantID: tenantID, StoreID: storeA})); err != nil {
		t.Fatal(err)
	}
	units, err := s.Units(ctx, tenantID)
	if err != nil || len(units) != len(DefaultUnits) {
		t.Fatalf("units: %d %v", len(units), err)
	}

	// Item with an alternate unit and a re-order rule; barcode generated.
	it, err := s.CreateItem(ctx, tenantID, ItemInput{
		Name: "Parle-G 100g", HSN: "1905", Unit: "pcs", SalePricePaise: 1000, MRPPaise: 1000, PurchasePricePaise: 820,
		TaxRateBP: 1800, ReorderLevel: 24, ReorderQty: 96, AltUnits: []AltUnit{{Unit: "BOX", Factor: 48}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.Barcode != "2100000000012" || it.Unit != "PCS" || len(it.AltUnits) != 1 {
		t.Fatalf("created item: %+v", it)
	}
	if _, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Dup", Barcode: it.Barcode, TaxRateBP: 500}); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("duplicate barcode must be 409, got %v", err)
	}
	if _, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Old slab", TaxRateBP: 1200}); err == nil {
		t.Fatal("12 % is not a slab in force in 2026")
	}

	// Purchase: 2 BOX (= 96 PCS) for a taxable value of ₹787.20 → 96 @ ₹8.20.
	purchaseID := uuid.NewString()
	purchase := events.InvoicePayload{InvoiceID: purchaseID, StoreID: storeA, DocType: "purchase", Number: "PUR/26-27/00001", Lines: []events.InvoiceLine{
		{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "BOX", Qty: 2, TaxablePaise: 78720},
		{LineNo: 2, Name: "Free-text carry bag", Unit: "PCS", Qty: 1, TaxablePaise: 500}, // no item_id: ignored
	}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, purchase)); err != nil {
		t.Fatal(err)
	}
	b := balance(t, s, tenantID, storeA, it.ID)
	if b.Qty != 96 || b.AvgCostPaise != 820 || b.ValuePaise != 78720 {
		t.Fatalf("after purchase: %+v", b)
	}
	if n := outboxCount(t, pool, events.StockMoved); n != 1 {
		t.Fatalf("stock.moved events: %d", n)
	}

	// Sale of 10 at the average; redeliveries (same and new envelope id) change nothing.
	saleID := uuid.NewString()
	sale := events.InvoicePayload{InvoiceID: saleID, StoreID: storeA, DocType: "sale", Number: "INV/26-27/00001", Lines: []events.InvoiceLine{
		{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 10, TaxablePaise: 8475},
	}}
	env := envelope(t, events.InvoiceCreated, tenantID, storeA, sale)
	for i := 0; i < 2; i++ {
		if err := router[events.InvoiceCreated](ctx, env); err != nil {
			t.Fatal(err)
		}
	}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, sale)); err != nil {
		t.Fatal(err)
	}
	b = balance(t, s, tenantID, storeA, it.ID)
	if b.Qty != 86 || b.AvgCostPaise != 820 {
		t.Fatalf("after sale + redeliveries: %+v", b)
	}
	if n := outboxCount(t, pool, events.StockMoved); n != 2 {
		t.Fatalf("stock.moved after redelivery: %d", n)
	}

	// Second purchase re-averages: (86×820 + 96×940) / 182 = 883.
	p2 := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: storeA, DocType: "purchase", Number: "PUR/26-27/00002", Lines: []events.InvoiceLine{
		{LineNo: 1, ItemID: it.ID, Name: it.Name, Unit: "PCS", Qty: 96, TaxablePaise: 90240},
	}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, p2)); err != nil {
		t.Fatal(err)
	}
	if b = balance(t, s, tenantID, storeA, it.ID); b.Qty != 182 || b.AvgCostPaise != 883 {
		t.Fatalf("after second purchase: %+v", b)
	}

	// Cancelling the sale returns the 10 units at their original cost.
	if err := router[events.InvoiceCancelled](ctx, envelope(t, events.InvoiceCancelled, tenantID, storeA, events.InvoicePayload{InvoiceID: saleID, StoreID: storeA, DocType: "sale", Number: sale.Number})); err != nil {
		t.Fatal(err)
	}
	if b = balance(t, s, tenantID, storeA, it.ID); b.Qty != 192 || b.AvgCostPaise != 880 {
		t.Fatalf("after cancellation: %+v", b)
	}
	led, total, err := s.ItemLedger(ctx, tenantID, it.ID, LedgerFilter{StoreID: storeA, Page: httpx.Page{Limit: 50}})
	if err != nil || total != 4 || *led[0].RunningQty != 192 || led[0].Reason != ReasonSale || led[0].Qty != 10 {
		t.Fatalf("ledger: total=%d err=%v first=%+v", total, err, led[0])
	}

	// Re-order: dropping to the level raises one alert per day, not two.
	big := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: storeA, DocType: "sale", Number: "INV/26-27/00002", Lines: []events.InvoiceLine{
		{LineNo: 1, ItemID: it.ID, Name: it.Name, Qty: 170, TaxablePaise: 144067},
	}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, big)); err != nil {
		t.Fatal(err)
	}
	more := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: storeA, DocType: "sale", Number: "INV/26-27/00003", Lines: []events.InvoiceLine{
		{LineNo: 1, ItemID: it.ID, Name: it.Name, Qty: 1, TaxablePaise: 847},
	}}
	if err := router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenantID, storeA, more)); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, pool, events.StockLow); n != 1 {
		t.Fatalf("stock.low alerts today: %d", n)
	}
	sugg, err := s.Suggestions(ctx, tenantID, storeA)
	if err != nil || len(sugg) != 1 || sugg[0].OnHand != 21 || sugg[0].SuggestedQty != 96 {
		t.Fatalf("suggestions: %+v %v", sugg, err)
	}
	drafts, err := s.PurchaseDrafts(ctx, tenantID, PurchaseDraftInput{StoreID: storeA})
	if err != nil || len(drafts) != 1 || drafts[0].Items != 1 || drafts[0].Body["doc_type"] != "purchase" {
		t.Fatalf("purchase drafts: %+v %v", drafts, err)
	}
	if n, err := s.ReorderScan(ctx); err != nil || n != 0 {
		t.Fatalf("scan must not duplicate today's alert: %d %v", n, err)
	}

	// Transfer 2 PCS to store B: out at the average, in at the dispatch cost.
	p := &auth.Principal{Internal: true, TenantID: tenantID}
	tr, err := s.CreateTransfer(ctx, p, TransferInput{FromStoreID: storeA, ToStoreID: storeB, Lines: []TransferLineInput{{ItemID: it.ID, Qty: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, p, tr.ID, ActionReceive); err == nil {
		t.Fatal("receiving a draft must fail")
	}
	if tr, err = s.Transition(ctx, p, tr.ID, ActionDispatch); err != nil || tr.Status != TransferDispatched || tr.Lines[0].UnitCostPaise != 880 {
		t.Fatalf("dispatch: %+v %v", tr, err)
	}
	if tr, err = s.Transition(ctx, p, tr.ID, ActionReceive); err != nil || tr.Status != TransferReceived {
		t.Fatalf("receive: %+v %v", tr, err)
	}
	if b = balance(t, s, tenantID, storeB, it.ID); b.Qty != 2 || b.AvgCostPaise != 880 {
		t.Fatalf("store B after transfer: %+v", b)
	}
	if b = balance(t, s, tenantID, storeA, it.ID); b.Qty != 19 {
		t.Fatalf("store A after transfer: %+v", b)
	}

	// Opening stock is recorded once per store/item/batch.
	for i := 0; i < 2; i++ {
		res, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeB, AsOf: "2026-04-01", Lines: []OpeningLine{{ItemID: it.ID, Qty: 5, UnitCostPaise: 800}}})
		if err != nil || len(res.Errors) != 0 {
			t.Fatalf("opening: %+v %v", res, err)
		}
		if (i == 0 && res.Created != 1) || (i == 1 && res.Updated != 1) {
			t.Fatalf("opening run %d: %+v", i, res)
		}
	}
	if b = balance(t, s, tenantID, storeB, it.ID); b.Qty != 7 {
		t.Fatalf("store B after opening: %+v", b)
	}

	// Valuation and summary add up.
	v, err := s.Valuate(ctx, tenantID, "")
	if err != nil || v.TotalQty != 26 || v.TotalValuePaise != 19*880+ValuePaise(7, WeightedAverage(2, 880, 5, 800)) {
		t.Fatalf("valuation: %+v %v", v, err)
	}
	sum, err := s.Summary(ctx, tenantID, storeA)
	if err != nil || len(sum) != 1 || sum[0].OnHand != 19 || sum[0].ReorderLevel != 24 {
		t.Fatalf("summary: %+v %v", sum, err)
	}

	// Batches and expiry: a batch expiring in 10 days raises one alert.
	med, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Crocin 500", HSN: "3004", TaxRateBP: 500, TrackBatches: true, PurchasePricePaise: 1500})
	if err != nil {
		t.Fatal(err)
	}
	exp := s.Now().AddDate(0, 0, 10).Format(time.DateOnly)
	if res, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeA, Lines: []OpeningLine{{ItemID: med.ID, Qty: 30, BatchNo: "B2601", ExpiryDate: exp}}}); err != nil || res.Created != 1 {
		t.Fatalf("batch opening: %+v %v", res, err)
	}
	ex, err := s.Expiring(ctx, tenantID, storeA, 30)
	if err != nil || len(ex) != 1 || ex[0].DaysLeft != 10 || ex[0].Qty != 30 || ex[0].ValuePaise != 45000 {
		t.Fatalf("expiring: %+v %v", ex, err)
	}
	if n, err := s.ExpiryScan(ctx); err != nil || n != 1 {
		t.Fatalf("expiry scan: %d %v", n, err)
	}
	if n, err := s.ExpiryScan(ctx); err != nil || n != 0 {
		t.Fatalf("second expiry scan must be silent: %d %v", n, err)
	}
	draft, err := s.ReturnToVendor(ctx, tenantID, ex[0].BatchID, storeA, ReturnToVendorInput{})
	if err != nil || draft["doc_type"] != "purchase_return" {
		t.Fatalf("return to vendor: %v %v", draft, err)
	}
	batches, err := s.ItemBatches(ctx, tenantID, med.ID, storeA)
	if err != nil || len(batches) != 1 || batches[0].Qty != 30 {
		t.Fatalf("batches: %+v %v", batches, err)
	}

	// Adjustments need a note and move stock; expiry write-offs must be negative.
	if _, err := s.Adjust(ctx, tenantID, storeA, "u1", AdjustmentInput{Reason: ReasonExpiryWriteoff, Note: "x", Lines: []AdjustmentLine{{ItemID: med.ID, BatchNo: "B2601", Qty: 5}}}); err == nil {
		t.Fatal("positive expiry write-off must fail")
	}
	adj, err := s.Adjust(ctx, tenantID, storeA, "u1", AdjustmentInput{Reason: ReasonExpiryWriteoff, Note: "expired on shelf", Lines: []AdjustmentLine{{ItemID: med.ID, BatchNo: "B2601", Qty: -5}}})
	if err != nil || len(adj.Movements) != 1 || adj.Movements[0].UnitCostPaise != 1500 {
		t.Fatalf("write-off: %+v %v", adj, err)
	}
	if rows, _, err := s.Stock(ctx, tenantID, StockFilter{StoreID: storeA, ItemID: med.ID, ByBatch: true, Page: httpx.Page{Limit: 10}}); err != nil || len(rows) != 1 || rows[0].Qty != 25 || rows[0].BatchNo != "B2601" {
		t.Fatalf("batch stock: %+v %v", rows, err)
	}

	// Dead stock and aging see the medicine (never sold) but not Parle-G (sold today).
	dead, err := s.DeadStock(ctx, tenantID, storeA, 0)
	if err != nil || len(dead) != 1 || dead[0].ItemID != med.ID {
		t.Fatalf("dead stock: %+v %v", dead, err)
	}
	aging, err := s.StockAging(ctx, tenantID, storeA)
	if err != nil || aging[0].Items != 2 {
		t.Fatalf("aging: %+v %v", aging, err)
	}

	// Production: consume 4 Parle-G at the store's average, produce 1 hamper at the run's cost.
	hamper, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Gift hamper", TaxRateBP: 1800})
	if err != nil {
		t.Fatal(err)
	}
	prod := events.ProductionCompletedPayload{ProductionRunID: uuid.NewString(), StoreID: storeA,
		Consumed: []events.ProductionLine{{ItemID: it.ID, Qty: 4}}, Produced: []events.ProductionLine{{ItemID: hamper.ID, Qty: 1, UnitCostPaise: 5000}}}
	if err := router[events.ProductionCompleted](ctx, envelope(t, events.ProductionCompleted, tenantID, storeA, prod)); err != nil {
		t.Fatal(err)
	}
	if b = balance(t, s, tenantID, storeA, it.ID); b.Qty != 15 {
		t.Fatalf("after production consume: %+v", b)
	}
	if b = balance(t, s, tenantID, storeA, hamper.ID); b.Qty != 1 || b.AvgCostPaise != 5000 {
		t.Fatalf("after production output: %+v", b)
	}

	// Bulk items upsert by external_ref, then by SKU.
	res, err := s.BulkItems(ctx, tenantID, []BulkItem{
		{Name: "Tata Salt 1kg", SKU: "TS1", Unit: "PCS", SalePricePaise: 2800, MRPPaise: 2800, TaxRateBP: 0, ExternalRef: "I0001"},
		{Name: "Tata Salt 1 kg", SKU: "TS1", Unit: "PCS", SalePricePaise: 2900, MRPPaise: 2800, TaxRateBP: 0, ExternalRef: "I0001"}, // sale > MRP is clamped, not rejected
		{Name: "Bad", Unit: "PCS", TaxRateBP: 777},
	})
	if err != nil || res.Created != 1 || res.Updated != 1 || len(res.Errors) != 1 || res.Errors[0].Index != 2 {
		t.Fatalf("bulk items: %+v %v", res, err)
	}
	found, err := s.LookupItems(ctx, tenantID, LookupInput{Barcodes: []string{it.Barcode}})
	if err != nil || len(found) != 1 || found[0].ID != it.ID {
		t.Fatalf("lookup: %+v %v", found, err)
	}
	if lk, err := s.LookupByCode(ctx, tenantID, " ts1 "); err != nil || lk.Item.ExternalRef != "I0001" {
		t.Fatalf("lookup by sku: %+v %v", lk, err)
	}
}

func TestPosCatalogETagOverHTTP(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tenantID, storeID := uuid.NewString(), uuid.NewString()
	secret := "test-jwt-secret-that-is-32-chars-long!!"
	v := auth.NewVerifier(secret, "svc-secret")
	mux := http.NewServeMux()
	s.Routes(mux, v, false)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok, err := auth.Issue([]byte(secret), auth.Claims{TenantID: tenantID, StoreID: storeID, Role: auth.RoleCashier, Stores: []string{storeID},
		RegisteredClaims: auth.RegisteredClaimsFor("u1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
		var rdr *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		} else {
			rdr = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		for k, val := range headers {
			req.Header.Set(k, val)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var buf strings.Builder
		b := make([]byte, 64<<10)
		for {
			n, err := res.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				break
			}
		}
		return res, []byte(buf.String())
	}

	// A cashier may read but not create items.
	if res, _ := do("POST", "/v1/items", ItemInput{Name: "X", TaxRateBP: 500}, nil); res.StatusCode != 403 {
		t.Fatalf("cashier create: %d", res.StatusCode)
	}
	it, err := s.CreateItem(ctx, tenantID, ItemInput{Name: "Parle-G", TaxRateBP: 1800, SalePricePaise: 1000})
	if err != nil {
		t.Fatal(err)
	}
	res, body := do("GET", "/v1/items/pos-catalog?store_id="+storeID, nil, nil)
	if res.StatusCode != 200 || !strings.Contains(string(body), it.ID) {
		t.Fatalf("catalog: %d %s", res.StatusCode, body)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if res, _ = do("GET", "/v1/items/pos-catalog?store_id="+storeID, nil, map[string]string{"If-None-Match": etag}); res.StatusCode != 304 {
		t.Fatalf("unchanged catalog must be 304, got %d", res.StatusCode)
	}
	// Stock movement at the store changes the ETag.
	if _, err := s.OpeningStock(ctx, tenantID, OpeningInput{StoreID: storeID, Lines: []OpeningLine{{ItemID: it.ID, Qty: 3, UnitCostPaise: 800}}}); err != nil {
		t.Fatal(err)
	}
	if res, body = do("GET", "/v1/items/pos-catalog?store_id="+storeID, nil, map[string]string{"If-None-Match": etag}); res.StatusCode != 200 || !strings.Contains(string(body), `"on_hand":3`) {
		t.Fatalf("changed catalog: %d %s", res.StatusCode, body)
	}
	// Scanner lookup and label rendering.
	if res, body = do("GET", "/v1/barcodes/"+it.Barcode, nil, nil); res.StatusCode != 200 || !strings.Contains(string(body), it.ID) {
		t.Fatalf("barcode lookup: %d %s", res.StatusCode, body)
	}
	if res, body = do("GET", "/v1/items/"+it.ID+"/barcode.svg?module=3", nil, nil); res.StatusCode != 200 || !strings.HasPrefix(string(body), "<svg") {
		t.Fatalf("barcode svg: %d %.40s", res.StatusCode, body)
	}
	// Another store is out of reach for this cashier.
	if res, _ = do("GET", "/v1/items/pos-catalog?store_id="+uuid.NewString(), nil, nil); res.StatusCode != 403 {
		t.Fatalf("foreign store: %d", res.StatusCode)
	}
	// Internal lookup needs the service secret.
	if res, _ = do("POST", "/internal/v1/items/lookup", LookupInput{IDs: []string{it.ID}}, nil); res.StatusCode != 401 {
		t.Fatalf("internal without secret: %d", res.StatusCode)
	}
	if res, body = do("POST", "/internal/v1/items/lookup", LookupInput{IDs: []string{it.ID}}, map[string]string{auth.HeaderServiceSecret: "svc-secret", auth.HeaderTenantID: tenantID}); res.StatusCode != 200 || !strings.Contains(string(body), it.ID) {
		t.Fatalf("internal lookup: %d %s", res.StatusCode, body)
	}
}
