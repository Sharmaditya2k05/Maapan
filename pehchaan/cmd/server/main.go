// pehchaan (पहचान) — identity service: OTP login, JWT + refresh tokens, tenants, stores, users, roles.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/svc"
	"github.com/taraju/pehchaan/internal/identity"
	"github.com/taraju/pehchaan/internal/tokens"
	"github.com/taraju/pehchaan/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pehchaan:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "pehchaan", DefaultPort: "7001", DB: true, Redis: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}
	ids := &identity.Service{
		DB:          app.DB,
		Redis:       app.Redis,
		Refresh:     &tokens.Store{KV: tokens.RedisKV{C: app.Redis}, Now: time.Now},
		Soochna:     svc.New(config.String("SOOCHNA_URL", "http://localhost:7006"), app.Cfg.ServiceSecret, app.Cfg.UseIDTokens),
		Relay:       app.Relay,
		Log:         app.Log,
		JWTSecret:   []byte(app.Cfg.JWTSecret),
		Env:         app.Cfg.Env,
		DevFixedOTP: config.String("OTP_DEV_FIXED", ""),
		Now:         time.Now,
	}
	mux := http.NewServeMux()
	ids.Routes(mux, app.Verifier)
	return app.Run(ctx, mux)
}
