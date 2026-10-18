// kar (कर) — tax service: GSTIN verification, HSN master, GST maths,
// e-Way bill & e-Invoice JSON, GSTR-1 / GSTR-3B.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taraju/kar/internal/api"
	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/gstin"
	"github.com/taraju/kar/migrations"
	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/svc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "kar:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config.LoadDotEnv(".env")
	app, err := boot.New(ctx, boot.Options{
		Name: "kar", DefaultPort: "7003", DB: true, Migrations: migrations.FS,
		Redis: config.String("REDIS_URL", "") != "", // optional second-level GSTIN cache
	})
	if err != nil {
		return err
	}
	secret, idTok := app.Cfg.ServiceSecret, app.Cfg.UseIDTokens
	dir := &clients.Directory{
		Aadesh:   svc.New(config.String("AADESH_URL", "http://localhost:7004"), secret, idTok),
		Pehchaan: svc.New(config.String("PEHCHAAN_URL", "http://localhost:7001"), secret, idTok),
		Bahi:     svc.New(config.String("BAHI_URL", "http://localhost:7002"), secret, idTok),
	}

	var provider gstin.Provider = gstin.ChecksumOnly{}
	if config.String("GSTIN_PROVIDER", "checksum") == "gsp" {
		base := config.String("GSP_BASE_URL", "")
		if base == "" {
			return fmt.Errorf("GSTIN_PROVIDER=gsp needs GSP_BASE_URL")
		}
		provider = gstin.NewGSP(base, config.String("GSP_SEARCH_PATH", ""), config.String("GSP_CLIENT_ID", ""),
			config.String("GSP_CLIENT_SECRET", ""), config.Duration("GSP_TIMEOUT", 8*time.Second))
	}
	app.Log.Info("gstin provider", "provider", provider.Name())

	s := &api.Service{
		DB: app.DB, Dir: dir, Log: app.Log,
		GSTIN: &gstin.Service{DB: app.DB, Redis: app.Redis, Provider: provider,
			TTL: config.Duration("GSTIN_CACHE_TTL", 7*24*time.Hour), Log: app.Log},
	}
	mux := http.NewServeMux()
	s.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
