package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/cache"
	"github.com/maapan/mool/svc"
	"github.com/maapan/pehchaan/internal/otp"
	"github.com/maapan/pehchaan/internal/tokens"
)

// newTestRedis connects to TEST_REDIS_URL (Redis or Garnet); skips when unset.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	c, err := cache.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testPhone() string {
	// A random-ish valid number so parallel runs do not share keys.
	d := strings.ReplaceAll(uuid.NewString(), "-", "")
	digits := []byte("9")
	for i := 0; len(digits) < 10; i++ {
		digits = append(digits, '0'+d[i]%10)
	}
	return "+91" + string(digits)
}

func cleanupOTP(t *testing.T, rc *redis.Client, phone string) {
	t.Cleanup(func() { rc.Del(context.Background(), otpKey(phone), attemptsKey(phone), lockKey(phone)) })
}

// The Lua script and the no-script fallback must behave the same.
func TestCheckOTPAtomic(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	key := otp.DeriveKey([]byte("test-secret-test-secret-test-secret"))
	if err := rc.Eval(ctx, "return 1", nil).Err(); err != nil {
		t.Logf("server runs no Lua (%v): the lua path exercises the fallback", err)
	}
	type checker func(phone, hash string) (otpOutcome, int, error)
	paths := map[string]checker{
		"lua": func(phone, hash string) (otpOutcome, int, error) {
			return checkOTP(ctx, rc, phone, hash, maxWrongAttempts, otpTTL, lockAfterTooMany)
		},
		"no-script": func(phone, hash string) (otpOutcome, int, error) {
			return checkOTPNoScript(ctx, rc, []string{otpKey(phone), attemptsKey(phone), lockKey(phone)}, hash,
				maxWrongAttempts, otpTTL, lockAfterTooMany)
		},
	}
	for name, check := range paths {
		t.Run(name, func(t *testing.T) {
			phone := testPhone()
			cleanupOTP(t, rc, phone)
			good, bad := otp.Hash(key, phone, "123456"), otp.Hash(key, phone, "000000")

			if o, _, err := check(phone, good); err != nil || o != otpMissing {
				t.Fatalf("no OTP requested: %v %v", o, err)
			}
			rc.Set(ctx, otpKey(phone), good, otpTTL)
			for i := 1; i < maxWrongAttempts; i++ {
				o, n, err := check(phone, bad)
				if err != nil || o != otpWrong || n != i {
					t.Fatalf("wrong guess %d: %v n=%d %v", i, o, n, err)
				}
			}
			if o, _, _ := check(phone, good); o != otpMatch {
				t.Fatalf("the right code on the last try must pass: %v", o)
			}
			if o, _, _ := check(phone, good); o != otpMissing {
				t.Fatalf("an OTP is single-use: %v", o)
			}

			// Parallel wrong guesses: at most maxWrongAttempts-1 are answered "wrong",
			// then the phone is locked and even the right code is refused.
			rc.Set(ctx, otpKey(phone), good, otpTTL)
			var mu sync.Mutex
			counts := map[otpOutcome]int{}
			var wg sync.WaitGroup
			for i := 0; i < 25; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					o, _, err := check(phone, bad)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					counts[o]++
					mu.Unlock()
				}()
			}
			wg.Wait()
			if counts[otpWrong] > maxWrongAttempts-1 || counts[otpLocked] == 0 || counts[otpMatch] != 0 {
				t.Fatalf("parallel guesses: %v", counts)
			}
			if o, _, _ := check(phone, good); o != otpLocked {
				t.Fatalf("locked phone must refuse the right code: %v", o)
			}
		})
	}
}

