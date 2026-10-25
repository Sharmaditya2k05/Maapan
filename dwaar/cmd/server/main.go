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
		limiter = &ratelimit.Redis{Client: app.Redis}
	} else {
		app.Log.Warn("REDIS_URL not set: using in-memory rate limits (per instance)")
	}

	opt := gateway.Options{
		JWTSecret: []byte(app.Cfg.JWTSecret),
		Upstreams: upstreams,
		Limiter:   limiter,
		Limits: gateway.Limits{
			PublicPerMinute: config.Int("RATE_LIMIT_PUBLIC_PER_MIN", 20),
			UserPerMinute:   config.Int("RATE_LIMIT_USER_PER_MIN", 600),
		},
		MaxBodyBytes:    int64(config.Int("MAX_BODY_MB", 25)) << 20,
		UpstreamTimeout: config.Duration("UPSTREAM_TIMEOUT", 0),
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
	mux.Handle("GET /wake", httpx.H(gw.Wake))
	mux.Handle("/api/", gw)
	return app.Run(ctx, mux)
}
