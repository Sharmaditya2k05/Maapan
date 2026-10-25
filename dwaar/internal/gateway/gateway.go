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
	PublicPerMinute int // per client IP on unauthenticated routes (OTP, refresh, webhooks)
	UserPerMinute   int // per user on authenticated routes
}

// Options configure the gateway.
type Options struct {
	JWTSecret       []byte
	Upstreams       Upstreams
	Limiter         ratelimit.Limiter
	Limits          Limits
	MaxBodyBytes    int64
	UpstreamTimeout time.Duration
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
}

// New builds the gateway with one reverse proxy per configured upstream.
func New(opt Options) *Gateway {
	if opt.MaxBodyBytes <= 0 {
		opt.MaxBodyBytes = 25 << 20
	}
	if opt.UpstreamTimeout <= 0 {
		opt.UpstreamTimeout = 60 * time.Second
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
	g := &Gateway{opt: opt, proxies: map[string]*httputil.ReverseProxy{}}
	for name, target := range opt.Upstreams {
		target := target
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
				// CORS is answered here; upstream CORS would duplicate headers.
				pr.Out.Header.Del("Origin")
			},
			Transport:     transport,
			FlushInterval: -1, // stream SSE (buddhi) without buffering
			ModifyResponse: func(res *http.Response) error {
				for k := range res.Header {
					if strings.HasPrefix(k, "Access-Control-") {
						res.Header.Del(k)
					}
				}
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
	r.Body = http.MaxBytesReader(w, r.Body, g.opt.MaxBodyBytes)

	ctx := r.Context()
	if IsPublic(r.Method, r.URL.Path) {
		if !g.allow(w, r, "ip:"+httpx.ClientIP(r), g.opt.Limits.PublicPerMinute) {
			return
		}
		// Authorization is passed through: onboarding carries pehchaan's own token,
		// and the upstream decides what (if anything) to accept.
	} else {
		claims, err := auth.Parse(g.opt.JWTSecret, auth.BearerToken(r))
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
		if !g.allow(w, r, "user:"+claims.Subject, g.opt.Limits.UserPerMinute) {
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
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (g *Gateway) allow(w http.ResponseWriter, r *http.Request, key string, perMinute int) bool {
	if g.opt.Limiter == nil || perMinute <= 0 {
		return true
	}
	ok, retry, err := g.opt.Limiter.Allow(r.Context(), key, perMinute, time.Minute)
	if err != nil {
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

// Catalog serves GET /api: which services this gateway can reach.
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

func (g *Gateway) Catalog(w http.ResponseWriter, _ *http.Request) error {
	out := make([]ServiceInfo, 0, len(Services))
	for _, s := range Services {
		_, ok := g.proxies[s]
		out = append(out, ServiceInfo{Name: s, Prefix: "/api/" + s, Configured: ok})
	}
	return httpx.OK(w, map[string]any{"services": out})
}
