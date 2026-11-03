package production

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/bom"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/internal/testdb"
)

type env struct {
	pool  *pgxpool.Pool
	inv   *testdb.Inventory
	boms  *bom.Service
	runs  *Service
	p     *auth.Principal
	store string
	ids   map[string]string
}

func newID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func setup(t *testing.T) *env {
	pool := testdb.New(t)
	now := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	inv := testdb.NewInventory()
	log := slog.New(slog.DiscardHandler)
	boms := &bom.Service{DB: pool, Inventory: inv, Log: log, Now: clock}
	e := &env{pool: pool, inv: inv, boms: boms, ids: map[string]string{},
		runs: &Service{DB: pool, BOMs: boms, Inventory: inv, Log: log, Now: clock}}
	e.p = &auth.Principal{Internal: true, TenantID: newID(t, pool), StoreID: newID(t, pool)}
	e.store = e.p.StoreID
	for _, n := range []string{"bread", "flour", "yeast", "salt", "crumbs", "ghee"} {
		e.ids[n] = newID(t, pool)
	}
	inv.Add(clients.Item{ID: e.ids["bread"], Name: "Bread 400g", Unit: "PCS"})
	inv.Add(clients.Item{ID: e.ids["flour"], Name: "Flour", Unit: "KG", PurchasePricePaise: 3500})
	inv.Add(clients.Item{ID: e.ids["yeast"], Name: "Yeast", Unit: "KG"})
	inv.Add(clients.Item{ID: e.ids["salt"], Name: "Salt", Unit: "KG"})
	inv.Add(clients.Item{ID: e.ids["crumbs"], Name: "Bread crumbs", Unit: "KG"})
	inv.Add(clients.Item{ID: e.ids["ghee"], Name: "Ghee", Unit: "KG"}) // no stock, no price
	inv.SetStock(e.store, e.ids["flour"], 100, 400000)                 // ₹40/kg average (purchase price ₹35 is ignored)
	inv.SetStock(e.store, e.ids["yeast"], 5, 200000)                   // ₹400/kg
	inv.SetStock(e.store, e.ids["salt"], 10, 20000)                    // ₹20/kg
	return e
}

