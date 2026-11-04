// dwaar (द्वार) — the API gateway / middleware: the only public backend.
// Authenticates, rate limits and routes /api/{service}/... to internal services.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maapan/dwaar/internal/gateway"
	"github.com/maapan/dwaar/internal/ratelimit"
	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/gcp"
	"github.com/maapan/mool/httpx"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "dwaar:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config.LoadDotEnv(".env")
	useRedis := config.String("REDIS_URL", "") != ""
	app, err := boot.New(ctx, boot.Options{Name: "dwaar", DefaultPort: "8000", Redis: useRedis})
	if err != nil {
		return err
	}

	upstreams, err := gateway.UpstreamsFromEnv()
	if err != nil {
		return err
	}
	var limiter ratelimit.Limiter = ratelimit.NewMemory()
	if app.Redis != nil {
		// Redis first; when it fails the limits move to process memory instead
		// of disappearing (OTP routes must never be unlimited).
		limiter = ratelimit.NewFallback(&ratelimit.Redis{Client: app.Redis}, func(err error) {
			app.Log.Error("Redis rate limiter failing: using in-memory limits (per instance)", "error", err.Error())
		})
	} else {
		app.Log.Warn("REDIS_URL not set: using in-memory rate limits (per instance)")
	}

	catalog := config.String("API_CATALOG", "")
	if catalog == "" {
		catalog = gateway.CatalogPublic
		if app.Cfg.IsProd() {
			catalog = gateway.CatalogAuth
		}
	}
	switch catalog {
	case gateway.CatalogPublic, gateway.CatalogAuth, gateway.CatalogOff:
	default:
		return fmt.Errorf("API_CATALOG must be public, auth or off, got %q", catalog)
	}
	var previous []byte
	if p := config.String("JWT_SECRET_PREVIOUS", ""); p != "" {
		previous = []byte(p)
	}

	opt := gateway.Options{
		JWTSecret:         []byte(app.Cfg.JWTSecret),
		PreviousJWTSecret: previous,
		Upstreams:         upstreams,
		Limiter:           limiter,
		Limits: gateway.Limits{
			PublicPerMinute:  config.Int("RATE_LIMIT_PUBLIC_PER_MIN", 20),
			WebhookPerMinute: config.Int("RATE_LIMIT_WEBHOOK_PER_MIN", 1200),
			UserPerMinute:    config.Int("RATE_LIMIT_USER_PER_MIN", 600),
		},
		TrustedProxies:  config.Int("TRUSTED_PROXIES", 1),
		ClientIPHeader:  config.String("CLIENT_IP_HEADER", ""),
		Catalog:         catalog,
		StatusTTL:       config.Duration("STATUS_CACHE", 10*time.Second),
		StatusTimeout:   config.Duration("STATUS_TIMEOUT", 3*time.Second),
		MaxBodyBytes:    int64(config.Int("MAX_BODY_MB", 25)) << 20,
		UpstreamTimeout: config.Duration("UPSTREAM_TIMEOUT", 0),
		EdgeRetry:       config.Duration("EDGE_RETRY", 75*time.Second),
		Log:             app.Log,
	}
	if app.Cfg.UseIDTokens {
		opt.IDToken = gcp.IDToken
	}
	gw := gateway.New(opt)
	for _, s := range gateway.Services {
		if u, ok := upstreams[s]; ok {
			app.Log.Info("route", "prefix", "/api/"+s, "upstream", u.String())
		}
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api", httpx.H(gw.Catalog))
	mux.Handle("GET /api/_status", httpx.H(gw.Status))
	mux.Handle("GET /wake", httpx.H(gw.Wake))
	mux.Handle("/api/", gw)
	return app.Run(ctx, mux)
}
