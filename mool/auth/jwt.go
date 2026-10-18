// Package auth issues and verifies Taraju access tokens and enforces RBAC.
package auth

import (
	"errors"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer is the iss claim on access tokens.
const Issuer = "pehchaan"

// AccessTTL is the lifetime of an access token.
const AccessTTL = 15 * time.Minute

// Claims are the Taraju access-token claims.
type Claims struct {
	TenantID string   `json:"tid"`
	StoreID  string   `json:"sid,omitempty"`
	Role     string   `json:"role"`
	Stores   []string `json:"stores,omitempty"`
	Phone    string   `json:"phn,omitempty"`
	jwt.RegisteredClaims
}

// UserID returns the subject.
func (c *Claims) UserID() string { return c.Subject }

// Can reports whether the caller holds perm.
func (c *Claims) Can(perm string) bool { return Can(c.Role, perm) }

// CanAccessStore reports whether the caller may act on storeID.
func (c *Claims) CanAccessStore(storeID string) bool {
	if !StoreScoped(c.Role) {
		return true
	}
	return slices.Contains(c.Stores, storeID)
}

// Issue signs an access token.
func Issue(secret []byte, c Claims, now time.Time) (string, error) {
	c.Issuer = Issuer
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now.Add(-30 * time.Second))
	c.ExpiresAt = jwt.NewNumericDate(now.Add(AccessTTL))
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

// Parse verifies a token and returns its claims.
func Parse(secret []byte, token string) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		return secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, err
	}
	if c.Subject == "" || c.TenantID == "" || !ValidRole(c.Role) {
		return nil, errors.New("token is missing required claims")
	}
	return c, nil
}

// RegisteredClaimsFor sets the subject (user ID); Issue fills the rest.
func RegisteredClaimsFor(userID string) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Subject: userID}
}
