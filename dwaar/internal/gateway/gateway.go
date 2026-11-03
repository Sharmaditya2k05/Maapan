package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/dwaar/internal/ratelimit"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// Limits configures rate limiting.
type Limits struct {
	PublicPerMinute  int // per client IP on OTP and token routes (one shared bucket)
	WebhookPerMinute int // per client IP per webhook route (payments, SMS/WhatsApp delivery reports)
	UserPerMinute    int // per user on authenticated routes
}

// Catalog modes for GET /api.
const (
	CatalogPublic = "public" // anyone (local, dev, demo)
	CatalogAuth   = "auth"   // needs a valid access token (default in prod)
	CatalogOff    = "off"    // 404
)

// Options configure the gateway.
type Options struct {
	JWTSecret []byte
	// PreviousJWTSecret is still accepted while JWT_SECRET is rotated. Access
	// tokens live 15 minutes, so it can go 15 minutes after every signer switched.
	PreviousJWTSecret []byte
	Upstreams         Upstreams
	Limiter           ratelimit.Limiter
	Limits            Limits
	MaxBodyBytes      int64
	UpstreamTimeout   time.Duration
	// TrustedProxies is how many proxies in front of dwaar append to
	// X-Forwarded-For (see ClientIP). 0 uses the TCP peer address.
	TrustedProxies int
	// Catalog is CatalogPublic, CatalogAuth or CatalogOff ("" = public).
	Catalog string
	// StatusTTL caches GET /api/_status results (default 10s); StatusTimeout
	// bounds each upstream probe (default 3s).
	StatusTTL     time.Duration
	StatusTimeout time.Duration
	// EdgeRetry is how long to keep retrying requests a host's edge proxy
	// rejected while a service was starting (0 = no retries).
	EdgeRetry time.Duration
	// IDToken, when set, fetches a Google ID token for the upstream audience
	// (Cloud Run IAM). It is sent as X-Serverless-Authorization so the user's
	// JWT can stay in Authorization.
	IDToken func(ctx context.Context, audience string) (string, error)
	Log     *slog.Logger
}

// Gateway is the http.Handler for /api/.
type Gateway struct {
	opt     Options
	proxies map[string]*httputil.ReverseProxy
	stats   map[string]*upstreamStats
	status  statusCache
}

type clientIPKey struct{}

// New builds the gateway with one reverse proxy per configured upstream.
func New(opt Options) *Gateway {
	if opt.MaxBodyBytes <= 0 {
		opt.MaxBodyBytes = 25 << 20
	}
	if opt.UpstreamTimeout <= 0 {
		opt.UpstreamTimeout = 60 * time.Second
	}
	if opt.StatusTTL <= 0 {
		opt.StatusTTL = 10 * time.Second
	}
	if opt.StatusTimeout <= 0 {
		opt.StatusTimeout = 3 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: opt.UpstreamTimeout,
	}
	retrying := edgeRetry{next: transport, budget: opt.EdgeRetry}
	g := &Gateway{opt: opt, proxies: map[string]*httputil.ReverseProxy{}, stats: map[string]*upstreamStats{}}
	for name, target := range opt.Upstreams {
		target := target
		g.stats[name] = &upstreamStats{}
		g.proxies[name] = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				rest := pr.In.Header.Get(hdrRest)
				pr.Out.Header.Del(hdrRest)
				pr.Out.URL.Scheme = target.Scheme
				pr.Out.URL.Host = target.Host
				pr.Out.URL.Path = strings.TrimRight(target.Path, "/") + rest
				pr.Out.URL.RawPath = ""
				pr.Out.URL.RawQuery = pr.In.URL.RawQuery
				pr.Out.Host = target.Host
				pr.SetXForwarded()
				// The services read the first X-Forwarded-For hop (mool/httpx), so
				// it must be the client dwaar resolved, not whatever the client sent.
				if ip, _ := pr.In.Context().Value(clientIPKey{}).(string); ip != "" {
					pr.Out.Header.Set("X-Forwarded-For", ip)
					pr.Out.Header.Set("X-Real-IP", ip)
				}
				// CORS is answered here; upstream CORS would duplicate headers.
				pr.Out.Header.Del("Origin")
			},
			Transport:     retrying,
			FlushInterval: -1, // stream SSE (buddhi) without buffering
			ModifyResponse: func(res *http.Response) error {
				for k := range res.Header {
					if strings.HasPrefix(k, "Access-Control-") {
						res.Header.Del(k)
					}
				}
				// dwaar already set X-Request-ID on the response (and passed the
				// same id upstream); drop the service's copy so it appears once.
				res.Header.Del(httpx.HeaderRequestID)
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				logx.From(r.Context()).Warn("upstream error", "service", name, "error", err.Error())
				if errors.Is(err, context.DeadlineExceeded) {
					httpx.WriteError(w, r, &httpx.Error{Status: http.StatusGatewayTimeout, Code: "upstream_timeout", Message: name + " did not answer in time"})
					return
				}
				httpx.WriteError(w, r, httpx.Upstream("%s is unreachable", name))
			},
		}
	}
	return g
}

