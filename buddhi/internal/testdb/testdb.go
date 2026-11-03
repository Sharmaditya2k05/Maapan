// Package testdb gives tests a fresh, migrated Postgres schema on
// TEST_DATABASE_URL. Tests skip when the variable is unset.
package testdb

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/buddhi/migrations"
	"github.com/maapan/mool/db"
)

// New returns a pool whose search_path is a new schema with buddhi's
// migrations applied; the schema is dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (e.g. postgres://maapan:maapan@localhost:5432/maapan_test?sslmode=disable)")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("buddhi_test_%d", rand.Int64())
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
