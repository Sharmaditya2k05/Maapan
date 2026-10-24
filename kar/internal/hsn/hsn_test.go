package hsn

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/migrations"
	"github.com/taraju/mool/db"
)

const header = "code,description,rate_bp,cess_bp,cess_per_unit_paise,valid_from,valid_to,condition\n"

func TestParseCSVValidation(t *testing.T) {
	csv := header +
		"190590,Biscuits,1800,0,0,2017-07-01,2025-09-21,\n" + // line 2 ok
		"190590,Biscuits,500,0,0,2025-09-22,,\n" + // line 3 ok
		"19059,Bad code,500,0,0,2025-09-22,,\n" + // 5 digits
		"640399,Footwear,1200,0,0,2025-10-01,,sale value up to Rs 2500\n" + // 12% gone after 21 Sep 2025
		"640399,Footwear,1200,0,0,2025-01-01,2025-09-21,sale value up to Rs 2500\n" + // ok: 12% valid then
		"220210,Aerated water,4000,0,0,2024-01-01,,\n" + // 40% only from 22 Sep 2025
		"240210,Cigars,2800,0,0,2025-09-22,,\n" + // 28% kept for tobacco
		"9963,Restaurant,500,,,2025-09-22,,\n" + // ok, empty cess
		"190590,Biscuits again,500,0,0,2025-09-22,,\n" + // duplicate of line 3
		"84713010,Laptops,1800,0,0,2025-09-22,2025-01-01,\n" + // valid_to before valid_from
		"85171300,Phones,1800,-5,0,2025-09-22,,\n" + // negative cess
		"30049099,Medicines,500,0,0,22-09-2025,,\n" // bad date
	rows, errs, err := ParseCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("valid rows %d: %+v", len(rows), rows)
	}
	want := map[int]string{4: "4, 6 or 8 digits", 5: "not a GST slab", 7: "not a GST slab", 10: "duplicate of line 3",
		11: "before valid_from", 12: "cess_bp", 13: "valid_from"}
	if len(errs) != len(want) {
		t.Fatalf("errors %+v", errs)
	}
	for _, e := range errs {
		if !strings.Contains(e.Error, want[e.Line]) {
			t.Errorf("line %d: %q, want %q", e.Line, e.Error, want[e.Line])
		}
	}
	if _, _, err := ParseCSV(strings.NewReader("code,description\n1905,x\n")); err == nil {
		t.Fatal("missing header columns must fail")
	}
	if _, _, err := ParseCSV(strings.NewReader("\ufeff" + header + "190590,B,500,0,0,2025-09-22,,\n")); err != nil {
		t.Fatalf("BOM header: %v", err)
	}
}

func TestTSQuery(t *testing.T) {
	if q := TSQuery("Bisc  cakes!"); q != "bisc:* & cakes:*" {
		t.Fatalf("%q", q)
	}
	if q := TSQuery("':*|&!()"); q != "" {
		t.Fatalf("operators must be stripped: %q", q)
	}
	if q := TSQuery("मसाला चाय"); q != "मसाला:* & चाय:*" {
		t.Fatalf("unicode words survive: %q", q)
	}
}

// testPool returns a pool on a fresh schema with kar's migrations (skips without TEST_DATABASE_URL).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("kar_hsn_test_%d", rand.Int63())
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

func day(s string) time.Time { t, _ := time.Parse(time.DateOnly, s); return t }