// hdrRest passes the rewritten path from ServeHTTP to Rewrite. It is removed
// before the request leaves the gateway.
const hdrRest = "X-Dwaar-Rest"

// HeaderIdempotencyKey lets clients (POS retries) make a write safe to repeat.
// dwaar validates it and passes it on; the service deduplicates.
const HeaderIdempotencyKey = "Idempotency-Key"

// ServeHTTP authenticates, rate limits and proxies one request.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w.Header())
	StripSpoofed(r.Header)
	r.Header.Del(hdrRest)

	t, ok := ParsePath(r.URL.Path)
	if !ok || Blocked(t.Rest) {
		httpx.WriteError(w, r, httpx.NotFound("no such route"))
		return
	}
	if r.ContentLength > g.opt.MaxBodyBytes {
		httpx.WriteError(w, r, &httpx.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Message: "request body too large"})
		return
	}
	if err := checkIdempotencyKey(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, g.opt.MaxBodyBytes)

	ip := ClientIP(r, g.opt.TrustedProxies)
	ctx := context.WithValue(r.Context(), clientIPKey{}, ip)
	if class := PublicClass(r.Method, r.URL.Path); class != "" {
		key, limit := "ip:"+ip, g.opt.Limits.PublicPerMinute
		if class == ClassWebhook {
			key, limit = "wh:"+t.Service+strings.TrimRight(t.Rest, "/")+":"+ip, g.opt.Limits.WebhookPerMinute
		}
		if !g.allow(w, r, key, limit, class == ClassOTP) {
			return
		}
		// Authorization is passed through: onboarding carries pehchaan's own token,
		// and the upstream decides what (if anything) to accept.
	} else {
		claims, err := g.parse(auth.BearerToken(r))
		if err != nil {
			httpx.WriteError(w, r, httpx.Unauthorized("invalid or expired token"))
			return
		}
		store := claims.StoreID
		if s := strings.TrimSpace(r.Header.Get(auth.HeaderStoreID)); s != "" {
			if !claims.CanAccessStore(s) {
				httpx.WriteError(w, r, httpx.Forbidden("no access to store %s", s))
				return
			}
			store = s
		}
		if !g.allow(w, r, "user:"+claims.Subject, g.opt.Limits.UserPerMinute, false) {
			return
		}
		r.Header.Set(auth.HeaderTenantID, claims.TenantID)
		r.Header.Set(auth.HeaderUserID, claims.Subject)
		r.Header.Set(auth.HeaderRole, claims.Role)
		if store != "" {
			r.Header.Set(auth.HeaderStoreID, store)
		} else {
			r.Header.Del(auth.HeaderStoreID)
		}
		ctx = logx.With(ctx, logx.From(ctx).With("tenant_id", claims.TenantID, "user_id", claims.Subject))
	}

	proxy, ok := g.proxies[t.Service]
	if !ok {
		httpx.WriteError(w, r, httpx.Unavailable("%s is not configured on this gateway", t.Service))
		return
	}
	if g.opt.IDToken != nil {
		target := g.opt.Upstreams[t.Service]
		tok, err := g.opt.IDToken(ctx, target.Scheme+"://"+target.Host)
		if err != nil {
			logx.From(ctx).Error("id token", "service", t.Service, "error", err.Error())
			httpx.WriteError(w, r, httpx.Upstream("cannot authenticate to %s", t.Service))
			return
		}
		r.Header.Set("X-Serverless-Authorization", "Bearer "+tok)
	}
	r.Header.Set(hdrRest, t.Rest)
	g.proxyMetered(t.Service, proxy, w, r.WithContext(ctx))
}

