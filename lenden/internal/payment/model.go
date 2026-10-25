// Package payment is lenden's core: payment requests (UPI QR / links),
// captured payments, manual receipts, refunds, webhooks, settlements,
// per-tenant gateway keys and reconciliation.
package payment

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/mool/httpx"
)

// RequestInput is the body of POST /v1/payment-requests and /internal/v1/payment-requests.
type RequestInput struct {
	InvoiceID    string `json:"invoice_id,omitempty"`
	PartyID      string `json:"party_id,omitempty"`
	PartyPhone   string `json:"party_phone,omitempty"`
	AmountPaise  int64  `json:"amount_paise"`
	Purpose      string `json:"purpose"`                  // invoice | on_account
	Kind         string `json:"kind"`                     // upi_qr | payment_link
	ExpiresInSec int    `json:"expires_in_sec,omitempty"` // default 15 min (QR) / 7 days (link)
	StoreID      string `json:"store_id,omitempty"`
	Gateway      string `json:"gateway,omitempty"` // razorpay | cashfree | mock; default PAYMENT_GATEWAY
}

// Validate checks the request shape.
func (in *RequestInput) Validate() error {
	if in.AmountPaise <= 0 || in.AmountPaise > 100_00_000_00 { // ₹1 crore cap per request
		return httpx.Validation("amount_paise must be between 1 and 10000000000")
	}
	switch in.Purpose {
	case "invoice":
		if !isUUID(in.InvoiceID) {
			return httpx.Validation("purpose=invoice needs a valid invoice_id")
		}
	case "on_account":
		if in.PartyID == "" && in.PartyPhone == "" {
			return httpx.Validation("purpose=on_account needs party_id or party_phone")
		}
	default:
		return httpx.Validation("purpose must be invoice or on_account")
	}
	if in.Kind != "upi_qr" && in.Kind != "payment_link" {
		return httpx.Validation("kind must be upi_qr or payment_link")
	}
	if in.Kind == "payment_link" && in.PartyPhone == "" {
		return httpx.Validation("payment_link needs party_phone")
	}
	if in.PartyPhone != "" && !validPhone(in.PartyPhone) {
		return httpx.Validation("party_phone must be +91XXXXXXXXXX")
	}
	for name, id := range map[string]string{"party_id": in.PartyID, "store_id": in.StoreID} {
		if id != "" && !isUUID(id) {
			return httpx.Validation("%s must be a UUID", name)
		}
	}
	if in.ExpiresInSec < 0 || in.ExpiresInSec > 30*24*3600 {
		return httpx.Validation("expires_in_sec must be between 0 and 30 days")
	}
	return nil
}

// Expiry returns the request expiry, applying defaults and Razorpay's 2-minute minimum.
func (in *RequestInput) Expiry(now time.Time) time.Time {
	d := time.Duration(in.ExpiresInSec) * time.Second
	if d == 0 {
		d = 15 * time.Minute
		if in.Kind == "payment_link" {
			d = 7 * 24 * time.Hour
		}
	}
	if d < 3*time.Minute {
		d = 3 * time.Minute
	}
	return now.Add(d).UTC().Truncate(time.Second)
}

