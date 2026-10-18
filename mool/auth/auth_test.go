package auth

import (
	"testing"
	"time"
)

func TestIssueParse(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, err := Issue(secret, Claims{TenantID: "t1", StoreID: "s1", Role: RoleCashier, Stores: []string{"s1"},
		RegisteredClaims: RegisteredClaimsFor("u1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(secret, tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID() != "u1" || !c.Can(PermInvoiceCreate) || c.Can(PermInvoiceCancel) {
		t.Fatalf("claims/permissions wrong: %+v", c)
	}
	if !c.CanAccessStore("s1") || c.CanAccessStore("s2") {
		t.Fatal("store scoping wrong")
	}
	if _, err := Parse([]byte("other-secret-other-secret-other-sec"), tok); err == nil {
		t.Fatal("wrong secret must fail")
	}
}
