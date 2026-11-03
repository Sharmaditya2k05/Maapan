package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/maapan/mool/auth"
)

// Keyring holds the JWT signing secret and, during a rotation, the previous
// one. Tokens are signed with the current secret and carry its key id in the
// JOSE "kid" header; verification picks the secret by kid and falls back to
// trying both (tokens from signers that do not set kid).
//
// Compatibility: the claims, issuer, lifetimes and HS256 are exactly what
// mool/auth.Issue produces, and mool/auth.Parse ignores "kid", so every other
// service keeps verifying pehchaan's tokens with JWT_SECRET unchanged.
type Keyring struct {
	current, previous       []byte
	currentKID, previousKID string
}

// NewKeyring builds a keyring. previous may be empty.
func NewKeyring(current, previous []byte) *Keyring {
	k := &Keyring{current: current, currentKID: KeyID(current)}
	if len(previous) > 0 && string(previous) != string(current) {
		k.previous, k.previousKID = previous, KeyID(previous)
	}
	return k
}

// KeyID is a stable, non-reversible identifier of a secret: the first 16 hex
// characters of SHA-256 over a domain-separated input. Every service with the
// same secret computes the same id.
func KeyID(secret []byte) string {
	h := sha256.Sum256(append([]byte("maapan/jwt-kid/v1:"), secret...))
	return hex.EncodeToString(h[:8])
}

// CurrentKID is the kid stamped on new tokens.
func (k *Keyring) CurrentKID() string { return k.currentKID }

// HasPrevious reports whether a previous secret is configured.
func (k *Keyring) HasPrevious() bool { return len(k.previous) > 0 }

// secretsFor returns the secrets to try for tok, the one named by its kid first.
func (k *Keyring) secretsFor(tok string) [][]byte {
	out := [][]byte{k.current}
	if len(k.previous) == 0 {
		return out
	}
	if t, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{}); err == nil {
		if kid, _ := t.Header["kid"].(string); kid == k.previousKID {
			return [][]byte{k.previous, k.current}
		}
	}
	return append(out, k.previous)
}

// IssueAccess signs an access token exactly like mool/auth.Issue, plus kid.
func (k *Keyring) IssueAccess(c auth.Claims, now time.Time) (string, error) {
	c.Issuer = auth.Issuer
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now.Add(-30 * time.Second))
	c.ExpiresAt = jwt.NewNumericDate(now.Add(auth.AccessTTL))
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	t.Header["kid"] = k.currentKID
	return t.SignedString(k.current)
}

// ParseAccess verifies an access token with mool/auth.Parse under the current
// or the previous secret.
func (k *Keyring) ParseAccess(tok string) (*auth.Claims, error) {
	if tok == "" {
		return nil, errors.New("empty token")
	}
	var firstErr error
	for _, s := range k.secretsFor(tok) {
		c, err := auth.Parse(s, tok)
		if err == nil {
			return c, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// IssueOnboarding signs an onboarding token with the current secret and kid.
func (k *Keyring) IssueOnboarding(userID, phone string, now time.Time) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, onboardingClaims(userID, phone, now))
	t.Header["kid"] = k.currentKID
	return t.SignedString(k.current)
}

// ParseOnboarding verifies an onboarding token under the current or previous secret.
func (k *Keyring) ParseOnboarding(tok string) (*OnboardingClaims, error) {
	if tok == "" {
		return nil, errors.New("empty token")
	}
	var firstErr error
	for _, s := range k.secretsFor(tok) {
		c, err := ParseOnboarding(s, tok)
		if err == nil {
			return c, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}
