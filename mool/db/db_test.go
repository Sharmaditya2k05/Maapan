package db

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A search_path in DATABASE_URL creates that schema on connect and puts the
// service's tables (including schema_migrations) inside it.
func TestConnectCreatesSearchPathSchema(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	const schema = "mool_db_schema_test"
	pool, err := Connect(ctx, url+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") //nolint:errcheck
	var got string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != schema {
		t.Fatalf("current_schema = %q, want %q", got, schema)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE probe (id int)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name='probe'", schema).Scan(&n); err != nil || n != 1 {
		t.Fatalf("probe table not in %s: n=%d err=%v", schema, n, err)
	}
}