// End to end in demo mode (APP_ENV=local, fixed OTP): request, a wrong try,
// the fixed code logs in; the stored hash is not keyed by the JWT secret.
func TestDemoLoginWithFixedOTP(t *testing.T) {
	rc := newTestRedis(t)
	s := newTestService(t, newTestPool(t))
	s.Redis = rc
	s.DevFixedOTP = "123456"
	s.Soochna = svc.New("http://127.0.0.1:1", "x", false) // down: local env only logs the OTP
	ctx := context.Background()
	phone := testPhone()
	cleanupOTP(t, rc, phone)
	ip := "198.51.100." + phone[len(phone)-2:]
	t.Cleanup(func() {
		rc.Del(ctx, "pehchaan:rl:otp:phone:"+phone, "pehchaan:rl:otp:ip:"+ip, "pehchaan:rl:verify:ip:"+ip)
	})

	if _, err := s.RequestOTP(ctx, phone, "sms", ip); err != nil {
		t.Fatal(err)
	}
	stored, _ := rc.Get(ctx, otpKey(phone)).Result()
	if stored != otp.Hash(otp.DeriveKey(s.JWTSecret), phone, "123456") || stored == otp.Hash(s.JWTSecret, phone, "123456") {
		t.Fatal("OTP must be hashed with the HKDF-derived key")
	}
	if _, err := s.VerifyOTP(ctx, otpVerifyInput{Phone: phone, OTP: "654321"}, ip); status(err) != 401 {
		t.Fatalf("wrong OTP: %v", err)
	}
	res, err := s.VerifyOTP(ctx, otpVerifyInput{Phone: phone, OTP: "123456"}, ip)
	if err != nil || !res.NeedsOnboarding || res.OnboardingToken == "" {
		t.Fatalf("fixed OTP login: %+v %v", res, err)
	}
}

