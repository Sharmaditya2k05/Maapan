package tokens

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/maapan/mool/auth"
)

var (
	oldSecret = []byte("old-secret-old-secret-old-secret-old")
	newSecret = []byte("new-secret-new-secret-new-secret-new")
)

func claims() auth.Claims {
	return auth.Claims{TenantID: "t1", StoreID: "s1", Role: auth.RoleCashier, Stores: []string{"s1"},
		RegisteredClaims: auth.RegisteredClaimsFor("u1")}
}

func TestKeyringTokensStayCompatibleWithMool(t *testing.T) {
	k := NewKeyring(newSecret, oldSecret)
	tok, err := k.IssueAccess(claims(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Every other service verifies with mool/auth.Parse and JWT_SECRET only.
	c, err := auth.Parse(newSecret, tok)
	if err != nil || c.Subject != "u1" || c.TenantID != "t1" || c.Issuer != auth.Issuer {
		t.Fatalf("mool must accept keyring tokens: %+v %v", c, err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if err != nil || parsed.Header["kid"] != KeyID(newSecret) || k.CurrentKID() != KeyID(newSecret) {
		t.Fatalf("kid header: %v %v", parsed.Header, err)
	}
	// And tokens minted by mool (no kid) verify here.
	moolTok, _ := auth.Issue(newSecret, claims(), time.Now())
	if _, err := k.ParseAccess(moolTok); err != nil {
		t.Fatalf("mool token: %v", err)
	}
}

func TestKeyringRotation(t *testing.T) {
	before := NewKeyring(oldSecret, nil)
	oldTok, _ := before.IssueAccess(claims(), time.Now())
	oldOnb, _ := before.IssueOnboarding("u1", "+919876543210", time.Now())
	oldMool, _ := auth.Issue(oldSecret, claims(), time.Now())

	during := NewKeyring(newSecret, oldSecret)
	for name, tok := range map[string]string{"kid": oldTok, "no kid": oldMool} {
		if _, err := during.ParseAccess(tok); err != nil {
			t.Fatalf("%s token from the previous secret must verify during rotation: %v", name, err)
		}
	}
	if _, err := during.ParseOnboarding(oldOnb); err != nil {
		t.Fatalf("onboarding token during rotation: %v", err)
	}
	newTok, _ := during.IssueAccess(claims(), time.Now())
	if _, err := before.ParseAccess(newTok); err == nil {
		t.Fatal("old keyring must not accept the new secret")
	}

	after := NewKeyring(newSecret, nil)
	if _, err := after.ParseAccess(oldTok); err == nil {
		t.Fatal("once the previous secret is removed its tokens must fail")
	}
	if _, err := after.ParseAccess(""); err == nil {
		t.Fatal("empty token")
	}
	// Onboarding tokens are not access tokens and vice versa.
	if _, err := during.ParseAccess(oldOnb); err == nil {
		t.Fatal("onboarding token must not pass as access token")
	}
	if _, err := during.ParseOnboarding(newTok); err == nil {
		t.Fatal("access token must not pass as onboarding token")
	}
	if NewKeyring(newSecret, newSecret).HasPrevious() {
		t.Fatal("same secret twice is not a rotation")
	}
}
