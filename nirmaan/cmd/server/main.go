// nirmaan (निर्माण) — manufacturing service: multi-level BOMs, cost roll-up,
// production runs that consume raw material and produce finished goods.
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
	"github.com/taraju/nirmaan/internal/bom"
	"github.com/taraju/nirmaan/internal/clients"
	"github.com/taraju/nirmaan/internal/production"
	"github.com/taraju/nirmaan/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "nirmaan:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "nirmaan", DefaultPort: "7009", DB: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}
	bhandar := &clients.Bhandar{C: svc.New(config.String("BHANDAR_URL", "http://localhost:7008"), app.Cfg.ServiceSecret, app.Cfg.UseIDTokens)}
	boms := &bom.Service{DB: app.DB, Bhandar: bhandar, Log: app.Log, Now: time.Now}
	runs := &production.Service{DB: app.DB, BOMs: boms, Bhandar: bhandar, Relay: app.Relay, Log: app.Log, Now: time.Now}

	mux := http.NewServeMux()
	boms.Routes(mux, app.Verifier)
	runs.Routes(mux, app.Verifier)
	return app.Run(ctx, mux)
}
