package creds

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/kar/migrations"
	"github.com/maapan/mool/db"
)

// testPool returns a pool on a fresh schema with kar's migrations (skips without TEST_DATABASE_URL).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("kar_creds_test_%d", rand.Int63())
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
	return pool
}

func TestStoreRoundTrip(t *testing.T) {
	pool := testPool(t)
	c, _ := NewCipher(DeriveKey("svc"))
	s := &Store{DB: pool, Cipher: c}
	ctx := context.Background()
	tenant, other := uuid.NewString(), uuid.NewString()
	sm, err := s.Put(ctx, tenant, "27aapfu0939f1zv", Input{EInvoiceUsername: "acme_api", EInvoicePassword: "S3cret!einv",
		EWayUsername: "acme_ewb", EWayPassword: "S3cret!ewb"}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if sm.GSTIN != "27AAPFU0939F1ZV" || sm.EInvoiceUsername != "****_api" || !sm.HasEInvoicePassword || !sm.HasEWayPassword {
		t.Fatalf("summary %+v", sm)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT einvoice_password_enc FROM gsp_credentials WHERE tenant_id=$1`, tenant).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("S3cret")) {
		t.Fatal("password stored in clear")
	}
	got, ok, err := s.Credentials(ctx, tenant, "27AAPFU0939F1ZV")
	if err != nil || !ok || got.EInvoicePassword != "S3cret!einv" || got.EWayPassword != "S3cret!ewb" || got.EWayUsername != "acme_ewb" {
		t.Fatalf("credentials %v %+v", err, got)
	}
	// Another tenant sees nothing; a blob moved to another tenant does not open.
	if _, ok, _ := s.Credentials(ctx, other, "27AAPFU0939F1ZV"); ok {
		t.Fatal("tenant isolation")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO gsp_credentials (tenant_id, gstin, einvoice_username, einvoice_password_enc)
		SELECT $2, gstin, einvoice_username, einvoice_password_enc FROM gsp_credentials WHERE tenant_id=$1`, tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Credentials(ctx, other, "27AAPFU0939F1ZV"); err == nil {
		t.Fatal("a copied blob must not decrypt under another tenant")
	}
	// Updating the username alone keeps the password.
	if _, err := s.Put(ctx, tenant, "27AAPFU0939F1ZV", Input{EInvoiceUsername: "acme_api2", EWayUsername: "acme_ewb"}, "u2"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Credentials(ctx, tenant, "27AAPFU0939F1ZV")
	if got.EInvoiceUsername != "acme_api2" || got.EInvoicePassword != "S3cret!einv" {
		t.Fatalf("partial update %+v", got)
	}
	list, err := s.List(ctx, tenant)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %+v", err, list)
	}
	if ok, err := s.Delete(ctx, tenant, "27AAPFU0939F1ZV"); err != nil || !ok {
		t.Fatalf("delete %v %v", ok, err)
	}
	if _, ok, _ := s.Summary(ctx, tenant, "27AAPFU0939F1ZV"); ok {
		t.Fatal("deleted")
	}
	if _, err := s.Put(ctx, tenant, "27AAPFU0939F1ZV", Input{}, "u"); err == nil {
		t.Fatal("empty input must fail")
	}
}
