// Package providers sends SMS and WhatsApp messages. Each provider implements
// Sender; the driver is chosen by env (SMS_PROVIDER, WHATSAPP_PROVIDER).
package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	MediaURL string            // optional document (WhatsApp)
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
	c.Log.Info("CONSOLE MESSAGE", "channel", m.Channel, "to", m.To, "template", m.Template, "body", m.Body, "media_url", m.MediaURL)
	return "console-" + uuid.NewString(), nil
}

// digits strips "+" for providers that want 91XXXXXXXXXX.
func digits(phone string) string { return strings.TrimPrefix(phone, "+") }

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
}

// Build returns the SMS and WhatsApp senders.
func Build(cfg Config, log *slog.Logger) (sms, whatsapp Sender, err error) {
	console := &Console{Log: log}
	switch cfg.SMSProvider {
	case "", "console":
		sms = console
	case "msg91":
		if cfg.MSG91AuthKey == "" {
			return nil, nil, fmt.Errorf("SMS_PROVIDER=msg91 needs MSG91_AUTH_KEY")
		}
		sms = NewMSG91(cfg)
	default:
		return nil, nil, fmt.Errorf("unknown SMS_PROVIDER %q", cfg.SMSProvider)
	}
	switch cfg.WhatsAppProvider {
	case "", "console":
		whatsapp = console
	case "gupshup":
		if cfg.GupshupAPIKey == "" || cfg.GupshupSource == "" {
			return nil, nil, fmt.Errorf("WHATSAPP_PROVIDER=gupshup needs GUPSHUP_API_KEY and GUPSHUP_SOURCE")
		}
		whatsapp = NewGupshup(cfg)
	default:
		return nil, nil, fmt.Errorf("unknown WHATSAPP_PROVIDER %q", cfg.WhatsAppProvider)
	}
	return sms, whatsapp, nil
}
