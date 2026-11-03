// bhandar (भंडार) — inventory service: item master, barcodes, multi-store stock
// ledger with weighted-average costing, batches & expiry, transfers, re-order
// rules and stock analytics.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maapan/bhandar/internal/inventory"
	"github.com/maapan/bhandar/migrations"
	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/svc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bhandar:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "bhandar", DefaultPort: "7008", DB: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}
	svc := &inventory.Service{
		DB: app.DB, Relay: app.Relay, Log: app.Log, Now: time.Now,
		ExpiryAlertDays: config.Int("EXPIRY_ALERT_DAYS", 30),
		// HSN suggestions read kar's master; empty KAR_URL = local item master only.
		Kar: svc.New(config.String("KAR_URL", "http://localhost:7003"), app.Cfg.ServiceSecret, app.Cfg.UseIDTokens),
	}
	mux := http.NewServeMux()
	svc.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
