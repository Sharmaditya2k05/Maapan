// Package jobauth lets Cloud Scheduler call soochna's /internal/v1/jobs/*
// endpoints with its OIDC token alone, so the shared service secret no longer
// has to be stored in Scheduler job headers.
//
// A request is accepted when it carries the service secret (other services,
// local runs) or a Google-signed ID token whose audience is this service and
// whose e-mail is one of the allowed service accounts (the scheduler SA).
// The token is verified here, not only by Cloud Run's IAM check: dwaar and
// every other invoker can also reach the service.
package jobauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// GoogleCertsURL serves Google's OAuth2 signing keys (JWKS).
const GoogleCertsURL = "https://www.googleapis.com/oauth2/v3/certs"

// OIDC verifies Google ID tokens.
type OIDC struct {
	Audience string   // e.g. https://soochna-123.asia-south1.run.app
	Emails   []string // allowed service-account e-mails
	CertsURL string   // default GoogleCertsURL
	HTTP     *http.Client
	Now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

// Enabled reports whether OIDC verification is configured.
func (o *OIDC) Enabled() bool { return o != nil && o.Audience != "" && len(o.Emails) > 0 }

// Middleware accepts the service secret or a valid scheduler OIDC token.
func Middleware(v *auth.Verifier, o *OIDC) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !v.IsInternal(r) {
				if !o.Enabled() {
					httpx.WriteError(w, r, httpx.Unauthorized("internal endpoint"))
					return
				}
				if _, err := o.Verify(r.Context(), bearer(r)); err != nil {
					httpx.WriteError(w, r, httpx.Unauthorized("job endpoint: %s", err.Error()))
					return
				}
			}
			p := &auth.Principal{Internal: true, TenantID: r.Header.Get(auth.HeaderTenantID)}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// bearer returns the ID token. Cloud Scheduler sends it in Authorization.
func bearer(r *http.Request) string {
	if t := auth.BearerToken(r); t != "" {
		return t
	}
	h := r.Header.Get("X-Serverless-Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Claims are the ID-token claims we check.
type Claims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	jwt.RegisteredClaims
}

// Verify checks signature, issuer, audience, expiry and e-mail.
func (o *OIDC) Verify(ctx context.Context, token string) (*Claims, error) {
	if token == "" {
		return nil, errors.New("missing bearer token")
	}
	var c Claims
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(o.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30 * time.Second),
	}
	if o.Now != nil {
		opts = append(opts, jwt.WithTimeFunc(o.Now))
	}
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return o.key(ctx, kid)
	}, opts...)
	if err != nil {
		return nil, err
	}
	if c.Issuer != "https://accounts.google.com" && c.Issuer != "accounts.google.com" {
		return nil, fmt.Errorf("unexpected issuer %q", c.Issuer)
	}
	if !c.EmailVerified || !slices.Contains(o.Emails, c.Email) {
		return nil, fmt.Errorf("caller %q is not allowed", c.Email)
	}
	return &c, nil
}

func (o *OIDC) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if o.Now != nil {
		now = o.Now()
	}
	if k, ok := o.keys[kid]; ok && now.Before(o.expires) {
		return k, nil
	}
	// Unknown kid or stale cache: refetch (Google rotates keys roughly daily).
	keys, ttl, err := o.fetch(ctx)
	if err != nil {
		return nil, err
	}
	o.keys, o.expires = keys, now.Add(ttl)
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (o *OIDC) fetch(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
	u := o.CertsURL
	if u == "" {
		u = GoogleCertsURL
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch signing keys: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("fetch signing keys: status %d", res.StatusCode)
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&jwks); err != nil {
		return nil, 0, fmt.Errorf("decode signing keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, maxAge(res.Header.Get("Cache-Control")), nil
}

// maxAge reads max-age from Cache-Control (default 1 h, capped at 24 h).
func maxAge(cc string) time.Duration {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return min(time.Duration(n)*time.Second, 24*time.Hour)
			}
		}
	}
	return time.Hour
}
