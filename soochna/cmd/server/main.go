// soochna (सूचना) — notification service: SMS (MSG91), WhatsApp (Gupshup) and e-mail (SMTP) delivery,
// templates, consent, dead letters, payment reminders and the delivery log.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maapan/mool/boot"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/svc"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/jobauth"
	"github.com/maapan/soochna/internal/messaging"
	"github.com/maapan/soochna/internal/providers"
	"github.com/maapan/soochna/internal/templates"
	"github.com/maapan/soochna/migrations"
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
		EmailProvider:    config.String("EMAIL_PROVIDER", "console"),
		SMTP: providers.SMTPConfig{
			Host:     config.String("SMTP_HOST", ""),
			Port:     config.Int("SMTP_PORT", 587),
			Username: config.String("SMTP_USERNAME", ""),
			Password: config.String("SMTP_PASSWORD", ""),
			From:     config.String("EMAIL_FROM", ""),
			FromName: config.String("EMAIL_FROM_NAME", "Maapan"),
			ReplyTo:  config.String("EMAIL_REPLY_TO", ""),
		},
	}
	drivers, err := providers.Build(pcfg, app.Log)
	if err != nil {
		return err
	}
	quiet, err := messaging.ParseQuietHours(config.String("QUIET_HOURS", "21:00-09:00"), nil)
	if err != nil {
		return err
	}
	secret, idTok := app.Cfg.ServiceSecret, app.Cfg.UseIDTokens
	msgs := &messaging.Service{
		DB: app.DB, SMS: drivers.SMS, WhatsApp: drivers.WhatsApp, Email: drivers.Email, Log: app.Log,
		Tenants:            clients.NewTenants(svc.New(config.String("PEHCHAAN_URL", "http://localhost:7001"), secret, idTok)),
		WebhookToken:       config.String("WEBHOOK_TOKEN", ""),
		Quiet:              quiet,
		PromoRequiresOptIn: config.Bool("PROMO_REQUIRES_OPT_IN", true),
		InvoicePDFURL:      config.String("INVOICE_PDF_URL", ""),
	}
	if u := config.String("BAHI_URL", ""); u != "" {
		msgs.Bahi = &clients.BahiClient{C: svc.New(u, secret, idTok)}
	}
	oidc := &jobauth.OIDC{
		Audience: config.String("JOBS_OIDC_AUDIENCE", ""),
		Emails:   config.List("JOBS_OIDC_EMAILS", nil),
	}
	app.Log.Info("providers", "sms", drivers.SMS.Name(), "whatsapp", drivers.WhatsApp.Name(), "email", drivers.Email.Name(),
		"bahi", msgs.Bahi != nil, "jobs_oidc", oidc.Enabled(), "quiet_hours", quiet.Enabled())

	mux := http.NewServeMux()
	msgs.Routes(mux, app.Verifier, app.TrustIAMForEvents(), jobauth.Middleware(app.Verifier, oidc))
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
