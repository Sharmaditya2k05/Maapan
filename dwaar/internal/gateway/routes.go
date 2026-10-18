// Package gateway is dwaar's core: route /api/{service}/{rest} to the right
// Cloud Run service after authentication, rate limiting and header hygiene.
package gateway

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Services that sit behind the gateway, in ARCHITECTURE.md order.
var Services = []string{
	"pehchaan", "bahi", "kar", "aadesh", "lenden", "soochna", "aayaat", "bhandar", "nirmaan", "buddhi",
}

// publicRoutes need no JWT (ARCHITECTURE.md §2). Keyed "METHOD /api/path".
var publicRoutes = map[string]bool{
	"POST /api/pehchaan/v1/auth/otp/request": true,
	"POST /api/pehchaan/v1/auth/otp/verify":  true,
	"POST /api/pehchaan/v1/auth/refresh":     true,
	"POST /api/pehchaan/v1/auth/logout":      true,
	"POST /api/lenden/v1/webhooks/razorpay":  true,
	"POST /api/lenden/v1/webhooks/cashfree":  true,
	// Onboarding carries pehchaan's own short-lived onboarding token, not an access token.
	"POST /api/pehchaan/v1/onboarding": true,
	// SMS/WhatsApp delivery reports; soochna checks ?token= itself.
	"POST /api/soochna/v1/webhooks/gupshup": true,
	"POST /api/soochna/v1/webhooks/msg91":   true,
}

// IsPublic reports whether method+path can be called without a token.
func IsPublic(method, path string) bool {
	return publicRoutes[method+" "+strings.TrimRight(path, "/")]
}

// Target is a parsed gateway path.
type Target struct {
	Service string // e.g. "aadesh"
	Rest    string // e.g. "/v1/invoices" (always starts with "/")
}

// ParsePath splits /api/{service}/{rest}. ok=false when the path is not a
// gateway path or the service is unknown.
func ParsePath(path string) (Target, bool) {
	p, found := strings.CutPrefix(path, "/api/")
	if !found {
		return Target{}, false
	}
	svc, rest, _ := strings.Cut(p, "/")
	if !isService(svc) {
		return Target{}, false
	}
	return Target{Service: svc, Rest: "/" + rest}, true
}

func isService(name string) bool {
	for _, s := range Services {
		if s == name {
			return true
		}
	}
	return false
}

// Blocked reports whether rest must never be reachable from the internet:
// service-to-service endpoints and the Pub/Sub push endpoint.
func Blocked(rest string) bool {
	clean := strings.ToLower(rest)
	// Collapse duplicate slashes so "//internal" cannot slip through.
	for strings.Contains(clean, "//") {
		clean = strings.ReplaceAll(clean, "//", "/")
	}
	return clean == "/internal" || strings.HasPrefix(clean, "/internal/") ||
		clean == "/events" || strings.HasPrefix(clean, "/events/") ||
		strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..")
}

// Upstreams maps service name to base URL.
type Upstreams map[string]*url.URL

// UpstreamsFromEnv reads <SERVICE>_URL for every service. Unset services are
// left out (requests to them get 503).
func UpstreamsFromEnv() (Upstreams, error) {
	u := Upstreams{}
	for _, s := range Services {
		raw := strings.TrimSpace(os.Getenv(strings.ToUpper(s) + "_URL"))
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, &url.Error{Op: "parse", URL: raw, Err: errBadUpstream}
		}
		u[s] = parsed
	}
	return u, nil
}

type gwErr string

func (e gwErr) Error() string { return string(e) }

const errBadUpstream = gwErr("upstream URL must be absolute, e.g. http://localhost:7004")

// spoofable are headers only dwaar (or a service) may set. Anything the
// client sends under these names is removed before routing.
var spoofable = []string{
	"X-Service-Secret", "X-Tenant-ID", "X-User-ID", "X-Role",
	"X-Serverless-Authorization", "X-Forwarded-User", "X-Internal",
}

// StripSpoofed removes identity headers a client must not control.
// X-Store-ID is kept: it is the user's store selection and is validated against the token.
func StripSpoofed(h http.Header) {
	for _, k := range spoofable {
		h.Del(k)
	}
}
