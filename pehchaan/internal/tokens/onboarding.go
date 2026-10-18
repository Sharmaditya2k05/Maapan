package tokens

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// OnboardingIssuer differs from the access-token issuer, so other services
// (which require iss=pehchaan) reject onboarding tokens outright.
const OnboardingIssuer = "pehchaan-onboarding"

// OnboardingTTL is how long a new user has to create their business.
const OnboardingTTL = 30 * time.Minute

// OnboardingClaims identify a verified phone that has no business yet.
type OnboardingClaims struct {
	Phone string `json:"phn"`
	jwt.RegisteredClaims
}

// IssueOnboarding signs an onboarding token for userID/phone.
func IssueOnboarding(secret []byte, userID, phone string, now time.Time) (string, error) {
	c := OnboardingClaims{Phone: phone, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, Issuer: OnboardingIssuer,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(OnboardingTTL)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

// ParseOnboarding verifies an onboarding token.
func ParseOnboarding(secret []byte, tok string) (*OnboardingClaims, error) {
	c := &OnboardingClaims{}
	_, err := jwt.ParseWithClaims(tok, c, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(OnboardingIssuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if c.Subject == "" {
		return nil, errors.New("onboarding token has no subject")
	}
	return c, nil
}
