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

	"github.com/taraju/bhandar/internal/inventory"
	"github.com/taraju/bhandar/migrations"
	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
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
	}
	mux := http.NewServeMux()
	svc.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
