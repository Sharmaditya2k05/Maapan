// aadesh (आदेश) — order & billing service: POS sales, purchases, returns, numbering, offline sync.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maapan/aadesh/internal/clients"
	"github.com/maapan/aadesh/internal/invoice"
	"github.com/maapan/aadesh/migrations"
	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/svc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "aadesh:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "aadesh", DefaultPort: "7004", DB: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}
	secret, idTok := app.Cfg.ServiceSecret, app.Cfg.UseIDTokens
	dir := clients.NewDirectory(
		svc.New(config.String("PEHCHAAN_URL", "http://localhost:7001"), secret, idTok),
		svc.New(config.String("BAHI_URL", "http://localhost:7002"), secret, idTok),
		svc.New(config.String("BHANDAR_URL", "http://localhost:7008"), secret, idTok),
		svc.New(config.String("LENDEN_URL", "http://localhost:7005"), secret, idTok),
	)
	invoices := &invoice.Service{DB: app.DB, Dir: dir, Relay: app.Relay, Log: app.Log, Now: time.Now}

	mux := http.NewServeMux()
	invoices.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}
