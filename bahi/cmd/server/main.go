// bahi (बही) — ledger service: chart of accounts, parties, double-entry vouchers,
// cash & bank books, cheques and financial reports.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taraju/bahi/internal/ledger"
	"github.com/taraju/bahi/migrations"
	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/svc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bahi:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "bahi", DefaultPort: "7002", DB: true, Migrations: migrations.FS,
	})
	if err != nil {
		return err
	}
	var kar *svc.Client
	if u := config.String("KAR_URL", ""); u != "" {
		kar = svc.New(u, app.Cfg.ServiceSecret, app.Cfg.UseIDTokens)
	}
	books := &ledger.Service{DB: app.DB, Kar: kar, Log: app.Log, Now: time.Now}

	mux := http.NewServeMux()
	books.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