func TestImportAndDatedLookup(t *testing.T) {
	pool := testPool(t)
	s := &Store{DB: pool}
	ctx := context.Background()

	// The 69 seed rows migrated as unverified, valid from 1 Jul 2017.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hsn_rates WHERE source='seed-unverified' AND valid_from='2017-07-01'`).Scan(&n); err != nil || n != 69 {
		t.Fatalf("seed rows %d %v", n, err)
	}
	c, ok, err := s.Get(ctx, "1905", day("2026-01-01"))
	if err != nil || !ok || c.Verified || c.Source != SourceSeed || c.RateBP != 500 || c.DefaultRateBP != 500 {
		t.Fatalf("seed 1905: %v %+v", err, c)
	}

	csv := header +
		"190590,Biscuits and cookies,1800,0,0,2017-07-01,,\n" +
		"190590,Biscuits and cookies,500,0,0,2025-09-22,,\n" +
		"640399,Footwear with leather uppers,1200,0,0,2017-07-01,2025-09-21,sale value above Rs 1000\n" +
		"640399,Footwear with leather uppers,500,0,0,2025-09-22,,sale value up to Rs 2500\n" +
		"640399,Footwear with leather uppers,1800,0,0,2025-09-22,,sale value above Rs 2500\n" +
		"24022010,Cigarettes of tobacco,2800,500,416000,2025-09-22,,\n" +
		"1234,Bad,1200,0,0,2026-01-01,,\n"
	rep, err := s.Import(ctx, strings.NewReader(csv), "cbic-test")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 6 || rep.Updated != 0 || rep.Rejected != 1 || rep.Closed != 1 || rep.Errors[0].Line != 8 {
		t.Fatalf("report %+v", rep)
	}
	// Biscuits: 18% before 22 Sep 2025, 5% after (the first row was closed on 21 Sep).
	if c, _, _ := s.Get(ctx, "190590", day("2025-09-21")); c.RateBP != 1800 || c.ValidTo == nil || *c.ValidTo != "2025-09-21" {
		t.Fatalf("before: %+v", c)
	}
	if c, _, _ := s.Get(ctx, "190590", day("2025-09-22")); c.RateBP != 500 || !c.Verified || c.Source != "cbic-test" {
		t.Fatalf("after: %+v", c)
	}
	if _, ok, _ := s.Get(ctx, "190590", day("2017-06-30")); ok {
		t.Fatal("nothing before GST")
	}
	// Footwear: two conditional rates in force today.
	c, ok, err = s.Get(ctx, "640399", day("2026-10-24"))
	if err != nil || !ok || len(c.Rates) != 2 {
		t.Fatalf("footwear: %v %+v", err, c)
	}
	if c, _, _ := s.Get(ctx, "24022010", day("2026-10-24")); c.CessBP != 500 || c.CessPerUnitPaise != 416000 || c.Kind != "goods" {
		t.Fatalf("cess: %+v", c)
	}
	// Re-importing a row updates it in place.
	rep, err = s.Import(ctx, strings.NewReader(header+"190590,Biscuits & cookies (sweet),500,0,0,2025-09-22,,\n"), "cbic-test-2")
	if err != nil || rep.Updated != 1 || rep.Inserted != 0 {
		t.Fatalf("re-import %v %+v", err, rep)
	}

	// Search: code prefix and full text, both dated.
	items, err := s.Search(ctx, "1905", day("2026-10-24"), 10)
	if err != nil || len(items) < 2 || items[0].Code != "1905" {
		t.Fatalf("prefix: %v %+v", err, items)
	}
	items, err = s.Search(ctx, "biscu", day("2026-10-24"), 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("text: %v %+v", err, items)
	}
	found := false
	for _, it := range items {
		if it.Code == "190590" && it.RateBP == 500 && it.Description == "Biscuits & cookies (sweet)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("190590 not found by text: %+v", items)
	}
	items, _ = s.Search(ctx, "biscu", day("2020-01-01"), 10)
	for _, it := range items {
		if it.Code == "190590" && it.RateBP != 1800 {
			t.Fatalf("dated search must use the 2020 rate: %+v", it)
		}
	}
	// Full-text search runs on the GIN index (seq scan disabled so the planner must use it if it can).
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `EXPLAIN SELECT code FROM hsn_rates WHERE search @@ to_tsquery('simple', $1)`, TSQuery("biscu"))
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if !strings.Contains(plan.String(), "hsn_rates_search") {
		t.Fatalf("full-text search does not use the GIN index:\n%s", plan.String())
	}
	// The compatibility view still answers.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hsn_codes`).Scan(&n); err != nil || n < 69 {
		t.Fatalf("hsn_codes view %d %v", n, err)
	}
}
