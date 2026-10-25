package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/pehchaan/internal/tokens"
	"github.com/maapan/pehchaan/migrations"
)

const (
	gstinMH  = "27AAPFU0939F1ZV" // Maharashtra
	gstinKA  = "29AAPFU0939F1ZR" // Karnataka, same PAN
	gstinKA2 = "29AAPFU0939F2ZQ" // Karnataka, second registration
	gstinDL  = "07AAPFU0939F1ZX" // Delhi
	gstinMH2 = "27AABCT1234Q1Z3" // another business
)

type memKV map[string]string

func (f memKV) Set(_ context.Context, k, v string, _ time.Duration) error { f[k] = v; return nil }
func (f memKV) GetDel(_ context.Context, k string) (string, bool, error) {
	v, ok := f[k]
	delete(f, k)
	return v, ok, nil
}
func (f memKV) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f[k]
	return v, ok, nil
}
func (f memKV) Exists(_ context.Context, k string) (bool, error) { _, ok := f[k]; return ok, nil }
func (f memKV) Del(_ context.Context, ks ...string) error {
	for _, k := range ks {
		delete(f, k)
	}
	return nil
}

// newTestPool returns a pool on a fresh schema of TEST_DATABASE_URL (skips when unset).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("pehchaan_test_%d", rand.Int63())
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
	return pool
}

func newTestService(t *testing.T, pool *pgxpool.Pool) *Service {
	t.Helper()
	if err := db.Migrate(context.Background(), pool, migrations.FS, slog.Default()); err != nil {
		t.Fatal(err)
	}
	return &Service{DB: pool, Refresh: &tokens.Store{KV: memKV{}, Now: time.Now}, Log: slog.Default(),
		JWTSecret: []byte("test-secret-test-secret-test-secret"), Env: "local", Now: time.Now}
}

func status(err error) int {
	if err == nil {
		return 0
	}
	return httpx.AsError(err).Status
}

