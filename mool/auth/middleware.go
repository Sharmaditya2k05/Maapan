package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/maapan/mool/httpx"
)

// Header names used between dwaar and the services, and between services.
const (
	HeaderServiceSecret = "X-Service-Secret"
	HeaderTenantID      = "X-Tenant-ID"
	HeaderStoreID       = "X-Store-ID"
	HeaderUserID        = "X-User-ID"
	HeaderRole          = "X-Role"
)

type ctxKey struct{}

// Principal is who is calling: a user (Claims set) or an internal service.
type Principal struct {
	Claims   *Claims
	Internal bool
	TenantID string
	StoreID  string
}

// UserID returns the user ID, or "system" for internal calls.
func (p *Principal) UserID() string {
	if p.Claims != nil {
		return p.Claims.Subject
	}
	return "system"
}

// Can reports whether the principal holds perm. Internal callers can do everything.
func (p *Principal) Can(perm string) bool {
	return p.Internal || (p.Claims != nil && p.Claims.Can(perm))
}

// FromContext returns the principal set by the middleware.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// WithPrincipal stores p in ctx (useful in tests and event handlers).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Verifier authenticates requests.
type Verifier struct {
	JWTSecret     []byte
	ServiceSecret []byte
}

// NewVerifier builds a Verifier.
func NewVerifier(jwtSecret, serviceSecret string) *Verifier {
	return &Verifier{JWTSecret: []byte(jwtSecret), ServiceSecret: []byte(serviceSecret)}
}

// BearerToken extracts the bearer token from Authorization.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// IsInternal reports whether r carries the correct service secret.
func (v *Verifier) IsInternal(r *http.Request) bool {
	got := r.Header.Get(HeaderServiceSecret)
	return got != "" && len(v.ServiceSecret) > 0 &&
		subtle.ConstantTimeCompare([]byte(got), v.ServiceSecret) == 1
}

// User requires a valid user JWT. The active store is the X-Store-ID header
// when the user may access it, otherwise the sid claim.
func (v *Verifier) User(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := BearerToken(r)
		if tok == "" {
			httpx.WriteError(w, r, httpx.Unauthorized("missing bearer token"))
			return
		}
		c, err := Parse(v.JWTSecret, tok)
		if err != nil {
			httpx.WriteError(w, r, httpx.Unauthorized("invalid or expired token"))
			return
		}
		store := c.StoreID
		if s := r.Header.Get(HeaderStoreID); s != "" {
			if !c.CanAccessStore(s) {
				httpx.WriteError(w, r, httpx.Forbidden("no access to store %s", s))
				return
			}
			store = s
		}
		p := &Principal{Claims: c, TenantID: c.TenantID, StoreID: store}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// Internal requires the service secret. Tenant/store come from headers.
func (v *Verifier) Internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.IsInternal(r) {
			httpx.WriteError(w, r, httpx.Unauthorized("internal endpoint"))
			return
		}
		p := &Principal{Internal: true, TenantID: r.Header.Get(HeaderTenantID), StoreID: r.Header.Get(HeaderStoreID)}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// Require wraps h so it runs only when the principal holds every perm.
func Require(h http.Handler, perms ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if p == nil {
			httpx.WriteError(w, r, httpx.Unauthorized("not authenticated"))
			return
		}
		for _, perm := range perms {
			if !p.Can(perm) {
				httpx.WriteError(w, r, httpx.Forbidden("missing permission %s", perm))
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
