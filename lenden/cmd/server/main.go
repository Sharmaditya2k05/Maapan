// lenden (लेनदेन) — payments: Razorpay / Cashfree UPI QR & payment links, webhooks,
// manual receipts, refunds, settlements, per-tenant gateway keys and reconciliation.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maapan/lenden/internal/creds"
	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/lenden/internal/payment"
	"github.com/maapan/lenden/migrations"
	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lenden:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "lenden", DefaultPort: "7005", DB: true, Migrations: migrations.FS, Outbox: true,
	})
	if err != nil {
		return err
	}

	// Per-tenant gateway keys are sealed with CREDENTIALS_KEY (derived from
	// SERVICE_SECRET outside prod when unset; prod refuses to start without it).
	cipher, err := creds.Load(config.String("CREDENTIALS_KEY", ""), app.Cfg.ServiceSecret, app.Cfg.IsProd(), app.Log)
	if err != nil {
		return err
	}
	store := &creds.Store{DB: app.DB, Cipher: cipher}

	gws, err := buildGateways(app.Cfg.IsProd(), app.Log)
	if err != nil {
		return err
	}
	gws.Creds = store

	ledger := &payment.PGLedger{DB: app.DB, Relay: app.Relay}
	svc := &payment.Service{
		DB: app.DB, Gateways: gws, Ledger: ledger, Relay: app.Relay, Log: app.Log, Creds: store,
		PayeeName: config.String("PAYEE_DISPLAY_NAME", "Maapan Store"), Now: time.Now,
	}
	var hooks []*payment.Webhooks
	for _, src := range gws.Webhooks() {
		hooks = append(hooks, &payment.Webhooks{Source: src, Ledger: ledger, Secrets: store, Log: app.Log})
	}

	mux := http.NewServeMux()
	svc.Routes(mux, app.Verifier, hooks)
	return app.Run(ctx, mux)
}

// buildGateways enables every gateway whose platform credentials are set, and
// makes PAYMENT_GATEWAY the default for new payment requests. Keeping all of
// them enabled means payments taken on one gateway still refund and reconcile
// after the default is switched to another. Razorpay and Cashfree are always
// enabled for tenants that store their own keys, so their webhooks are mounted
// even when the platform has no keys of its own.
func buildGateways(prod bool, log *slog.Logger) (*gateway.Registry, error) {
	reg := gateway.NewRegistry()

	rzpWebhook := gateway.RazorpayWebhooks{Secret: cred("RAZORPAY_WEBHOOK_SECRET")}
	if id, secret := cred("RAZORPAY_KEY_ID"), cred("RAZORPAY_KEY_SECRET"); id != "" || secret != "" {
		if id == "" || secret == "" || rzpWebhook.Secret == "" {
			return nil, fmt.Errorf("razorpay needs RAZORPAY_KEY_ID, RAZORPAY_KEY_SECRET and RAZORPAY_WEBHOOK_SECRET")
		}
		reg.Add(gateway.NewRazorpay(id, secret), rzpWebhook)
	}
	reg.Enable("razorpay", gateway.RazorpayFactory("", nil), rzpWebhook)

	cfEnv := config.String("CASHFREE_ENV", "sandbox")
	if cfEnv != "sandbox" && cfEnv != "production" {
		return nil, fmt.Errorf("CASHFREE_ENV must be sandbox or production")
	}
	cfVersion := config.String("CASHFREE_API_VERSION", "2023-08-01")
	cfPhone := config.String("CASHFREE_FALLBACK_PHONE", "")
	cfBase := ""
	if u := config.String("CASHFREE_BASE_URL", ""); u != "" && !prod {
		cfBase = u // local stand-in server for tests; ignored in prod
	}
	cfWebhook := gateway.CashfreeWebhooks{Secret: cred("CASHFREE_CLIENT_SECRET"),
		MaxSkew: config.Duration("CASHFREE_WEBHOOK_MAX_SKEW", 10*time.Minute)}
	if id, secret := cred("CASHFREE_CLIENT_ID"), cred("CASHFREE_CLIENT_SECRET"); id != "" || secret != "" {
		if id == "" || secret == "" {
			return nil, fmt.Errorf("cashfree needs CASHFREE_CLIENT_ID and CASHFREE_CLIENT_SECRET")
		}
		if prod && cfEnv != "production" {
			log.Warn("cashfree is on the SANDBOX environment in prod")
		}
		cf := gateway.NewCashfree(id, secret, cfEnv, cfVersion, cfPhone)
		if cfBase != "" {
			cf.BaseURL = cfBase
		}
		reg.Add(cf, cfWebhook)
	}
	reg.Enable("cashfree", gateway.CashfreeFactory(cfVersion, cfPhone, cfBase, nil), cfWebhook)

	if !prod {
		reg.Add(gateway.NewMock(config.String("MOCK_UPI_VPA", "maapan@mock")), nil)
	}

	def := config.String("PAYMENT_GATEWAY", "mock")
	if def == "mock" && prod {
		return nil, fmt.Errorf("PAYMENT_GATEWAY=mock is not allowed in prod")
	}
	if err := reg.SetDefault(def); err != nil {
		return nil, fmt.Errorf("PAYMENT_GATEWAY: %w", err)
	}
	if def == "mock" {
		log.Warn("using MOCK payment gateway; POST /v1/dev/simulate-payment/{id} marks a request paid")
	}
	log.Info("payment gateways ready", "default", def, "platform_keys", reg.Names(), "tenant_configurable", reg.Supported())
	return reg, nil
}

// cred reads a credential, treating the CHANGE_ME placeholders that
// gcp-bootstrap.sh puts in Secret Manager as "not configured".
func cred(key string) string {
	v := config.String(key, "")
	if strings.HasPrefix(v, "CHANGE_ME") {
		return ""
	}
	return v
}
