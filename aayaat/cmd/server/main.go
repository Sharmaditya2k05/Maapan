// aayaat (आयात) — import service: MargBooks / Excel / CSV migration into bahi and bhandar.
//
// Normal mode serves HTTP. Worker mode (`aayaat -job <id> -tenant <id>`) commits
// one large import and exits; it runs as the `aayaat-worker` Cloud Run Job.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maapan/aayaat/internal/clients"
	"github.com/maapan/aayaat/internal/job"
	"github.com/maapan/aayaat/internal/storage"
	"github.com/maapan/aayaat/migrations"
	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/svc"
)

func main() {
	jobID := flag.String("job", "", "commit this import job and exit (Cloud Run Job mode)")
	tenantID := flag.String("tenant", "", "tenant of -job")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *jobID, *tenantID); err != nil {
		fmt.Fprintln(os.Stderr, "aayaat:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, jobID, tenantID string) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "aayaat", DefaultPort: "7007", DB: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}
	secret, idTok := app.Cfg.ServiceSecret, app.Cfg.UseIDTokens

	var store storage.Storage = storage.Local{Dir: config.String("STORAGE_DIR", "./data/uploads")}
	if config.String("STORAGE_DRIVER", "local") == "gcs" {
		bucket := config.String("GCS_BUCKET", "")
		if bucket == "" {
			return fmt.Errorf("STORAGE_DRIVER=gcs needs GCS_BUCKET")
		}
		store = storage.NewGCS(bucket)
	}

	s := &job.Service{
		DB: app.DB, Store: store, Relay: app.Relay, Log: app.Log, Now: time.Now,
		Targets: clients.Targets{
			Bahi:    svc.New(config.String("BAHI_URL", "http://localhost:7002"), secret, idTok),
			Bhandar: svc.New(config.String("BHANDAR_URL", "http://localhost:7008"), secret, idTok),
		},
		LargeRows: config.Int("LARGE_IMPORT_ROWS", 50_000),
	}
	// Bulk calls can be slow for big batches.
	s.Targets.Bahi.HTTP.Timeout, s.Targets.Bhandar.HTTP.Timeout = 2*time.Minute, 2*time.Minute
	if name := config.String("WORKER_JOB_NAME", ""); name != "" {
		s.Worker = job.NewCloudRunJob(app.Cfg.GCPProject, config.String("GCP_REGION", "asia-south1"), name)
	}

	if jobID != "" {
		defer app.Close()
		if tenantID == "" {
			return fmt.Errorf("-job needs -tenant")
		}
		app.Log.Info("worker mode", "job_id", jobID)
		if err := s.RunCommit(ctx, tenantID, jobID); err != nil {
			return err
		}
		return app.Relay.Flush(ctx) // publish aayaat.import.completed before exiting
	}

	s.RecoverStale(ctx, 15*time.Minute)
	mux := http.NewServeMux()
	s.Routes(mux, app.Verifier)
	return app.Run(ctx, mux)
}