// Request is a stored payment request.
type Request struct {
	ID            string    `json:"id"`
	StoreID       *string   `json:"store_id,omitempty"`
	InvoiceID     *string   `json:"invoice_id,omitempty"`
	PartyID       *string   `json:"party_id,omitempty"`
	PartyPhone    *string   `json:"party_phone,omitempty"`
	AmountPaise   int64     `json:"amount_paise"`
	PaidPaise     int64     `json:"paid_paise"`
	Purpose       string    `json:"purpose"`
	Kind          string    `json:"kind"`
	Gateway       string    `json:"gateway"`
	GatewayRef    *string   `json:"gateway_ref,omitempty"`
	UPIQRImageURL *string   `json:"upi_qr_image_url,omitempty"`
	UPIIntent     *string   `json:"upi_intent,omitempty"`
	ShortURL      *string   `json:"short_url,omitempty"`
	Status        string    `json:"status"` // created | partial | paid | expired | cancelled | failed
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// Payment is money received.
type Payment struct {
	ID             string    `json:"id"`
	StoreID        *string   `json:"store_id,omitempty"`
	RequestID      *string   `json:"request_id,omitempty"`
	InvoiceID      *string   `json:"invoice_id,omitempty"`
	PartyID        *string   `json:"party_id,omitempty"`
	PartyPhone     *string   `json:"party_phone,omitempty"`
	AmountPaise    int64     `json:"amount_paise"`
	RefundedPaise  int64     `json:"refunded_paise"`
	ShortPaise     int64     `json:"short_paise"`  // requested − captured, when the capture fell short of the request
	ExcessPaise    int64     `json:"excess_paise"` // captured − requested, when the customer paid more than asked
	Method         string    `json:"method"`
	Gateway        string    `json:"gateway"`
	GatewayRef     *string   `json:"gateway_ref,omitempty"`
	Status         string    `json:"status"`
	Notes          *string   `json:"notes,omitempty"`
	IdempotencyKey *string   `json:"idempotency_key,omitempty"`
	CapturedAt     time.Time `json:"captured_at"`
	CreatedBy      string    `json:"created_by"`
}

// MaxIdempotencyKey is the longest idempotency key accepted on manual receipts.
const MaxIdempotencyKey = 64

// ManualInput records a cash/cheque/bank receipt (POST /v1/payments/manual).
// IdempotencyKey (body field or Idempotency-Key header) is required: a replay
// returns the receipt recorded the first time instead of booking it twice.
type ManualInput struct {
	InvoiceID      string `json:"invoice_id,omitempty"`
	PartyID        string `json:"party_id,omitempty"`
	PartyPhone     string `json:"party_phone,omitempty"`
	StoreID        string `json:"store_id,omitempty"`
	AmountPaise    int64  `json:"amount_paise"`
	Method         string `json:"method"`              // cash | cheque | bank | upi | card
	Reference      string `json:"reference,omitempty"` // cheque no / UTR
	Notes          string `json:"notes,omitempty"`
	ReceivedAt     string `json:"received_at,omitempty"` // RFC3339 or YYYY-MM-DD; default now
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

var manualMethods = map[string]bool{"cash": true, "cheque": true, "bank": true, "upi": true, "card": true}

// Validate checks the receipt. The idempotency key may still arrive through the
// header, so only its length is checked here; Service.Manual requires it.
func (in *ManualInput) Validate() error {
	if in.AmountPaise <= 0 {
		return httpx.Validation("amount_paise must be positive")
	}
	if !manualMethods[in.Method] {
		return httpx.Validation("method must be cash, cheque, bank, upi or card")
	}
	if in.InvoiceID == "" && in.PartyID == "" {
		return httpx.Validation("a receipt needs invoice_id or party_id")
	}
	for name, id := range map[string]string{"invoice_id": in.InvoiceID, "party_id": in.PartyID, "store_id": in.StoreID} {
		if id != "" && !isUUID(id) {
			return httpx.Validation("%s must be a UUID", name)
		}
	}
	if in.Method == "cheque" && strings.TrimSpace(in.Reference) == "" {
		return httpx.Validation("cheque receipts need the cheque number in reference")
	}
	if len(strings.TrimSpace(in.IdempotencyKey)) > MaxIdempotencyKey {
		return httpx.Validation("idempotency_key must be at most %d characters", MaxIdempotencyKey)
	}
	if _, err := in.receivedAt(time.Now()); err != nil {
		return err
	}
	return nil
}

func (in *ManualInput) receivedAt(now time.Time) (time.Time, error) {
	if in.ReceivedAt == "" {
		return now.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, in.ReceivedAt); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, in.ReceivedAt, ist); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, httpx.Validation("received_at must be RFC3339 or YYYY-MM-DD")
}

// RefundInput is the body of POST /v1/payments/{id}/refund.
type RefundInput struct {
	AmountPaise int64  `json:"amount_paise,omitempty"` // 0 = everything not yet refunded
	Reason      string `json:"reason"`
}

// Validate checks the refund.
func (in *RefundInput) Validate() error {
	if in.AmountPaise < 0 {
		return httpx.Validation("amount_paise cannot be negative")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return httpx.Validation("reason is required")
	}
	return nil
}

// RefundView is one refund as returned by the API.
type RefundView struct {
	ID          string     `json:"id"`
	PaymentID   string     `json:"payment_id"`
	AmountPaise int64      `json:"amount_paise"`
	GatewayRef  *string    `json:"gateway_ref,omitempty"`
	Status      string     `json:"status"` // pending | unknown | processed | failed
	Source      string     `json:"source"` // maapan | gateway
	Reason      string     `json:"reason"`
	LastError   *string    `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
}

// RefundResponse is the answer to POST /v1/payments/{id}/refund: the payment as
// it stands plus the refund just made. Status unknown means the gateway did not
// answer; the webhook or reconciliation settles it and the amount stays reserved.
type RefundResponse struct {
	*Payment
	Refund RefundView `json:"refund"`
}

// Capture is a provider payment to record against a request.
type Capture struct {
	RequestID   string
	Gateway     string
	PaymentRef  string
	AmountPaise int64
	Method      string
	CapturedAt  time.Time
}

// RefundDone is a provider-confirmed refund.
type RefundDone struct {
	Gateway     string
	RefundRef   string
	RefundID    string // our refunds.id from refund notes/receipt, when present
	PaymentRef  string
	AmountPaise int64
}

// SettlementDone is a provider payout reported by a webhook. TenantID is empty
// when the body named no tenant and the platform-wide secret verified it.
type SettlementDone struct {
	Gateway  string
	TenantID string
	gateway.Settlement
}

// Settlement is a stored gateway payout.
type Settlement struct {
	ID           string    `json:"id"`
	Gateway      string    `json:"gateway"`
	GatewayRef   string    `json:"gateway_ref"`
	UTR          *string   `json:"utr,omitempty"`
	GrossPaise   int64     `json:"gross_paise"`
	FeePaise     int64     `json:"fee_paise"`
	FeeTaxPaise  int64     `json:"fee_tax_paise"`
	RefundsPaise int64     `json:"refunds_paise"`
	NetPaise     int64     `json:"net_paise"`
	SettledAt    time.Time `json:"settled_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// SettlementFilter narrows GET /v1/settlements.
type SettlementFilter struct {
	Gateway, From, To string
	httpx.Page
}

// GatewayCredentialInput is the body of PUT /v1/settings/gateways/{gateway}.
type GatewayCredentialInput struct {
	KeyID         string `json:"key_id"`
	KeySecret     string `json:"key_secret"`
	WebhookSecret string `json:"webhook_secret,omitempty"` // Razorpay: required; Cashfree signs with key_secret
	Env           string `json:"env,omitempty"`            // razorpay: test | live; cashfree: sandbox | production
}

// Validate checks the shape; gateway-specific rules live in Service.PutGatewayCredentials.
func (in *GatewayCredentialInput) Validate() error {
	in.KeyID, in.KeySecret, in.WebhookSecret = strings.TrimSpace(in.KeyID), strings.TrimSpace(in.KeySecret), strings.TrimSpace(in.WebhookSecret)
	in.Env = strings.ToLower(strings.TrimSpace(in.Env))
	if in.KeyID == "" || in.KeySecret == "" {
		return httpx.Validation("key_id and key_secret are required")
	}
	for name, v := range map[string]string{"key_id": in.KeyID, "key_secret": in.KeySecret, "webhook_secret": in.WebhookSecret} {
		if len(v) > 256 {
			return httpx.Validation("%s is too long", name)
		}
	}
	return nil
}

// Filter narrows GET /v1/payments.
type Filter struct {
	StoreID, InvoiceID, PartyID, Method, Gateway, Status, From, To string
	httpx.Page
}

var ist = time.FixedZone("IST", 5*3600+1800)

func isUUID(s string) bool { _, err := uuid.Parse(s); return err == nil && len(s) == 36 }

func validPhone(p string) bool {
	if len(p) != 13 || !strings.HasPrefix(p, "+91") {
		return false
	}
	for _, c := range p[3:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// refundStatus derives the payment status after refunds.
func refundStatus(amount, refunded int64) string {
	switch {
	case refunded <= 0:
		return "captured"
	case refunded >= amount:
		return "refunded"
	}
	return "partially_refunded"
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
