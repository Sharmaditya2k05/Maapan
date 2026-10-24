package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookSource verifies and normalises one provider's webhooks. lenden mounts
// one at POST /v1/webhooks/{name} for every supported gateway.
type WebhookSource interface {
	Gateway() string
	// Verify checks the provider's signature over the raw body with the
	// platform-wide secret (false when none is configured).
	Verify(raw []byte, h http.Header) bool
	// VerifyWith checks the signature with a tenant's own webhook secret.
	VerifyWith(raw []byte, h http.Header, secret string) bool
	// Parse normalises the body into a WebhookEvent (KindIgnore for events lenden doesn't use).
	Parse(raw []byte) (WebhookEvent, error)
	// EventID is the provider's delivery id, or "" (lenden then hashes the body).
	EventID(h http.Header) string
}

// TenantSecret is one tenant's webhook secret for a gateway.
type TenantSecret struct {
	TenantID string
	Secret   string
}

// WebhookSecretSource looks up tenants' webhook secrets so a webhook can be
// verified against the keys of the store it belongs to. Implemented by creds.Store.
type WebhookSecretSource interface {
	// WebhookSecret returns tenantID's webhook secret for gateway, ok=false when none is stored.
	WebhookSecret(ctx context.Context, tenantID, gateway string) (string, bool, error)
	// AllWebhookSecrets returns every tenant's webhook secret for gateway (for
	// bodies that reference no payment, such as settlements).
	AllWebhookSecrets(ctx context.Context, gateway string) ([]TenantSecret, error)
}

// RazorpayWebhooks handles X-Razorpay-Signature webhooks.
type RazorpayWebhooks struct{ Secret string }

func (RazorpayWebhooks) Gateway() string { return "razorpay" }
func (w RazorpayWebhooks) Verify(raw []byte, h http.Header) bool {
	return w.VerifyWith(raw, h, w.Secret)
}
func (RazorpayWebhooks) VerifyWith(raw []byte, h http.Header, secret string) bool {
	return VerifySignature(raw, h.Get("X-Razorpay-Signature"), secret)
}
func (RazorpayWebhooks) Parse(raw []byte) (WebhookEvent, error) { return ParseWebhook(raw) }
func (RazorpayWebhooks) EventID(h http.Header) string           { return h.Get("X-Razorpay-Event-Id") }

// CashfreeWebhooks handles Cashfree webhooks: x-webhook-signature =
// base64(HMAC-SHA256(client secret, x-webhook-timestamp + raw body)).
type CashfreeWebhooks struct {
	Secret  string        // the Cashfree client secret
	MaxSkew time.Duration // reject deliveries older than this (0 = no check)
	Now     func() time.Time
}

func (CashfreeWebhooks) Gateway() string { return "cashfree" }

func (w CashfreeWebhooks) Verify(raw []byte, h http.Header) bool {
	return w.VerifyWith(raw, h, w.Secret)
}

func (w CashfreeWebhooks) VerifyWith(raw []byte, h http.Header, secret string) bool {
	sig, ts := strings.TrimSpace(h.Get("x-webhook-signature")), strings.TrimSpace(h.Get("x-webhook-timestamp"))
	if sig == "" || ts == "" || secret == "" {
		return false
	}
	if w.MaxSkew > 0 {
		ms, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return false
		}
		now := time.Now
		if w.Now != nil {
			now = w.Now
		}
		if d := now().Sub(time.UnixMilli(ms)); d > w.MaxSkew || d < -w.MaxSkew {
			return false
		}
	}
	return hmac.Equal([]byte(SignCashfree(raw, ts, secret)), []byte(sig))
}

