package jobwork

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/internal/testdb"
)

func newID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func status(err error) int {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func TestJobWorkFlowDB(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)
	inv := testdb.NewInventory()
	s := &Service{DB: pool, Inventory: inv, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	p := &auth.Principal{Internal: true, TenantID: newID(t, pool), StoreID: newID(t, pool)}
	yarn, cloth, die := newID(t, pool), newID(t, pool), newID(t, pool)
	inv.Add(clients.Item{ID: yarn, Name: "Cotton yarn", Unit: "KG", HSN: "5205", TaxRateBP: 500})
	inv.Add(clients.Item{ID: cloth, Name: "Grey cloth", Unit: "MTR", HSN: "5208", TaxRateBP: 500})
	inv.Add(clients.Item{ID: die, Name: "Weaving die", Unit: "PCS", HSN: "8448", TaxRateBP: 1800})
	inv.SetStock(p.StoreID, yarn, 500, 15_000_000) // ₹300/kg

	if _, err := s.CreateWorker(ctx, p.TenantID, WorkerInput{Name: "Bad", GSTIN: "24AAAAA0000A1Z0"}); status(err) != http.StatusUnprocessableEntity {
		t.Fatalf("bad gstin accepted: %v", err)
	}
	w, err := s.CreateWorker(ctx, p.TenantID, WorkerInput{Name: "Surat Weavers", StateCode: "24", Phone: "9800000000"})
	if err != nil {
		t.Fatal(err)
	}

	// Maharashtra principal sends 100 kg yarn (value from average cost) + a die (capital goods, exempt) to Gujarat
	ch, err := s.CreateChallan(ctx, p, ChallanInput{JobWorkerID: w.ID, PrincipalState: "27", NatureOfProcessing: "Weaving",
		ChallanDate: "2026-10-05", Lines: []ChallanLineInput{
			{ItemID: yarn, Qty: 100},
			{ItemID: die, Qty: 1, GoodsType: GoodsCapital, ReturnExempt: true, TaxableValuePaise: ptr(int64(5_000_000))},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ChallanNo != "JW/2026-27/00001" || !ch.InterState || ch.TotalValuePaise != 8_000_000 || ch.Lines[0].TaxableValuePaise != 3_000_000 {
		t.Fatalf("challan = %+v", ch)
	}
	if ch.Lines[0].DueDate == nil || *ch.Lines[0].DueDate != "2027-10-04" || ch.Lines[1].DueDate != nil {
		t.Fatalf("due dates = %+v %+v", ch.Lines[0].DueDate, ch.Lines[1].DueDate)
	}
	if w := strings.Join(ch.Warnings, "\n"); !strings.Contains(w, "Inter-state") || !strings.Contains(w, "2027-10-04") {
		t.Fatalf("warnings = %s", w)
	}

	// receive 380 m cloth for 95 kg yarn, 3 kg waste, ₹4,000 weaving charges; 2 kg still out
	ch, err = s.Receive(ctx, p, ch.ID, ReceiptInput{ReceiptDate: "2026-10-20", JWChallanNo: "SW/118", JWChallanDate: "2026-10-19",
		ProcessingChargesPaise: 400_000, Lines: []ReceiptLineInput{
			{ChallanLineNo: 1, Kind: KindProcessed, ItemID: cloth, Qty: 380, InputQty: 95},
			{ChallanLineNo: 1, Kind: KindWaste, Qty: 3},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Status != StatusPartial || ch.Lines[0].Pending != 2 || ch.Lines[0].Waste != 3 || len(ch.Receipts) != 1 || ch.Receipts[0].ReceiptNo != "JWR/2026-27/0001" {
		t.Fatalf("after receipt = %+v", ch)
	}
	// stock event: 98 kg yarn out at ₹300, 380 m cloth in at (98 × 30000 + 400000) / 380
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE event_type='nirmaan.production.completed'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data ReceiveEvent `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	ev := env.Data
	if !ev.JobWork || len(ev.Consumed) != 1 || ev.Consumed[0].Qty != 98 || ev.Consumed[0].UnitCostPaise != 30000 ||
		len(ev.Produced) != 1 || ev.Produced[0].Qty != 380 || ev.Produced[0].UnitCostPaise != 8789 || ev.TotalCostPaise != 3_340_000 {
		t.Fatalf("event = %+v", ev)
	}

	// over-receipt is refused
	if _, err := s.Receive(ctx, p, ch.ID, ReceiptInput{ReceiptDate: "2026-10-25", Lines: []ReceiptLineInput{{ChallanLineNo: 1, Kind: KindReturned, Qty: 5}}}); status(err) != http.StatusUnprocessableEntity {
		t.Fatalf("over-receipt = %v", err)
	}

	// pending: 2 kg yarn and the die; a year later the yarn is overdue (deemed supply), the die exempt
	pend, err := s.Pending(ctx, p, w.ID, "", false)
	if err != nil || len(pend) != 2 || pend[0].Pending != 2 || pend[0].Status != DueOK || pend[0].PendingValue != 60_000 {
		t.Fatalf("pending = %+v %v", pend, err)
	}
	pend, _ = s.Pending(ctx, p, "", "2027-10-05", false)
	if pend[0].Status != DueOverdue || pend[1].Status != DueExempt {
		t.Fatalf("pending a year on = %+v", pend)
	}
	if _, err := s.CancelChallan(ctx, p, ch.ID); status(err) != http.StatusConflict {
		t.Fatalf("cancel with receipts = %v", err)
	}

	// return the rest and the die: challan closes
	ch, err = s.Receive(ctx, p, ch.ID, ReceiptInput{ReceiptDate: "2026-10-28", Lines: []ReceiptLineInput{
		{ChallanLineNo: 1, Kind: KindReturned, Qty: 2}, {ChallanLineNo: 2, Kind: KindReturned, Qty: 1}}})
	if err != nil || ch.Status != StatusClosed {
		t.Fatalf("closed = %+v %v", ch, err)
	}

	// ITC-04 for H1 of 2026-27 is empty; H2 has the challan (IGST) and the receipts
	per, _ := ParsePeriod("2026-27-H1", "", "")
	t4, t5, err := s.ITC04(ctx, p.TenantID, per)
	if err != nil || len(t4) != 0 || len(t5) != 0 {
		t.Fatalf("H1 = %v %v %v", t4, t5, err)
	}
	per, _ = ParsePeriod("2026-27-H2", "", "")
	t4, t5, err = s.ITC04(ctx, p.TenantID, per)
	if err != nil {
		t.Fatal(err)
	}
	if len(t4) != 2 || t4[0].IGSTRateBP != 500 || t4[0].UQC != "KGS" || t4[1].GoodsType != GoodsCapital {
		t.Fatalf("table 4 = %+v", t4)
	}
	if len(t5) != 3 || t5[0].Qty != 380 || t5[0].UQC != "MTR" || t5[0].LossesQty != 3 || t5[0].JWChallanNo != "SW/118" {
		t.Fatalf("table 5A = %+v", t5)
	}
	var buf bytes.Buffer
	if err := WriteTable4CSV(&buf, t4); err != nil || !strings.Contains(buf.String(), "JW/2026-27/00001,05-10-2026,Inputs,Cotton yarn,5205,KGS,100,30000.00,5,0,0,0") {
		t.Fatalf("csv = %s %v", buf.String(), err)
	}

	// a cancelled challan leaves ITC-04
	ch2, err := s.CreateChallan(ctx, p, ChallanInput{JobWorkerID: w.ID, PrincipalState: "27", NatureOfProcessing: "Dyeing",
		ChallanDate: "2026-11-01", Lines: []ChallanLineInput{{ItemID: yarn, Qty: 10}}})
	if err != nil || ch2.ChallanNo != "JW/2026-27/00002" {
		t.Fatalf("challan 2 = %+v %v", ch2, err)
	}
	if _, err := s.CancelChallan(ctx, p, ch2.ID); err != nil {
		t.Fatal(err)
	}
	t4, _, _ = s.ITC04(ctx, p.TenantID, per)
	if len(t4) != 2 {
		t.Fatalf("cancelled challan reported: %+v", t4)
	}
}

func ptr[T any](v T) *T { return &v }