// onboard creates a business for a new phone and returns the owner principal and first store id.
func onboard(t *testing.T, s *Service, phone string, in OnboardingInput) (*auth.Principal, string) {
	t.Helper()
	ctx := context.Background()
	u, err := upsertUser(ctx, s.DB, phone, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := tokens.IssueOnboarding(s.JWTSecret, u.ID, phone, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Onboard(ctx, tok, in, "")
	if err != nil {
		t.Fatal(err)
	}
	c := &auth.Claims{TenantID: sess.TenantID, StoreID: sess.StoreID, Role: sess.Role, RegisteredClaims: auth.RegisteredClaimsFor(u.ID)}
	return &auth.Principal{Claims: c, TenantID: sess.TenantID, StoreID: sess.StoreID}, sess.StoreID
}

func regIn(t *testing.T, in RegistrationInput) RegistrationInput {
	t.Helper()
	if err := in.Validate(); err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return in
}

func storeIn(t *testing.T, in StoreInput) StoreInput {
	t.Helper()
	if err := in.Validate(); err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return in
}

func TestRegistrationInputValidation(t *testing.T) {
	cases := []struct {
		name string
		in   RegistrationInput
		ok   bool
	}{
		{"valid", RegistrationInput{GSTIN: " 29aapfu0939f1zr ", PIN: "560001"}, true},
		{"unregistered", RegistrationInput{StateCode: "27"}, true},
		{"bad checksum", RegistrationInput{GSTIN: "29AAPFU0939F1ZV"}, false},
		{"state differs from GSTIN", RegistrationInput{GSTIN: gstinKA, StateCode: "27"}, false},
		{"short pin", RegistrationInput{GSTIN: gstinKA, PIN: "56001"}, false},
		{"pin with letters", RegistrationInput{GSTIN: gstinKA, PIN: "56000A"}, false},
		{"no state", RegistrationInput{}, false},
		{"unregistered composition", RegistrationInput{StateCode: "27", Composition: true}, false},
		{"composition and e-invoice", RegistrationInput{GSTIN: gstinKA, Composition: true, EInvoiceApplicable: true}, false},
		{"bad status", RegistrationInput{GSTIN: gstinKA, Status: "closed"}, false},
	}
	for _, c := range cases {
		err := c.in.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
	in := RegistrationInput{GSTIN: " 29aapfu0939f1zr "}
	_ = in.Validate()
	if in.GSTIN != gstinKA || in.StateCode != "29" {
		t.Fatalf("normalise: %+v", in)
	}
	if err := (&OnboardingInput{BusinessName: "X", OwnerName: "Y", StateCode: "27", PIN: "1234"}).Validate(); status(err) != 422 {
		t.Fatalf("onboarding pin must be validated: %v", err)
	}
}

func TestRegistrationsCRUD(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	ctx := context.Background()
	owner, mainStore := onboard(t, s, "+919876500001", OnboardingInput{BusinessName: "Sharma Traders", OwnerName: "Kartik",
		GSTIN: gstinMH, StoreName: "Andheri", Address: "12 MG Road", City: "Mumbai", PIN: "400053"})

	// Onboarding created the primary registration and linked the store to it.
	regs, err := s.Registrations(ctx, owner.TenantID, "")
	if err != nil || len(regs) != 1 {
		t.Fatalf("registrations after onboarding: %+v %v", regs, err)
	}
	prim := regs[0]
	if !prim.IsPrimary || prim.GSTIN != gstinMH || prim.StateCode != "27" || prim.City != "Mumbai" || prim.PIN != "400053" ||
		prim.LegalName != "Sharma Traders" || prim.Status != "active" || prim.StoreCount != 1 {
		t.Fatalf("primary: %+v", prim)
	}
	info, err := s.InternalStore(ctx, mainStore)
	if err != nil || info.RegistrationID != prim.ID || !info.RegistrationPrimary || info.GSTIN != gstinMH || info.PIN != "400053" ||
		info.City != "Mumbai" || info.LegalName != "Sharma Traders" || info.TenantID != owner.TenantID {
		t.Fatalf("internal store: %+v %v", info, err)
	}

	// A second GSTIN (Karnataka branch).
	ka, err := s.CreateRegistration(ctx, owner, regIn(t, RegistrationInput{GSTIN: gstinKA, TradeName: "Sharma Bengaluru",
		Address: "4 Brigade Rd", City: "Bengaluru", PIN: "560001", EInvoiceApplicable: true}))
	if err != nil || ka.IsPrimary || ka.LegalName != "Sharma Traders" || ka.StateCode != "29" || !ka.EInvoiceApplicable {
		t.Fatalf("create KA: %+v %v", ka, err)
	}
	if _, err := s.CreateRegistration(ctx, owner, regIn(t, RegistrationInput{GSTIN: gstinKA})); status(err) != 409 {
		t.Fatalf("duplicate GSTIN must be 409: %v", err)
	}

	// Stores: default is the primary; a Karnataka store must name (or carry) the Karnataka GSTIN.
	if _, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Jayanagar", Code: "BLR1", StateCode: "29"})); status(err) != 422 {
		t.Fatalf("KA store under the MH primary must be 422: %v", err)
	}
	blr, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Jayanagar", Code: "BLR1", RegistrationID: ka.ID}))
	if err != nil || blr.RegistrationID != ka.ID || blr.StateCode != "29" {
		t.Fatalf("KA store: %+v %v", blr, err)
	}
	blr2, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Whitefield", Code: "BLR2", GSTIN: gstinKA}))
	if err != nil || blr2.RegistrationID != ka.ID {
		t.Fatalf("store found by its GSTIN: %+v %v", blr2, err)
	}
	if _, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Delhi", Code: "DEL", GSTIN: gstinDL})); status(err) != 422 {
		t.Fatalf("unknown GSTIN must be 422: %v", err)
	}
	if _, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Mixed", Code: "MIX", GSTIN: gstinMH, RegistrationID: ka.ID})); status(err) != 422 {
		t.Fatalf("GSTIN differing from the registration must be 422: %v", err)
	}
	thane, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Thane", Code: "THN"}))
	if err != nil || thane.RegistrationID != prim.ID || thane.StateCode != "27" {
		t.Fatalf("default store: %+v %v", thane, err)
	}

	// Internal lookup: the registration's GSTIN/address fill a store that has none.
	info, err = s.InternalStore(ctx, blr.ID)
	if err != nil || info.GSTIN != gstinKA || info.Address != "4 Brigade Rd" || info.StateCode != "29" || info.RegistrationPrimary ||
		info.TradeName != "Sharma Bengaluru" || info.PIN != "560001" || !info.EInvoiceApplicable || info.Composition {
		t.Fatalf("internal KA store: %+v %v", info, err)
	}

	// Another tenant's registration cannot be used.
	other, _ := onboard(t, s, "+919876500002", OnboardingInput{BusinessName: "Other Co", OwnerName: "O", GSTIN: gstinMH2})
	otherRegs, _ := s.Registrations(ctx, other.TenantID, "")
	if _, err := s.UpdateStore(ctx, owner, thane.ID, storeIn(t, StoreInput{Name: "Thane", Code: "THN", RegistrationID: otherRegs[0].ID})); status(err) != 422 {
		t.Fatalf("foreign registration must be 422: %v", err)
	}
	if _, err := s.Registration(ctx, owner.TenantID, otherRegs[0].ID); status(err) != 404 {
		t.Fatalf("foreign registration get must be 404: %v", err)
	}

	// Deactivation is refused while stores bill under it; the primary can never be deactivated or demoted.
	in := registrationInputOf(*ka)
	in.Status = "inactive"
	if _, err := s.UpdateRegistration(ctx, owner, ka.ID, in); status(err) != 409 {
		t.Fatalf("deactivate in use must be 409: %v", err)
	}
	if _, err := s.DeactivateRegistration(ctx, owner, prim.ID); status(err) != 422 {
		t.Fatalf("deactivate primary must be 422: %v", err)
	}
	off := false
	pin := registrationInputOf(prim)
	pin.IsPrimary = &off
	if _, err := s.UpdateRegistration(ctx, owner, prim.ID, pin); status(err) != 422 {
		t.Fatalf("demote primary must be 422: %v", err)
	}

	// Switching the primary demotes the old one and keeps tenants in sync.
	on := true
	kin := registrationInputOf(*ka)
	kin.IsPrimary = &on
	if ka, err = s.UpdateRegistration(ctx, owner, ka.ID, kin); err != nil || !ka.IsPrimary {
		t.Fatalf("make KA primary: %+v %v", ka, err)
	}
	var primaries int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM registrations WHERE tenant_id=$1 AND is_primary`, owner.TenantID).Scan(&primaries)
	tn, _ := getTenant(ctx, s.DB, owner.TenantID)
	if primaries != 1 || deref(tn.GSTIN) != gstinKA || tn.StateCode != "29" || deref(tn.PAN) != "AAPFU0939F" {
		t.Fatalf("one primary and tenant synced: n=%d tenant=%+v", primaries, tn)
	}
	if info, _ = s.InternalStore(ctx, mainStore); info.RegistrationPrimary || info.GSTIN != gstinMH {
		t.Fatalf("main store after switch: %+v", info)
	}

	// Move the Karnataka stores to a second Karnataka GSTIN; KA2 is then in use.
	ka2, err := s.CreateRegistration(ctx, owner, regIn(t, RegistrationInput{GSTIN: gstinKA2}))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []*Store{blr, blr2} {
		if _, err := s.UpdateStore(ctx, owner, st.ID, storeIn(t, StoreInput{Name: st.Name, Code: st.Code, RegistrationID: ka2.ID})); err != nil {
			t.Fatalf("move %s: %v", st.Code, err)
		}
	}
	if got, _ := s.Registration(ctx, owner.TenantID, ka.ID); got.StoreCount != 0 {
		t.Fatalf("KA should have no stores: %+v", got)
	}
	if r, err := s.DeactivateRegistration(ctx, owner, ka2.ID); status(err) != 409 {
		t.Fatalf("KA2 has stores: %+v %v", r, err)
	}
	// Make MH primary again and retire KA (now unused).
	mhIn := registrationInputOf(prim)
	mhIn.IsPrimary = &on
	if _, err := s.UpdateRegistration(ctx, owner, prim.ID, mhIn); err != nil {
		t.Fatal(err)
	}
	if r, err := s.DeactivateRegistration(ctx, owner, ka.ID); err != nil || r.Status != "inactive" {
		t.Fatalf("retire KA: %+v %v", r, err)
	}
	if _, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Late", Code: "LATE", RegistrationID: ka.ID})); status(err) != 422 {
		t.Fatalf("store under inactive registration must be 422: %v", err)
	}

	// PUT /v1/tenant carries a GSTIN change to the primary registration and its stores.
	tin := TenantInput{BusinessName: "Sharma Traders", GSTIN: "27AAPFU0939F2ZU", Address: "New address"}
	if err := tin.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTenant(ctx, owner, tin); err != nil {
		t.Fatal(err)
	}
	p2, _ := s.Registration(ctx, owner.TenantID, prim.ID)
	ms, _ := getStore(ctx, s.DB, owner.TenantID, mainStore)
	if p2.GSTIN != tin.GSTIN || p2.Address != "New address" || deref(ms.GSTIN) != tin.GSTIN {
		t.Fatalf("tenant change must reach the primary and its stores: %+v %+v", p2, ms)
	}

	// Internal list by tenant, filtered by GSTIN.
	all, _ := s.Registrations(ctx, owner.TenantID, "")
	byG, _ := s.Registrations(ctx, owner.TenantID, gstinKA2)
	if len(all) != 3 || !all[0].IsPrimary || len(byG) != 1 || byG[0].ID != ka2.ID {
		t.Fatalf("list: %d %+v", len(all), byG)
	}
}

// TestRegistrationBackfill applies 0001 alone, loads old-shaped data, then
// runs the rest and checks every store landed on the right registration.
func TestRegistrationBackfill(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	first, err := fs.ReadFile(migrations.FS, "0001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, fstest.MapFS{"0001_init.up.sql": {Data: first}}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var tA, tB, tC string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO tenants (business_name, gstin, state_code, address, phone) VALUES ('A Traders',$1,'27','1 Main St','+911') RETURNING id`, gstinMH).Scan(&tA))
	must(pool.QueryRow(ctx, `INSERT INTO tenants (business_name, state_code) VALUES ('B Kirana','09') RETURNING id`).Scan(&tB))
	must(pool.QueryRow(ctx, `INSERT INTO tenants (business_name, state_code) VALUES ('C Unreg','27') RETURNING id`).Scan(&tC))
	store := func(tenant, code, state string, gstin *string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO stores (tenant_id, name, code, state_code, gstin) VALUES ($1,$2,$2,$3,$4) RETURNING id`,
			tenant, code, state, gstin).Scan(&id))
		return id
	}
	mh, ka := gstinMH, gstinKA
	aMain := store(tA, "MAIN", "27", &mh)
	aBare := store(tA, "THN", "27", nil)
	aKA1 := store(tA, "BLR1", "29", &ka)
	aKA2 := store(tA, "BLR2", "29", &ka)
	bMain := store(tB, "MAIN", "09", nil)
	cKA := store(tC, "BLR", "29", &ka) // unregistered tenant with a registered branch

	s := newTestService(t, pool) // applies 0002+

	type link struct {
		gstin   string
		primary bool
	}
	linkOf := func(storeID string) link {
		var l link
		must(pool.QueryRow(ctx, `SELECT COALESCE(r.gstin,''), r.is_primary FROM stores s JOIN registrations r ON r.id=s.registration_id WHERE s.id=$1`, storeID).Scan(&l.gstin, &l.primary))
		return l
	}
	for id, want := range map[string]link{
		aMain: {gstinMH, true}, aBare: {gstinMH, true}, aKA1: {gstinKA, false}, aKA2: {gstinKA, false},
		bMain: {"", true}, cKA: {gstinKA, false},
	} {
		if got := linkOf(id); got != want {
			t.Errorf("store %s: got %+v want %+v", id, got, want)
		}
	}
	count := func(tenant string) (n, primaries int) {
		must(pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE is_primary) FROM registrations WHERE tenant_id=$1`, tenant).Scan(&n, &primaries))
		return
	}
	if n, p := count(tA); n != 2 || p != 1 {
		t.Errorf("tenant A: %d registrations, %d primary", n, p)
	}
	if n, p := count(tB); n != 1 || p != 1 {
		t.Errorf("tenant B: %d registrations, %d primary", n, p)
	}
	if n, p := count(tC); n != 2 || p != 1 {
		t.Errorf("tenant C: %d registrations, %d primary", n, p)
	}
	var nullable string
	must(pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stores' AND column_name='registration_id'`).Scan(&nullable))
	if nullable != "NO" {
		t.Fatalf("stores.registration_id must be NOT NULL, is_nullable=%s", nullable)
	}

	// The internal lookup falls back to the registration for a store without its own GSTIN/address.
	info, err := s.InternalStore(ctx, aBare)
	if err != nil || info.GSTIN != gstinMH || info.Address != "1 Main St" || !info.RegistrationPrimary || info.LegalName != "A Traders" {
		t.Fatalf("fallback: %+v %v", info, err)
	}
	if info, _ := s.InternalStore(ctx, bMain); info.GSTIN != "" || info.StateCode != "09" || !info.RegistrationPrimary {
		t.Fatalf("unregistered tenant: %+v", info)
	}
}