// SignCashfree computes Cashfree's webhook signature (tests and local simulation use it).
func SignCashfree(raw []byte, timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write(raw)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// EventID: Cashfree sends no delivery id header, so lenden falls back to the body hash
// (retries of one delivery carry the same body).
func (CashfreeWebhooks) EventID(http.Header) string { return "" }

type cfWebhook struct {
	Type      string `json:"type"`
	EventTime string `json:"event_time"`
	Data      struct {
		Order *struct {
			OrderID           string            `json:"order_id"`
			OrderTags         map[string]string `json:"order_tags"`
			CFPaymentID       json.Number       `json:"cf_payment_id"`      // link event (newer payloads)
			TransactionID     json.Number       `json:"transaction_id"`     // link event (older payloads)
			TransactionStatus string            `json:"transaction_status"` // link event
		} `json:"order"`
		Payment *struct {
			CFPaymentID   json.Number `json:"cf_payment_id"`
			PaymentStatus string      `json:"payment_status"`
			PaymentAmount float64     `json:"payment_amount"`
			PaymentGroup  string      `json:"payment_group"`
			PaymentTime   string      `json:"payment_time"`
		} `json:"payment"`
		Refund *struct {
			CFRefundID   json.Number `json:"cf_refund_id"`
			CFPaymentID  json.Number `json:"cf_payment_id"`
			RefundID     string      `json:"refund_id"`
			OrderID      string      `json:"order_id"`
			RefundAmount float64     `json:"refund_amount"`
			RefundStatus string      `json:"refund_status"`
		} `json:"refund"`
		// PAYMENT_LINK_EVENT fields
		LinkID         string            `json:"link_id"`
		LinkStatus     string            `json:"link_status"`
		LinkAmountPaid float64           `json:"link_amount_paid"`
		LinkNotes      map[string]string `json:"link_notes"`
	} `json:"data"`
}

// Parse normalises PAYMENT_SUCCESS_WEBHOOK, PAYMENT_LINK_EVENT (PAID) and
// REFUND_STATUS_WEBHOOK (SUCCESS). Everything else is KindIgnore.
func (CashfreeWebhooks) Parse(raw []byte) (WebhookEvent, error) {
	var w cfWebhook
	if err := json.Unmarshal(raw, &w); err != nil {
		return WebhookEvent{}, err
	}
	if w.Type == "" {
		return WebhookEvent{}, errors.New("webhook has no type field")
	}
	ev := WebhookEvent{Type: w.Type, Kind: KindIgnore}
	switch w.Type {
	case "PAYMENT_SUCCESS_WEBHOOK":
		if w.Data.Order == nil || w.Data.Payment == nil {
			return ev, errors.New(w.Type + ": missing order or payment")
		}
		p, o := w.Data.Payment, w.Data.Order
		if p.PaymentStatus != "SUCCESS" {
			return ev, nil
		}
		ev.Kind = KindCapture
		ev.GatewayRef = o.OrderID
		ev.PaymentRef = CashfreePaymentRef(o.OrderID, p.CFPaymentID)
		ev.AmountPaise = paise(p.PaymentAmount)
		ev.Method = MapMethod(p.PaymentGroup)
		ev.CapturedAt = parseCFTime(p.PaymentTime)
		// QR orders carry our request id as order_id and in order_tags; link orders are
		// Cashfree-generated and are resolved by the PAYMENT_LINK_EVENT instead.
		ev.RequestID = o.OrderTags["request_id"]
		if ev.RequestID == "" {
			ev.RequestID = o.OrderID
		}
	case "PAYMENT_LINK_EVENT":
		if w.Data.LinkStatus != "PAID" || w.Data.Order == nil || w.Data.Order.TransactionStatus != "SUCCESS" {
			return ev, nil
		}
		ev.Kind = KindCapture
		ev.GatewayRef = w.Data.LinkID
		ev.RequestID = w.Data.LinkNotes["request_id"]
		if ev.RequestID == "" {
			ev.RequestID = w.Data.LinkID
		}
		// Key the payment on cf_payment_id, exactly as FetchLink does (GET /orders/{id}/payments),
		// so a reconcile after a webhook can never book the same payment twice. Older link
		// payloads only carry transaction_id, which is that same id under another name.
		payID := w.Data.Order.CFPaymentID
		if payID.String() == "" {
			payID = w.Data.Order.TransactionID
		}
		ev.PaymentRef = CashfreePaymentRef(w.Data.Order.OrderID, payID)
		ev.AmountPaise = paise(w.Data.LinkAmountPaid) // links are created without partial payments
		ev.Method = MapMethod("")
		ev.CapturedAt = parseCFTime(w.EventTime)
	case "REFUND_STATUS_WEBHOOK":
		r := w.Data.Refund
		if r == nil {
			return ev, errors.New(w.Type + ": missing refund")
		}
		if r.RefundStatus != "SUCCESS" {
			return ev, nil
		}
		ev.Kind = KindRefund
		ev.RefundRef = r.CFRefundID.String()
		ev.RefundNote = r.RefundID
		ev.PaymentRef = CashfreePaymentRef(r.OrderID, r.CFPaymentID)
		ev.AmountPaise = paise(r.RefundAmount)
	}
	if ev.Kind != KindIgnore && ev.AmountPaise <= 0 {
		return ev, errors.New(w.Type + ": amount must be positive")
	}
	return ev, nil
}