func (e *env) breadBOM(t *testing.T, mutate func(*bom.Input)) *bom.BOM {
	t.Helper()
	in := bom.Input{ItemID: e.ids["bread"], ItemName: "Bread 400g", OutputQty: 10, Unit: "PCS",
		Components: []bom.Component{
			{ItemID: e.ids["flour"], ItemName: "Flour", Qty: 4.9, Unit: "KG", ScrapPercent: 2},
			{ItemID: e.ids["yeast"], ItemName: "Yeast", Qty: 100, Unit: "GM"},
		},
		Operations: []bom.Operation{{Name: "Baking", WorkCenter: "Oven 1", Hours: 1, RatePaisePerHour: 5000}}}
	if mutate != nil {
		mutate(&in)
	}
	b, err := e.boms.Create(context.Background(), e.p.TenantID, "u1", in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func status(err error) int {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func outbox(t *testing.T, pool *pgxpool.Pool) []CompletedEvent {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT payload FROM outbox WHERE event_type='nirmaan.production.completed' ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []CompletedEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data CompletedEvent `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env.Data)
	}
	return out
}

func TestBOMLifecycleDB(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	v1 := e.breadBOM(t, nil)
	if v1.ScrapMethod != bom.ScrapDivide || v1.Operations[0].WorkCenter != "Oven 1" {
		t.Fatalf("v1 = %+v", v1)
	}
	if _, err := e.boms.Activate(ctx, e.p.TenantID, v1.ID); err != nil {
		t.Fatal(err)
	}
	// the active primary BOM cannot be archived by accident
	if _, err := e.boms.Archive(ctx, e.p.TenantID, v1.ID, false); status(err) != http.StatusConflict {
		t.Fatalf("archive active = %v", err)
	}
	// cost from bhandar's weighted average, units converted (100 GM yeast = 0.1 KG)
	cost, err := e.boms.CostByID(ctx, e.p.TenantID, v1.ID, 10, bom.CostOptions{StoreID: e.store})
	if err != nil {
		t.Fatal(err)
	}
	// flour 4.9/0.98 = 5 kg × 4000 = 20000; yeast 0.1 × 40000 = 4000; baking 5000 → 29000
	if cost.MaterialPaise != 24000 || cost.OperationsPaise != 5000 || cost.Lines[0].PriceSource != clients.SourceAvgStore || !cost.Complete {
		t.Fatalf("cost = %+v", cost)
	}
	purchase, _ := e.boms.CostByID(ctx, e.p.TenantID, v1.ID, 10, bom.CostOptions{Basis: clients.BasisPurchase})
	if purchase.Complete || purchase.Lines[0].UnitCostPaise != 3500 {
		t.Fatalf("purchase basis = %+v", purchase)
	}

	// v2 effective from 1 Dec: v1 is end-dated 30 Nov, both stay active
	v2 := e.breadBOM(t, func(in *bom.Input) { in.EffectiveFrom = "2026-12-01"; in.Components[0].Qty = 5 })
	if _, err := e.boms.Activate(ctx, e.p.TenantID, v2.ID); err != nil {
		t.Fatal(err)
	}
	v1, _ = bom.Load(ctx, e.pool, e.p.TenantID, v1.ID)
	if v1.Status != bom.StatusActive || v1.EffectiveTo == nil || *v1.EffectiveTo != "2026-11-30" {
		t.Fatalf("v1 after v2 = %+v", v1)
	}
	for date, want := range map[string]string{"2026-11-15": v1.ID, "2026-12-15": v2.ID} {
		if id, _ := bom.EffectiveBOMID(ctx, e.pool, e.p.TenantID, e.ids["bread"], date); id != want {
			t.Errorf("effective on %s = %s", date, id)
		}
	}
	// an alternate activates beside the primary
	f := false
	alt := e.breadBOM(t, func(in *bom.Input) { in.IsPrimary = &f; in.AlternateName = "Oven 2" })
	if _, err := e.boms.Activate(ctx, e.p.TenantID, alt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.boms.Archive(ctx, e.p.TenantID, alt.ID, false); err != nil {
		t.Fatalf("archiving an alternate: %v", err)
	}
	// units that cannot convert block activation
	bad := e.breadBOM(t, func(in *bom.Input) { in.Components[1].Unit = "BOX" })
	if _, err := e.boms.Activate(ctx, e.p.TenantID, bad.ID); status(err) != http.StatusUnprocessableEntity {
		t.Fatalf("bad unit activation = %v", err)
	}
	list, err := e.boms.List(ctx, e.p.TenantID, e.ids["bread"], "", "", httpx.Page{Limit: 50})
	if err != nil || len(list) != 4 {
		t.Fatalf("list = %d %v", len(list), err)
	}
}

func TestRunLifecycleDB(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	b := e.breadBOM(t, nil)
	if _, err := e.boms.Activate(ctx, e.p.TenantID, b.ID); err != nil {
		t.Fatal(err)
	}
	run, err := e.runs.Create(ctx, e.p, CreateInput{ItemID: e.ids["bread"], Qty: 20})
	if err != nil {
		t.Fatal(err)
	}
	if run.RunNo != "PR/2026-27/00001" || run.BOMID != b.ID || run.PlannedHours != 2 {
		t.Fatalf("run = %+v", run)
	}
	if run.Lines[1].PlannedQty != 200 || run.Lines[1].PlannedBaseQty != 0.2 || run.Lines[1].BaseUnit != "KG" {
		t.Fatalf("yeast line = %+v", run.Lines[1])
	}
	// start issues material (WIP 10 kg flour × 4000 + 0.2 kg yeast × 40000 = 48000)
	run, err = e.runs.Start(ctx, e.p, run.ID, StartInput{})
	if err != nil {
		t.Fatal(err)
	}
	if run.IssuedValuePaise != 48000 || run.WIPPaise != 48000 || run.Lines[0].IssuedQty != 10 {
		t.Fatalf("after start = %+v", run)
	}
	// another run sees the issued flour as reserved: 100 on hand − 10 reserved < 95 needed
	big, err := e.runs.Create(ctx, e.p, CreateInput{BOMID: b.ID, Qty: 190})
	if err != nil {
		t.Fatal(err)
	}
	if big.RunNo != "PR/2026-27/00002" || len(big.Shortages) == 0 || big.Shortages[0].Reserved != 10 {
		t.Fatalf("reservation = %+v", big.Shortages)
	}
	if _, err := e.runs.Start(ctx, e.p, big.ID, StartInput{}); status(err) != http.StatusConflict {
		t.Fatalf("short start = %v", err)
	}

	// partial: 9 good + 1 rejected → half the material, event carries the run id and stock units
	ten, no := 9.0, false
	run, err = e.runs.Complete(ctx, e.p, run.ID, CompleteInput{OutputQty: &ten, RejectedQty: 1, Final: &no, OutputBatchNo: "B1"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusInProgress || run.Receipts != 1 || *run.OutputQty != 9 || run.MaterialCostPaise != 24000 || run.WIPPaise != 24000 {
		t.Fatalf("after partial = %+v", run)
	}
	evs := outbox(t, e.pool)
	if len(evs) != 1 || evs[0].ProductionRunID != run.ID || evs[0].Final || evs[0].RunNo != "PR/2026-27/00001" {
		t.Fatalf("event 1 = %+v", evs)
	}
	for _, c := range evs[0].Consumed {
		if c.ItemID == e.ids["yeast"] && (c.Qty != 0.1 || c.Unit != "KG" || c.UnitCostPaise != 40000) {
			t.Fatalf("yeast consumed in stock units: %+v", c)
		}
	}
	if evs[0].Produced[0].Qty != 9 || evs[0].Produced[0].BatchNo != "B1" {
		t.Fatalf("produced = %+v", evs[0].Produced)
	}

	// final: unplanned salt, a by-product, and ghee bhandar cannot price → refused, then priced by hand
	fin := CompleteInput{
		Consumed:   []Actual{{ItemID: e.ids["salt"], Qty: 200, Unit: "GM"}, {ItemID: e.ids["ghee"], Qty: 0.5}},
		ByProducts: []ByProduct{{ItemID: e.ids["crumbs"], Qty: 1, UnitCostPaise: 2000}},
	}
	if _, err := e.runs.Complete(ctx, e.p, run.ID, fin); status(err) != http.StatusUnprocessableEntity {
		t.Fatalf("missing price must block: %v", err)
	}
	fin.UnitCosts = []ManualCost{{ItemID: e.ids["ghee"], UnitCostPaise: 60000}}
	run, err = e.runs.Complete(ctx, e.p, run.ID, fin)
	if err != nil {
		t.Fatal(err)
	}
	// rest of the plan: flour 5 kg (20000) + yeast 0.1 (4000) + salt 0.2 kg (400) + ghee 0.5 (30000) = 54400; ops 5000
	if run.Status != StatusCompleted || run.Receipts != 2 || *run.OutputQty != 19 || run.RejectedQty != 1 ||
		run.MaterialCostPaise != 78400 || run.ByproductValuePaise != 2000 || *run.YieldPercent != 95 {
		t.Fatalf("after final = %+v", run)
	}
	evs = outbox(t, e.pool)
	last := evs[len(evs)-1]
	if len(evs) != 2 || last.ProductionRunID != run.ID+"/2" || !last.Final || len(last.Produced) != 2 || last.TotalCostPaise != 59400 {
		t.Fatalf("event 2 = %+v", last)
	}
	// main output carries cost less the by-product: (59400 − 2000) / 10 loaves
	if last.Produced[0].ItemID != e.ids["bread"] || last.Produced[0].UnitCostPaise != 5740 {
		t.Fatalf("produced = %+v", last.Produced)
	}
	if _, err := e.runs.Cancel(ctx, e.p, run.ID); status(err) != http.StatusConflict {
		t.Fatalf("cancel completed = %v", err)
	}

	// a failed batch is recorded: material consumed, nothing produced
	failed, _ := e.runs.Create(ctx, e.p, CreateInput{BOMID: b.ID, Qty: 10})
	zero := 0.0
	failed, err = e.runs.Complete(ctx, e.p, failed.ID, CompleteInput{OutputQty: &zero, FailureReason: "oven tripped"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || *failed.FailureReason != "oven tripped" || failed.MaterialCostPaise != 24000 {
		t.Fatalf("failed = %+v", failed)
	}
	evs = outbox(t, e.pool)
	if last := evs[len(evs)-1]; !last.Failed || len(last.Produced) != 0 || len(last.Consumed) != 2 {
		t.Fatalf("failed event = %+v", last)
	}

	wip, total, err := e.runs.WIP(ctx, e.p, "")
	if err != nil || len(wip) != 0 || total != 0 {
		t.Fatalf("wip = %v %d %v", wip, total, err)
	}
	if _, err := e.runs.Cancel(ctx, e.p, big.ID); err != nil {
		t.Fatal(err)
	}
}
