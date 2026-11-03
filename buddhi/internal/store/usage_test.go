package store

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/buddhi/internal/testdb"
	"github.com/maapan/mool/httpx"
)

const tenant = "11111111-1111-1111-1111-111111111111"

func newGuard(t *testing.T, budget int64) *Guard {
	pool := testdb.New(t)
	g := &Guard{DB: pool, DefaultBudget: budget, RequireFlag: true, Now: time.Now}
	if _, err := g.SaveSettings(context.Background(), Settings{TenantID: tenant, AIEnabled: true}); err != nil {
		t.Fatal(err)
	}
	return g
}

func status(err error) int {
	if err == nil {
		return 0
	}
	return httpx.AsError(err).Status
}

func TestReserveRequiresFlag(t *testing.T) {
	g := newGuard(t, 1000)
	other := "22222222-2222-2222-2222-222222222222"
	if _, err := g.Reserve(context.Background(), other, "scan", 10); status(err) != http.StatusForbidden {
		t.Fatalf("tenant without ai_enabled: %v", err)
	}
	if err := g.CheckFlag(context.Background(), other); status(err) != http.StatusForbidden {
		t.Fatalf("CheckFlag: %v", err)
	}
	if err := g.CheckFlag(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
}

// Concurrent calls must not all pass a check that only looks at past usage:
// with 100k budget and 40k per call only two can be in flight.
func TestReserveHoldsBudgetAcrossConcurrentCalls(t *testing.T) {
	g := newGuard(t, 100_000)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var granted []*Reservation
	rejected := 0
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := g.Reserve(ctx, tenant, "scan", 40_000)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if status(err) != http.StatusTooManyRequests {
					t.Errorf("unexpected error: %v", err)
				}
				rejected++
				return
			}
			granted = append(granted, r)
		}()
	}
	wg.Wait()
	if len(granted) != 2 || rejected != 4 {
		t.Fatalf("granted %d rejected %d, want 2/4", len(granted), rejected)
	}
	if held, _ := g.Held(ctx, tenant); held != 80_000 {
		t.Fatalf("held %d", held)
	}

	// Settling a cheap call frees the rest of its reservation.
	if err := g.Settle(ctx, granted[0], "u1", llm.Usage{Model: "m", InputTokens: 5_000, OutputTokens: 1_000}, true); err != nil {
		t.Fatal(err)
	}
	if held, _ := g.Held(ctx, tenant); held != 40_000 {
		t.Fatalf("held after settle %d", held)
	}
	if used, _ := g.MonthUsage(ctx, tenant); used != 6_000 {
		t.Fatalf("used %d", used)
	}
	// 100k - 6k used - 40k held = 54k free: one more 40k call fits, a second does not.
	if _, err := g.Reserve(ctx, tenant, "scan", 40_000); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Reserve(ctx, tenant, "scan", 40_000); status(err) != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %v", err)
	}
}

func TestReservationsExpire(t *testing.T) {
	g := newGuard(t, 50_000)
	ctx := context.Background()
	if _, err := g.Reserve(ctx, tenant, "ask", 40_000); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Reserve(ctx, tenant, "ask", 40_000); err == nil {
		t.Fatal("second reservation should not fit")
	}
	later := time.Now().Add(ReservationTTL + time.Minute)
	g.Now = func() time.Time { return later }
	if _, err := g.Reserve(ctx, tenant, "ask", 40_000); err != nil {
		t.Fatalf("a crashed call's hold must expire: %v", err)
	}
}

func TestUnlimitedBudgetHoldsNothing(t *testing.T) {
	g := newGuard(t, 0)
	r, err := g.Reserve(context.Background(), tenant, "scan", 40_000)
	if err != nil || r.ID != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if err := g.Settle(context.Background(), r, "u", llm.Usage{Model: "m", InputTokens: 1}, true); err != nil {
		t.Fatal(err)
	}
}
