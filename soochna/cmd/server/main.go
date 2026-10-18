// soochna (सूचना) — notification service: SMS (MSG91) and WhatsApp (Gupshup) delivery, templates, delivery log.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/taraju/mool/boot"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/svc"
	"github.com/taraju/soochna/internal/clients"
	"github.com/taraju/soochna/internal/messaging"
	"github.com/taraju/soochna/internal/providers"
	"github.com/taraju/soochna/internal/templates"
	"github.com/taraju/soochna/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "soochna:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := boot.New(ctx, boot.Options{
		Name: "soochna", DefaultPort: "7006", DB: true, Migrations: migrations.FS,
	})
	if err != nil {
		return err
	}
	pcfg := providers.Config{
		SMSProvider:      config.String("SMS_PROVIDER", "console"),
		WhatsAppProvider: config.String("WHATSAPP_PROVIDER", "console"),
		MSG91AuthKey:     config.String("MSG91_AUTH_KEY", ""),
		MSG91BaseURL:     config.String("MSG91_BASE_URL", ""),
		MSG91Templates:   templateIDs("MSG91_TEMPLATE_"),
		GupshupAPIKey:    config.String("GUPSHUP_API_KEY", ""),
		GupshupSource:    config.String("GUPSHUP_SOURCE", ""),
		GupshupAppName:   config.String("GUPSHUP_APP_NAME", ""),
		GupshupBaseURL:   config.String("GUPSHUP_BASE_URL", ""),
		GupshupTemplates: templateIDs("GUPSHUP_TEMPLATE_"),
	}
	sms, wa, err := providers.Build(pcfg, app.Log)
	if err != nil {
		return err
	}
	msgs := &messaging.Service{
		DB: app.DB, SMS: sms, WhatsApp: wa, Log: app.Log,
		Tenants:      clients.NewTenants(svc.New(config.String("PEHCHAAN_URL", "http://localhost:7001"), app.Cfg.ServiceSecret, app.Cfg.UseIDTokens)),
		WebhookToken: config.String("WEBHOOK_TOKEN", ""),
	}
	app.Log.Info("providers", "sms", sms.Name(), "whatsapp", wa.Name())

	mux := http.NewServeMux()
	msgs.Routes(mux, app.Verifier, app.TrustIAMForEvents())
	return app.Run(ctx, mux)
}

// templateIDs reads <prefix><TEMPLATE> env vars, e.g. MSG91_TEMPLATE_OTP.
func templateIDs(prefix string) map[string]string {
	out := map[string]string{}
	for _, n := range templates.Names() {
		if v := config.String(prefix+strings.ToUpper(n), ""); v != "" {
			out[n] = v
		}
	}
	return out
}
