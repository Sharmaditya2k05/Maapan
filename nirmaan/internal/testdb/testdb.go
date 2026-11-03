// Package testdb gives DB-gated tests a fresh, migrated schema in
// TEST_DATABASE_URL and an in-memory stand-in for bhandar.
package testdb

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/migrations"
)

// New connects to TEST_DATABASE_URL, creates a throw-away schema, migrates it
// and returns a pool bound to it. Skips the test when the URL is unset.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("nirmaan_test_%d", rand.Int63())
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
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
	if err := db.Migrate(ctx, pool, migrations.FS, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Inventory is a fake bhandar: items and per-store positions.
type Inventory struct {
	mu    sync.Mutex
	Items map[string]clients.Item
	Pos   map[string]map[string]clients.StockPos // store → item → position
}

// NewInventory returns an empty fake.
func NewInventory() *Inventory {
	return &Inventory{Items: map[string]clients.Item{}, Pos: map[string]map[string]clients.StockPos{}}
}

// Add registers an item (base unit, purchase price, alternates).
func (f *Inventory) Add(it clients.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Items[it.ID] = it
}

// Stock sets an item's position at a store.
func (f *Inventory) SetStock(store, item string, onHand float64, valuePaise int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Pos[store] == nil {
		f.Pos[store] = map[string]clients.StockPos{}
	}
	f.Pos[store][item] = clients.StockPos{OnHand: onHand, ValuePaise: valuePaise}
}

// Lookup implements clients.Inventory.
func (f *Inventory) Lookup(_ context.Context, ids []string) (map[string]clients.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]clients.Item{}
	for _, id := range ids {
		if it, ok := f.Items[id]; ok {
			out[id] = it
		}
	}
	return out, nil
}

// Stock implements clients.Inventory ("" sums every store).
func (f *Inventory) Stock(_ context.Context, store string) (map[string]clients.StockPos, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]clients.StockPos{}
	for s, m := range f.Pos {
		if store != "" && s != store {
			continue
		}
		for id, p := range m {
			q := out[id]
			q.OnHand += p.OnHand
			q.ValuePaise += p.ValuePaise
			out[id] = q
		}
	}
	return out, nil
}
