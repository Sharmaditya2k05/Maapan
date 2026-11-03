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

	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/svc"
	"github.com/maapan/pehchaan/internal/identity"
	"github.com/maapan/pehchaan/internal/otp"
	"github.com/maapan/pehchaan/internal/tokens"
	"github.com/maapan/pehchaan/migrations"
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
	// JWT rotation: new tokens are signed with JWT_SECRET (kid header); tokens
	// signed with JWT_SECRET_PREVIOUS are still accepted until it is removed.
	keys := tokens.NewKeyring([]byte(app.Cfg.JWTSecret), []byte(config.String("JWT_SECRET_PREVIOUS", "")))
	// OTP hashes use an HKDF-derived key, from OTP_SECRET when set (recommended:
	// then a JWT rotation does not invalidate OTPs in flight), else JWT_SECRET.
	otpRoot := config.String("OTP_SECRET", "")
	if otpRoot == "" {
		otpRoot = app.Cfg.JWTSecret
	}
	tenantHeader := config.String("INTERNAL_TENANT_HEADER", "required")
	if tenantHeader != "required" && tenantHeader != "optional" {
		return fmt.Errorf("INTERNAL_TENANT_HEADER must be required or optional, got %q", tenantHeader)
	}
	ids := &identity.Service{
		DB:        app.DB,
		Redis:     app.Redis,
		Refresh:   &tokens.Store{KV: tokens.RedisKV{C: app.Redis}, Now: time.Now},
		Soochna:   svc.New(config.String("SOOCHNA_URL", "http://localhost:7006"), app.Cfg.ServiceSecret, app.Cfg.UseIDTokens),
		Relay:     app.Relay,
		Log:       app.Log,
		JWTSecret: []byte(app.Cfg.JWTSecret),
		Keys:      keys,
		OTPKey:    otp.DeriveKey([]byte(otpRoot)),

		InternalTenantOptional: tenantHeader == "optional",
		Env:                    app.Cfg.Env,
		DevFixedOTP:            config.String("OTP_DEV_FIXED", ""),
		Now:                    time.Now,
	}
	mux := http.NewServeMux()
	ids.Routes(mux, app.Verifier)
	return app.Run(ctx, mux)
}