// parse verifies an access token with the current secret, then the previous one.
func (g *Gateway) parse(tok string) (*auth.Claims, error) {
	c, err := auth.Parse(g.opt.JWTSecret, tok)
	if err != nil && len(g.opt.PreviousJWTSecret) > 0 && tok != "" {
		if prev, perr := auth.Parse(g.opt.PreviousJWTSecret, tok); perr == nil {
			return prev, nil
		}
	}
	return c, err
}

// checkIdempotencyKey keeps Idempotency-Key on writes (validated: 1-128
// visible ASCII characters) and drops it on other methods, where it means nothing.
func checkIdempotencyKey(r *http.Request) error {
	vals := r.Header.Values(HeaderIdempotencyKey)
	if len(vals) == 0 {
		return nil
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		r.Header.Del(HeaderIdempotencyKey)
		return nil
	}
	if len(vals) > 1 {
		return httpx.BadRequest("send one Idempotency-Key")
	}
	k := strings.TrimSpace(vals[0])
	if k == "" || len(k) > 128 {
		return httpx.BadRequest("Idempotency-Key must be 1-128 characters")
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return httpx.BadRequest("Idempotency-Key must be visible ASCII")
		}
	}
	r.Header.Set(HeaderIdempotencyKey, k)
	return nil
}

// allow applies one rate limit. When the limiter fails (it should not: the
// Redis limiter falls back to memory) the request passes, except on failClosed
// routes (OTP), which are refused rather than left unlimited.
func (g *Gateway) allow(w http.ResponseWriter, r *http.Request, key string, perMinute int, failClosed bool) bool {
	if perMinute <= 0 {
		return true
	}
	var (
		ok    bool
		retry time.Duration
		err   = errors.New("no rate limiter configured")
	)
	if g.opt.Limiter != nil {
		ok, retry, err = g.opt.Limiter.Allow(r.Context(), key, perMinute, time.Minute)
	}
	if err != nil {
		if failClosed {
			logx.From(r.Context()).Error("rate limiter unavailable, refusing OTP route", "error", err.Error())
			w.Header().Set("Retry-After", "5")
			httpx.WriteError(w, r, httpx.Unavailable("please retry in a few seconds"))
			return false
		}
		logx.From(r.Context()).Warn("rate limiter unavailable, allowing", "error", err.Error())
		return true
	}
	if !ok {
		secs := int(retry.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		httpx.WriteError(w, r, httpx.TooMany("too many requests, retry in %ds", secs))
		return false
	}
	return true
}

func securityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	h.Set("Cache-Control", "no-store")
}

// ServiceInfo is one row of GET /api.
type ServiceInfo struct {
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Configured bool   `json:"configured"`
}

// Wake asks every upstream's /healthz at once and answers when all have
// replied (or 85s passed). Hosts that put idle services to sleep wake them on
// the first request, so the app calls this before a demo session starts. It
// needs no token and returns only up/down per service.
func (g *Gateway) Wake(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 85*time.Second)
	defer cancel()
	client := &http.Client{}
	type result struct{ name, status string }
	ch := make(chan result, len(g.opt.Upstreams))
	for name, u := range g.opt.Upstreams {
		go func(name, base string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
			if err == nil {
				var res *http.Response
				if res, err = client.Do(req); err == nil {
					res.Body.Close()
					if res.StatusCode == http.StatusOK {
						ch <- result{name, "up"}
						return
					}
				}
			}
			ch <- result{name, "down"}
		}(name, strings.TrimRight(u.String(), "/"))
	}
	out, ready := map[string]string{}, true
	for range g.opt.Upstreams {
		res := <-ch
		out[res.name] = res.status
		ready = ready && res.status == "up"
	}
	return httpx.OK(w, map[string]any{"ready": ready, "services": out})
}

// Catalog serves GET /api: which services this gateway can reach. In prod it
// needs a valid access token (Options.Catalog).
func (g *Gateway) Catalog(w http.ResponseWriter, r *http.Request) error {
	securityHeaders(w.Header())
	switch g.opt.Catalog {
	case CatalogOff:
		return httpx.NotFound("no such route")
	case CatalogAuth:
		if _, err := g.parse(auth.BearerToken(r)); err != nil {
			return httpx.Unauthorized("invalid or expired token")
		}
	}
	out := make([]ServiceInfo, 0, len(Services))
	for _, s := range Services {
		_, ok := g.proxies[s]
		out = append(out, ServiceInfo{Name: s, Prefix: "/api/" + s, Configured: ok})
	}
	return httpx.OK(w, map[string]any{"services": out})
}
