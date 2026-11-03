// Package messaging is soochna's core: accept a message, render it, check
// consent and quiet hours, send it through the channel's provider, log it,
// retry failures into a dead-letter table, track delivery, and turn platform
// events into customer/owner notifications.
package messaging

import (
	"net/mail"
	"strings"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// SendInput is the body of POST /internal/v1/messages (ARCHITECTURE.md §6).
type SendInput struct {
	Channel  string            `json:"channel"` // sms | whatsapp | email
	To       string            `json:"to"`      // Indian mobile, or an e-mail address for channel=email
	Template string            `json:"template"`
	Params   map[string]string `json:"params"`
	// MediaURL is an https document: a WhatsApp document message (e.g. the
	// invoice PDF) or an e-mail attachment. It must be fetchable by the
	// provider without auth (signed / public link).
	MediaURL       string `json:"media_url,omitempty"`
	MediaFilename  string `json:"media_filename,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Validate normalises and checks the input.
func (in *SendInput) Validate() error {
	switch in.Channel {
	case templates.SMS, templates.WhatsApp:
		phone, err := NormalizePhone(in.To)
		if err != nil {
			return httpx.Validation("to: %s", err.Error())
		}
		in.To = phone
	case templates.Email:
		addr, err := NormalizeEmail(in.To)
		if err != nil {
			return httpx.Validation("to: %s", err.Error())
		}
		in.To = addr
	default:
		return httpx.Validation("channel must be sms, whatsapp or email")
	}
	if !templates.Known(in.Template) {
		return httpx.Validation("unknown template %q (one of %s)", in.Template, strings.Join(templates.Names(), ", "))
	}
	if !templates.HasChannel(in.Template, in.Channel) {
		return httpx.Validation("template %s has no %s variant", in.Template, in.Channel)
	}
	if in.MediaURL != "" {
		if !strings.HasPrefix(in.MediaURL, "https://") || len(in.MediaURL) > 2000 {
			return httpx.Validation("media_url must be an https URL (max 2000 chars)")
		}
		if in.Channel == templates.SMS {
			return httpx.Validation("media_url is not supported on sms")
		}
	}
	if len(in.MediaFilename) > 120 || strings.ContainsAny(in.MediaFilename, "/\\\r\n\"") {
		return httpx.Validation("media_filename must be a plain file name (max 120 chars)")
	}
	if len(in.IdempotencyKey) > 200 {
		return httpx.Validation("idempotency_key too long")
	}
	if in.Params == nil {
		in.Params = map[string]string{}
	}
	return nil
}

// Message statuses.
const (
	StatusQueued     = "queued"
	StatusScheduled  = "scheduled"
	StatusSent       = "sent"
	StatusDelivered  = "delivered"
	StatusRead       = "read"
	StatusFailed     = "failed"
	StatusDead       = "dead"
	StatusSuppressed = "suppressed"
)

// Message is a stored message.
type Message struct {
	ID            string            `json:"id"`
	TenantID      *string           `json:"tenant_id,omitempty"`
	Channel       string            `json:"channel"`
	To            string            `json:"to"`
	Template      string            `json:"template"`
	Category      string            `json:"category"`
	Params        map[string]string `json:"params"`
	Subject       *string           `json:"subject,omitempty"`
	Body          string            `json:"body"`
	MediaURL      *string           `json:"media_url,omitempty"`
	MediaFilename *string           `json:"media_filename,omitempty"`
	Status        string            `json:"status"`
	Provider      *string           `json:"provider,omitempty"`
	ProviderRef   *string           `json:"provider_ref,omitempty"`
	Error         *string           `json:"error,omitempty"`
	Attempts      int               `json:"attempts"`
	NextAttemptAt *time.Time        `json:"next_attempt_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	SentAt        *time.Time        `json:"sent_at,omitempty"`
}

// SendResult is the 202 response.
type SendResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Reason explains suppressed / scheduled results.
	Reason string `json:"reason,omitempty"`
}

// NormalizePhone returns +91XXXXXXXXXX for an Indian mobile number.
func NormalizePhone(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	}
	if len(d) != 10 || d[0] < '6' {
		return "", errInvalidPhone
	}
	return "+91" + d, nil
}

// NormalizeEmail returns the bare, lower-cased address.
func NormalizeEmail(raw string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || strings.ContainsAny(a.Address, "\r\n") || !strings.Contains(a.Address[strings.LastIndexByte(a.Address, '@')+1:], ".") {
		return "", errInvalidEmail
	}
	return strings.ToLower(a.Address), nil
}

// NormalizeAddress normalises a phone or, if it contains "@", an e-mail address.
func NormalizeAddress(raw string) (string, error) {
	if strings.Contains(raw, "@") {
		return NormalizeEmail(raw)
	}
	return NormalizePhone(raw)
}

type strErr string

func (e strErr) Error() string { return string(e) }

var (
	errInvalidPhone error = strErr("not a valid Indian mobile number")
	errInvalidEmail error = strErr("not a valid e-mail address")
)

// redact removes secrets (OTP codes) before a message is stored.
func redact(template string, params map[string]string, body string) (map[string]string, string) {
	code, ok := params["otp"]
	if template != templates.OTP || !ok || code == "" {
		return params, body
	}
	safe := make(map[string]string, len(params))
	for k, v := range params {
		safe[k] = v
	}
	safe["otp"] = "******"
	return safe, strings.ReplaceAll(body, code, "******")
}
