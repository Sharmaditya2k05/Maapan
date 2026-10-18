// Package messaging is soochna's core: accept a message, render it, send it
// through the channel's provider, log it, retry failures, track delivery and
// turn platform events into customer/owner notifications.
package messaging

import (
	"strings"
	"time"

	"github.com/taraju/mool/httpx"
	"github.com/taraju/soochna/internal/templates"
)

// SendInput is the body of POST /internal/v1/messages (ARCHITECTURE.md §6).
type SendInput struct {
	Channel        string            `json:"channel"`
	To             string            `json:"to"`
	Template       string            `json:"template"`
	Params         map[string]string `json:"params"`
	MediaURL       string            `json:"media_url,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// Validate normalises and checks the input.
func (in *SendInput) Validate() error {
	if in.Channel != "sms" && in.Channel != "whatsapp" {
		return httpx.Validation("channel must be sms or whatsapp")
	}
	phone, err := NormalizePhone(in.To)
	if err != nil {
		return httpx.Validation("to: %s", err.Error())
	}
	in.To = phone
	if !templates.Known(in.Template) {
		return httpx.Validation("unknown template %q (one of %s)", in.Template, strings.Join(templates.Names(), ", "))
	}
	if in.MediaURL != "" && !strings.HasPrefix(in.MediaURL, "https://") {
		return httpx.Validation("media_url must be https")
	}
	if len(in.IdempotencyKey) > 200 {
		return httpx.Validation("idempotency_key too long")
	}
	if in.Params == nil {
		in.Params = map[string]string{}
	}
	return nil
}

// Message is a stored message.
type Message struct {
	ID          string            `json:"id"`
	TenantID    *string           `json:"tenant_id,omitempty"`
	Channel     string            `json:"channel"`
	To          string            `json:"to"`
	Template    string            `json:"template"`
	Params      map[string]string `json:"params"`
	Body        string            `json:"body"`
	MediaURL    *string           `json:"media_url,omitempty"`
	Status      string            `json:"status"`
	Provider    *string           `json:"provider,omitempty"`
	ProviderRef *string           `json:"provider_ref,omitempty"`
	Error       *string           `json:"error,omitempty"`
	Attempts    int               `json:"attempts"`
	CreatedAt   time.Time         `json:"created_at"`
	SentAt      *time.Time        `json:"sent_at,omitempty"`
}

// SendResult is the 202 response.
type SendResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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

type phoneErr struct{}

func (phoneErr) Error() string { return "not a valid Indian mobile number" }

var errInvalidPhone error = phoneErr{}

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
