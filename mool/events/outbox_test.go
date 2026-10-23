package events

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackoff(t *testing.T) {
	if Backoff(1) != 2*time.Second || Backoff(3) != 8*time.Second || Backoff(10) != MaxBackoff || Backoff(40) != MaxBackoff {
		t.Fatalf("backoff: %v %v %v", Backoff(1), Backoff(3), Backoff(10))
	}
}

type flakyPub struct {
	failTimes int
	calls     int
}

func (f *flakyPub) Publish(context.Context, Envelope) error {
	f.calls++
	if f.calls <= f.failTimes {
		return errors.New("boom")
	}
	return nil
}

// TestRelayLeaseRetryDead needs Postgres: TEST_DATABASE_URL=postgres://taraju:taraju@localhost:5432/taraju_test?sslmode=disable
func TestRelayLeaseRetryDead(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, OutboxDDL); err != nil {
		t.Fatal(err)
	}
	pub := &flakyPub{failTimes: 1}
	r := NewRelay(pool, pub, slog.Default())
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	tx, _ := pool.Begin(ctx)
	e, _ := New(InvoiceCreated, "test", "t1", "", map[string]string{"k": "v"})
	if err := Enqueue(ctx, tx, e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// First flush fails → attempts=1, leased into the future, not published, not dead.
	if err := r.Flush(ctx); err == nil {
		t.Fatal("expected publish failure")
	}
	var attempts int
	var leased, published, dead *time.Time
	if err := pool.QueryRow(ctx, `SELECT attempts, leased_until, published_at, dead_at FROM outbox WHERE id=$1`, e.ID).
		Scan(&attempts, &leased, &published, &dead); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || leased == nil || !leased.After(time.Now()) || published != nil || dead != nil {
		t.Fatalf("after failure: attempts=%d leased=%v published=%v dead=%v", attempts, leased, published, dead)
	}
	// While leased, a flush does nothing.
	if err := r.Flush(ctx); err != nil || pub.calls != 1 {
		t.Fatalf("leased row must not be re-published yet: calls=%d err=%v", pub.calls, err)
	}
	// Expire the lease → next flush succeeds and marks published.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET leased_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT published_at FROM outbox WHERE id=$1`, e.ID).Scan(&published); err != nil || published == nil {
		t.Fatalf("expected published: %v %v", published, err)
	}

	// Dead-lettering after MaxAttempts, then replay.
	pub.failTimes = 1 << 30
	tx, _ = pool.Begin(ctx)
	e2, _ := New(InvoiceCreated, "test", "t1", "", nil)
	_ = Enqueue(ctx, tx, e2)
	_ = tx.Commit(ctx)
	for i := 0; i < MaxAttempts; i++ {
		_, _ = pool.Exec(ctx, `UPDATE outbox SET leased_until = NULL WHERE id=$1`, e2.ID)
		_ = r.Flush(ctx)
	}
	n, err := r.DeadCount(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expected 1 dead row, got %d (%v)", n, err)
	}
	if r.DeadCheck(ctx) == nil {
		t.Fatal("DeadCheck must fail with dead rows")
	}
	if replayed, err := r.ReplayDead(ctx, ""); err != nil || replayed != 1 {
		t.Fatalf("replay: %d %v", replayed, err)
	}
	if n, _ := r.DeadCount(ctx); n != 0 {
		t.Fatal("replayed rows must not be dead")
	}
}
