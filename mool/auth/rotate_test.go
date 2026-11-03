package auth

import (
	"testing"
	"time"
)

// A token signed with the previous secret is still accepted while the key is
// rotated; an unknown secret is not.
func TestParseAnyAcceptsPreviousSecret(t *testing.T) {
	oldKey, newKey := []byte("old-secret-old-secret-old-secret-123"), []byte("new-secret-new-secret-new-secret-456")
	c := Claims{TenantID: "t1", Role: RoleSuperAdmin, RegisteredClaims: RegisteredClaimsFor("u1")}
	tok, err := Issue(oldKey, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAny(tok, newKey); err == nil {
		t.Fatal("old token must fail with only the new secret")
	}
	got, err := ParseAny(tok, newKey, oldKey)
	if err != nil || got.TenantID != "t1" {
		t.Fatalf("previous secret not accepted: %v %+v", err, got)
	}
	if _, err := ParseAny(tok, newKey, nil); err == nil {
		t.Fatal("empty previous secret must be ignored")
	}
}