// TestInternalStoreContract checks the JSON the other services read.
func TestInternalStoreContract(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	owner, storeID := onboard(t, s, "+919876500003", OnboardingInput{BusinessName: "Gupta & Sons", OwnerName: "G",
		GSTIN: gstinMH, City: "Pune", PIN: "411001", Address: "FC Road"})
	mux := http.NewServeMux()
	s.Routes(mux, auth.NewVerifier(string(s.JWTSecret), "svc-secret"))
	get := func(path, tenant string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(auth.HeaderServiceSecret, "svc-secret")
		if tenant != "" {
			req.Header.Set(auth.HeaderTenantID, tenant)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, st := get("/internal/v1/stores/"+storeID, owner.TenantID)
	if code != 200 {
		t.Fatalf("store lookup: %d %v", code, st)
	}
	for _, k := range []string{"id", "tenant_id", "name", "code", "state_code", "gstin", "address", "phone", "registration_id",
		"legal_name", "trade_name", "city", "pin", "composition", "einvoice_applicable", "registration_primary"} {
		if _, ok := st[k]; !ok {
			t.Errorf("internal store is missing %q", k)
		}
	}
	if st["gstin"] != gstinMH || st["pin"] != "411001" || st["city"] != "Pune" || st["registration_primary"] != true {
		t.Fatalf("store lookup values: %v", st)
	}
	if code, _ := get("/internal/v1/stores/"+storeID, uuid.NewString()); code != 404 {
		t.Fatalf("other tenant must get 404, got %d", code)
	}
	code, list := get("/internal/v1/registrations?tenant_id="+owner.TenantID, "")
	items, _ := list["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("internal registrations: %d %v", code, list)
	}
	reg := items[0].(map[string]any)
	if code, one := get("/internal/v1/registrations/"+reg["id"].(string), owner.TenantID); code != 200 || one["gstin"] != gstinMH {
		t.Fatalf("internal registration: %d %v", code, one)
	}
	if code, _ := get("/internal/v1/registrations", ""); code != 422 {
		t.Fatalf("list without tenant must be 422, got %d", code)
	}
	if code, _ := get("/internal/v1/registrations/"+reg["id"].(string), uuid.NewString()); code != 404 {
		t.Fatalf("registration of another tenant must be 404, got %d", code)
	}
}
