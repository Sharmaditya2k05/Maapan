// Package providers sends SMS, WhatsApp and e-mail. Each SMS/WhatsApp provider
// implements Sender, each e-mail provider Mailer; the driver is chosen by env
// (SMS_PROVIDER, WHATSAPP_PROVIDER, EMAIL_PROVIDER).
package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"

	"github.com/google/uuid"
)

// Message is what a provider needs to deliver.
type Message struct {
	Channel  string            // sms | whatsapp
	To       string            // +91XXXXXXXXXX
	Template string            // templates.* name
	Params   map[string]string // template variables
	Body     string            // rendered text (console / free-form WhatsApp)
	MediaURL string            // optional document (WhatsApp), https
	// MediaFilename is the file name shown for MediaURL ("" = last path segment).
	MediaFilename string

	// Per-tenant overrides from the sender/template registry ("" = provider default).
	SenderID   string // MSG91 DLT sender id (header) | Gupshup source number
	AppName    string // Gupshup app name
	TemplateID string // provider template id for this tenant
}

// Sender delivers one message and returns the provider's reference id.
type Sender interface {
	Name() string
	Send(ctx context.Context, m Message) (ref string, err error)
}

// ErrNotConfigured means the provider lacks keys or a template id.
var ErrNotConfigured = errors.New("provider not configured")

// Console logs messages instead of sending them (local dev, tests).
type Console struct{ Log *slog.Logger }

func (c *Console) Name() string { return "console" }

func (c *Console) Send(_ context.Context, m Message) (string, error) {
	c.Log.Info("CONSOLE MESSAGE", "channel", m.Channel, "to", m.To, "template", m.Template, "body", m.Body,
		"media_url", m.MediaURL, "sender_id", m.SenderID, "template_id", m.TemplateID)
	return "console-" + uuid.NewString(), nil
}

// digits strips "+" for providers that want 91XXXXXXXXXX.
func digits(phone string) string { return strings.TrimPrefix(phone, "+") }

// fileName is the display name of a document: explicit name, else the URL's
// last path segment without query, else "document.pdf".
func fileName(explicit, rawURL string) string {
	if explicit != "" {
		return explicit
	}
	if u, err := url.Parse(rawURL); err == nil {
		if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
			return b
		}
	}
	return "document.pdf"
}

// Config holds provider credentials (from env).
type Config struct {
	SMSProvider      string // console | msg91
	WhatsAppProvider string // console | gupshup

	MSG91AuthKey   string
	MSG91BaseURL   string
	MSG91Templates map[string]string // template name → MSG91 flow template_id

	GupshupAPIKey    string
	GupshupSource    string // WhatsApp business number, 91XXXXXXXXXX
	GupshupAppName   string
	GupshupBaseURL   string
	GupshupTemplates map[string]string // template name → approved WhatsApp template id

	EmailProvider string // console | smtp
	SMTP          SMTPConfig
}

// Senders are the configured channel drivers.
type Senders struct {
	SMS      Sender
	WhatsApp Sender
	Email    Mailer
}

// Build returns the SMS, WhatsApp and e-mail drivers.
func Build(cfg Config, log *slog.Logger) (Senders, error) {
	var out Senders
	console := &Console{Log: log}
	switch cfg.SMSProvider {
	case "", "console":
		out.SMS = console
	case "msg91":
		if cfg.MSG91AuthKey == "" {
			return out, fmt.Errorf("SMS_PROVIDER=msg91 needs MSG91_AUTH_KEY")
		}
		out.SMS = NewMSG91(cfg)
	default:
		return out, fmt.Errorf("unknown SMS_PROVIDER %q", cfg.SMSProvider)
	}
	switch cfg.WhatsAppProvider {
	case "", "console":
		out.WhatsApp = console
	case "gupshup":
		if cfg.GupshupAPIKey == "" || cfg.GupshupSource == "" {
			return out, fmt.Errorf("WHATSAPP_PROVIDER=gupshup needs GUPSHUP_API_KEY and GUPSHUP_SOURCE")
		}
		out.WhatsApp = NewGupshup(cfg)
	default:
		return out, fmt.Errorf("unknown WHATSAPP_PROVIDER %q", cfg.WhatsAppProvider)
	}
	switch cfg.EmailProvider {
	case "", "console":
		out.Email = &ConsoleMailer{Log: log}
	case "smtp":
		if cfg.SMTP.Host == "" || cfg.SMTP.From == "" {
			return out, fmt.Errorf("EMAIL_PROVIDER=smtp needs SMTP_HOST and EMAIL_FROM")
		}
		out.Email = NewSMTP(cfg.SMTP)
	default:
		return out, fmt.Errorf("unknown EMAIL_PROVIDER %q", cfg.EmailProvider)
	}
	return out, nil
}
