// buddhi (बुद्धि) — AI initiative: bill scanning, ask-your-books, re-order
// forecasting, anomaly checks and live KPI dashboards.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taraju/buddhi/internal/api"
	"github.com/taraju/buddhi/internal/clients"
	"github.com/taraju/buddhi/internal/jobs"
	"github.com/taraju/buddhi/internal/kpi"
	"github.com/taraju/buddhi/internal/llm"
	"github.com/taraju/buddhi/internal/store"
	"github.com/taraju/buddhi/migrations"
	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/svc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "buddhi:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// Redis is optional: without REDIS_URL the dashboard reads Postgres directly.
	app, err := boot.New(ctx, boot.Options{
		Name: "buddhi", DefaultPort: "7010", DB: true, Migrations: migrations.FS,
		Redis: config.String("REDIS_URL", "") != "",
	})
	if err != nil {
		return err
	}
	secret, idTok := app.Cfg.ServiceSecret, app.Cfg.UseIDTokens
	cl := &clients.Clients{
		Bahi:     svc.New(config.String("BAHI_URL", "http://localhost:7002"), secret, idTok),
		Bhandar:  svc.New(config.String("BHANDAR_URL", "http://localhost:7008"), secret, idTok),
		Aadesh:   svc.New(config.String("AADESH_URL", "http://localhost:7004"), secret, idTok),
		Soochna:  svc.New(config.String("SOOCHNA_URL", "http://localhost:7006"), secret, idTok),
		Pehchaan: svc.New(config.String("PEHCHAAN_URL", "http://localhost:7001"), secret, idTok),
	}
	ai := llm.New(llm.Config{
		APIKey:          config.String("ANTHROPIC_API_KEY", ""),
		SmartModel:      config.String("AI_MODEL_SMART", "claude-opus-5-5"),
		FastModel:       config.String("AI_MODEL_FAST", "claude-opus-5-5"),
		RefusalFallback: config.Bool("AI_REFUSAL_FALLBACK", true),
		Timeout:         config.Duration("AI_TIMEOUT", 3*time.Minute),
	})
	if !ai.Enabled() {
		app.Log.Warn("ANTHROPIC_API_KEY is empty: AI endpoints will answer 503; dashboards, forecasts and anomalies still work")
	}
	guard := &store.Guard{
		DB:            app.DB,
		DefaultBudget: int64(config.Int("AI_MONTHLY_TOKEN_BUDGET", 2_000_000)),
		RequireFlag:   config.Bool("AI_REQUIRE_TENANT_FLAG", true),
		Now:           time.Now,
	}
	k := &kpi.Service{DB: app.DB, Redis: app.Redis, Log: app.Log, Now: time.Now}
	a := &api.API{
		DB: app.DB, LLM: ai, Guard: guard, KPI: k, Clients: cl, Log: app.Log, Now: time.Now,
		Forecaster: &jobs.Forecaster{
			DB: app.DB, Clients: cl, Log: app.Log, Now: time.Now,
			HistoryDays: config.Int("FORECAST_HISTORY_DAYS", 56),
			LeadDays:    config.Int("FORECAST_LEAD_DAYS", 7),
			CoverDays:   config.Int("FORECAST_COVER_DAYS", 7),
		},
		Weekly: &jobs.Weekly{DB: app.DB, Guard: guard, KPI: k, LLM: ai, Clients: cl, Log: app.Log, Now: time.Now},
	}

	mux := http.NewServeMux()
	a.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