func TestInternalTenantRequiresMatchingTenant(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	owner, _ := onboard(t, s, "+919876500011", OnboardingInput{BusinessName: "Iyer Stores", OwnerName: "I", StateCode: "33"})
	other, _ := onboard(t, s, "+919876500012", OnboardingInput{BusinessName: "Rao Mart", OwnerName: "R", StateCode: "36"})
	mux := http.NewServeMux()
	s.Routes(mux, auth.NewVerifier(string(s.JWTSecret), "svc-secret"))
	get := func(id, tenant string) int {
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/tenants/"+id, nil)
		req.Header.Set(auth.HeaderServiceSecret, "svc-secret")
		if tenant != "" {
			req.Header.Set(auth.HeaderTenantID, tenant)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get(owner.TenantID, owner.TenantID); c != 200 {
		t.Fatalf("own tenant: %d", c)
	}
	if c := get(owner.TenantID, other.TenantID); c != 404 {
		t.Fatalf("another tenant's id: %d", c)
	}
	if c := get(owner.TenantID, ""); c != 403 {
		t.Fatalf("no X-Tenant-ID: %d", c)
	}
	s.InternalTenantOptional = true
	if c := get(owner.TenantID, ""); c != 200 {
		t.Fatalf("optional mode, no header: %d", c)
	}
	if c := get(owner.TenantID, other.TenantID); c != 404 {
		t.Fatalf("optional mode never allows a mismatch: %d", c)
	}
}

// Tokens signed with the previous secret keep working on pehchaan's routes,
// and pehchaan's kid tokens keep working for mool verifiers.
func TestRoutesAcceptRotatedSecret(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	owner, _ := onboard(t, s, "+919876500013", OnboardingInput{BusinessName: "Khan Kirana", OwnerName: "K", StateCode: "27"})
	oldSecret := s.JWTSecret
	newSecret := []byte("rotated-secret-rotated-secret-rotated")
	oldTok, err := auth.Issue(oldSecret, *owner.Claims, time.Now()) // minted before rotation, by mool, no kid
	if err != nil {
		t.Fatal(err)
	}
	s.Keys = tokens.NewKeyring(newSecret, oldSecret)
	mux := http.NewServeMux()
	s.Routes(mux, auth.NewVerifier(string(newSecret), "svc-secret"))
	me := func(tok string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := me(oldTok); c != 200 {
		t.Fatalf("token from JWT_SECRET_PREVIOUS: %d", c)
	}
	m, err := getMembership(context.Background(), s.DB, owner.TenantID, owner.Claims.Subject)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := getUser(context.Background(), s.DB, owner.Claims.Subject)
	sess, err := s.issueSession(context.Background(), u, m, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Parse(newSecret, sess.AccessToken); err != nil {
		t.Fatalf("mool must verify pehchaan's new tokens: %v", err)
	}
	s.Keys = tokens.NewKeyring(newSecret, nil)
	if c := me(oldTok); c != 401 {
		t.Fatalf("after the previous secret is dropped: %d", c)
	}
}

func TestInviteAndAuditTrail(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	ctx := context.Background()
	owner, mainStore := onboard(t, s, "+919876500021", OnboardingInput{BusinessName: "Bose Bakers", OwnerName: "B", StateCode: "19"})
	second, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Salt Lake", Code: "SL"}))
	if err != nil {
		t.Fatal(err)
	}

	in := InviteInput{Phone: "98765 00022", Name: "Cashier One", Role: auth.RoleCashier, StoreIDs: []string{mainStore}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	m, err := s.Invite(ctx, owner, in, "203.0.113.9")
	if err != nil || m.Role != auth.RoleCashier || len(m.StoreIDs) != 1 || !m.Active || m.Phone != "+919876500022" {
		t.Fatalf("invite: %+v %v", m, err)
	}
	if _, err := s.Invite(ctx, owner, in, ""); status(err) != 409 {
		t.Fatalf("duplicate invite: %v", err)
	}
	// The invitee is now a member: an OTP login lands in this business.
	ms, _ := activeMemberships(ctx, s.DB, m.ID)
	if len(ms) != 1 || ms[0].TenantID != owner.TenantID {
		t.Fatalf("invitee memberships: %+v", ms)
	}

	mgr := auth.RoleStoreManager
	if _, err := s.UpdateMember(ctx, owner, m.ID, MemberUpdate{Role: &mgr, StoreIDs: []string{mainStore, second.ID}}, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.UpdateMember(ctx, owner, m.ID, MemberUpdate{Active: &off}, ""); err != nil {
		t.Fatal(err)
	}
	// Inviting a removed member re-activates them with the new role.
	in.Role, in.StoreIDs = auth.RoleAccountant, nil
	if m, err = s.Invite(ctx, owner, in, ""); err != nil || !m.Active || m.Role != auth.RoleAccountant {
		t.Fatalf("re-invite: %+v %v", m, err)
	}

	items, err := s.AuditLog(ctx, owner.TenantID, AuditFilter{Target: m.ID, Action: "user."})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user.reactivate", "user.deactivate", "user.role_change", "user.invite"}
	if len(items) != len(want) {
		t.Fatalf("audit entries: %+v", items)
	}
	for i, a := range want {
		if items[i].Action != a {
			t.Fatalf("entry %d = %s, want %s", i, items[i].Action, a)
		}
	}
	var rc struct {
		Before  membershipState `json:"before"`
		After   membershipState `json:"after"`
		Changes []string        `json:"changes"`
	}
	if err := json.Unmarshal(items[2].Details, &rc); err != nil {
		t.Fatal(err)
	}
	if rc.Before.Role != auth.RoleCashier || rc.After.Role != auth.RoleStoreManager || len(rc.After.StoreIDs) != 2 ||
		strings.Join(rc.Changes, ",") != "role,store_ids" {
		t.Fatalf("role change details: %+v", rc)
	}
	if items[3].IP == nil || *items[3].IP != "203.0.113.9" || items[3].ActorID != owner.Claims.Subject {
		t.Fatalf("invite entry: %+v", items[3])
	}
	// Cursor pagination.
	page, _ := s.AuditLog(ctx, owner.TenantID, AuditFilter{Target: m.ID, Before: items[1].ID})
	if len(page) != 2 || page[0].ID != items[2].ID {
		t.Fatalf("page after cursor: %+v", page)
	}
	// Another tenant sees nothing.
	if other, _ := s.AuditLog(ctx, uuid.NewString(), AuditFilter{}); len(other) != 0 {
		t.Fatalf("audit leaked across tenants: %+v", other)
	}
}

func TestDevices(t *testing.T) {
	s := newTestService(t, newTestPool(t))
	ctx := context.Background()
	owner, mainStore := onboard(t, s, "+919876500031", OnboardingInput{BusinessName: "Patel Pharma", OwnerName: "P", StateCode: "24"})
	branch, err := s.CreateStore(ctx, owner, storeIn(t, StoreInput{Name: "Branch", Code: "BR"}))
	if err != nil {
		t.Fatal(err)
	}
	reg := func(p *auth.Principal, deviceID, store string) (*Device, error) {
		in := DeviceInput{DeviceID: deviceID, StoreID: store, Name: "Counter 1", Platform: "Android"}
		if err := in.Validate(); err != nil {
			return nil, err
		}
		return s.RegisterDevice(ctx, p, in, "")
	}
	d, err := reg(owner, "pos-A1", mainStore)
	if err != nil || d.Status != "active" || d.StoreID != mainStore || d.Platform == nil || *d.Platform != "android" {
		t.Fatalf("register: %+v %v", d, err)
	}
	if _, err := reg(owner, "pos-A1", branch.ID); status(err) != 409 {
		t.Fatalf("an active device cannot be registered twice: %v", err)
	}
	if _, err := reg(owner, "bad id!", mainStore); status(err) != 422 {
		t.Fatalf("device id validation: %v", err)
	}
	if _, err := reg(owner, "pos-X", uuid.NewString()); status(err) != 422 {
		t.Fatalf("foreign store: %v", err)
	}

	// A store manager manages devices of their own stores only; a cashier none.
	mgr := &auth.Principal{Claims: &auth.Claims{TenantID: owner.TenantID, Role: auth.RoleStoreManager, Stores: []string{branch.ID},
		RegisteredClaims: auth.RegisteredClaimsFor(uuid.NewString())}, TenantID: owner.TenantID}
	cashier := &auth.Principal{Claims: &auth.Claims{TenantID: owner.TenantID, Role: auth.RoleCashier, Stores: []string{mainStore},
		RegisteredClaims: auth.RegisteredClaimsFor(uuid.NewString())}, TenantID: owner.TenantID}
	if _, err := reg(mgr, "pos-B1", branch.ID); err != nil {
		t.Fatalf("manager, own store: %v", err)
	}
	if _, err := reg(mgr, "pos-B2", mainStore); status(err) != 403 {
		t.Fatalf("manager, other store: %v", err)
	}
	if _, err := reg(cashier, "pos-C1", mainStore); status(err) != 403 {
		t.Fatalf("cashier: %v", err)
	}
	if list, _ := s.Devices(ctx, mgr, "", ""); len(list) != 1 || list[0].DeviceID != "pos-B1" {
		t.Fatalf("manager sees own store devices only: %+v", list)
	}
	if list, _ := s.Devices(ctx, owner, mainStore, "active"); len(list) != 1 {
		t.Fatalf("filter by store: %+v", list)
	}

	// Internal lookup (aadesh) resolves device -> store for the tenant only.
	got, err := s.InternalDevice(ctx, owner.TenantID, "pos-A1")
	if err != nil || got.StoreID != mainStore {
		t.Fatalf("internal device: %+v %v", got, err)
	}
	if _, err := s.InternalDevice(ctx, uuid.NewString(), "pos-A1"); status(err) != 404 {
		t.Fatalf("other tenant: %v", err)
	}

	// Revoke: no longer resolvable, can be registered again (to another store).
	if _, err := s.RevokeDevice(ctx, mgr, d.ID, ""); status(err) != 404 {
		t.Fatalf("manager cannot revoke another store's device: %v", err)
	}
	rv, err := s.RevokeDevice(ctx, owner, d.ID, "")
	if err != nil || rv.Status != "revoked" || rv.RevokedAt == nil {
		t.Fatalf("revoke: %+v %v", rv, err)
	}
	if _, err := s.InternalDevice(ctx, owner.TenantID, "pos-A1"); status(err) != 404 {
		t.Fatalf("revoked device must not resolve: %v", err)
	}
	if moved, err := reg(owner, "pos-A1", branch.ID); err != nil || moved.StoreID != branch.ID {
		t.Fatalf("re-register after revoke: %+v %v", moved, err)
	}
	if hist, _ := s.Devices(ctx, owner, "", "revoked"); len(hist) != 1 {
		t.Fatalf("revoked history: %+v", hist)
	}
	if items, _ := s.AuditLog(ctx, owner.TenantID, AuditFilter{Action: "device."}); len(items) != 4 {
		t.Fatalf("device audit entries: %+v", items)
	}
}
