// kar (कर) — tax service: GSTIN verification, HSN master, GST maths,
// e-Invoice (IRN) and e-Way bill lifecycle through a GSP, GSTR returns.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/taraju/kar/internal/api"
	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/compliance"
	"github.com/taraju/kar/internal/creds"
	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/kar/internal/gstin"
	"github.com/taraju/kar/internal/hsn"
	"github.com/taraju/kar/internal/returns"
	"github.com/taraju/kar/migrations"
	"github.com/taraju/mool/auth"
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
		Name: "kar", DefaultPort: "7003", DB: true, Migrations: migrations.FS, Outbox: true,
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

	client, err := gspClient(app)
	if err != nil {
		return err
	}
	app.Log.Info("gsp provider", "provider", client.Name())

	cipher, err := creds.Load(config.String("CREDENTIALS_KEY", ""), secret, app.Cfg.IsProd(), app.Log)
	if err != nil {
		return err
	}
	credStore := &creds.Store{DB: app.DB, Cipher: cipher}

	var provider gstin.Provider = gstin.ChecksumOnly{}
	if config.String("GSTIN_PROVIDER", "checksum") == "gsp" {
		provider = gstin.GSP{Client: client}
	}
	app.Log.Info("gstin provider", "provider", provider.Name())

	engine := &compliance.Engine{DB: app.DB, Dir: dir, GSP: client, Creds: credStore, Log: app.Log,
		MaxAttempts: config.Int("EINVOICE_MAX_ATTEMPTS", 5)}
	if app.Relay != nil {
		engine.Kick = app.Relay.Kick
	}

	importers := config.List("HSN_IMPORT_TENANTS", nil)
	s := &api.Service{
		DB: app.DB, Dir: dir, Log: app.Log, Engine: engine, Creds: credStore,
		HSN: &hsn.Store{DB: app.DB},
		GSTIN: &gstin.Service{DB: app.DB, Redis: app.Redis, Provider: provider,
			TTL: config.Duration("GSTIN_CACHE_TTL", 7*24*time.Hour), Log: app.Log},
		HSNImportAllowed: func(tenantID string) bool {
			if len(importers) == 0 {
				return !app.Cfg.IsProd() // local/dev: any admin; prod: only listed tenants (or /internal)
			}
			for _, t := range importers {
				if strings.EqualFold(strings.TrimSpace(t), tenantID) {
					return true
				}
			}
			return false
		},
	}
	mux := http.NewServeMux()
	s.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	// GSTR-1 / GSTR-3B / GSTR-2B and IMS. 2B is pulled through the GSP with the
	// GSTIN's own portal credentials (the simulator answers an empty 2B).
	returns.Register(mux, app.Verifier, returns.Deps{
		DB: app.DB, Log: app.Log,
		Invoices:      returns.AadeshInvoices{C: dir.Aadesh},
		Registrations: returns.PehchaanRegistrations{C: dir.Pehchaan},
		TwoB: returns.TwoBFunc(func(ctx context.Context, gstin, period string) ([]byte, error) {
			c, _, err := credStore.Credentials(ctx, auth.FromContext(ctx).TenantID, gstin)
			if err != nil {
				return nil, err
			}
			return client.FetchGSTR2B(ctx, c, gstin, period)
		}),
	})
	return app.Run(ctx, mux)
}

// gspClient selects the GSP driver: GSP_PROVIDER=simulator (default outside
// prod) or http. Production refuses the simulator.
func gspClient(app *boot.App) (gsp.Client, error) {
	def := "simulator"
	if app.Cfg.IsProd() {
		def = ""
	}
	switch p := config.String("GSP_PROVIDER", def); p {
	case "simulator":
		if app.Cfg.IsProd() {
			return nil, fmt.Errorf("GSP_PROVIDER=simulator is not allowed in prod: nothing it issues is registered with the IRP/NIC")
		}
		key := []byte(config.String("GSP_SIM_KEY", ""))
		if len(key) == 0 {
			sum := sha256.Sum256([]byte("kar-gsp-simulator:" + app.Cfg.ServiceSecret))
			key = sum[:]
		}
		app.Log.Warn("GSP simulator active: IRNs and e-way bills are NOT registered with the government (local/dev only)")
		return gsp.NewSimulator(&gsp.PGStore{DB: app.DB}, key), nil
	case "http":
		return gsp.NewHTTP(gsp.HTTPConfig{
			BaseURL: config.String("GSP_BASE_URL", ""), ClientID: config.String("GSP_CLIENT_ID", ""),
			ClientSecret: config.String("GSP_CLIENT_SECRET", ""), TokenPath: config.String("GSP_TOKEN_PATH", ""),
			Paths: gsp.HTTPPaths{
				Search: config.String("GSP_SEARCH_PATH", ""), GenerateIRN: config.String("GSP_IRN_PATH", ""),
				CancelIRN: config.String("GSP_IRN_CANCEL_PATH", ""), IRNByDoc: config.String("GSP_IRN_BY_DOC_PATH", ""),
				EWBByIRN: config.String("GSP_EWB_BY_IRN_PATH", ""), GenerateEWB: config.String("GSP_EWB_PATH", ""),
				PartB: config.String("GSP_EWB_PARTB_PATH", ""), Extend: config.String("GSP_EWB_EXTEND_PATH", ""),
				CancelEWB: config.String("GSP_EWB_CANCEL_PATH", ""), GetEWB: config.String("GSP_EWB_GET_PATH", ""),
				GSTR2B: config.String("GSP_GSTR2B_PATH", ""),
			},
			HeaderGSTIN: config.String("GSP_HEADER_GSTIN", ""), HeaderUsername: config.String("GSP_HEADER_USERNAME", ""),
			HeaderPassword: config.String("GSP_HEADER_PASSWORD", ""),
			Timeout:        config.Duration("GSP_TIMEOUT", 15*time.Second), Log: app.Log,
		})
	case "":
		return nil, fmt.Errorf("GSP_PROVIDER is required in prod (http)")
	default:
		return nil, fmt.Errorf("GSP_PROVIDER must be simulator or http, not %q", p)
	}
}
