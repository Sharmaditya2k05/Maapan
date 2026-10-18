// Package payment is lenden's core: payment requests (UPI QR / links),
// captured payments, manual receipts, refunds, webhooks and reconciliation.
package payment

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/taraju/mool/httpx"
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
	Status        string    `json:"status"` // created | paid | expired | cancelled | failed
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// Payment is money received.
type Payment struct {
	ID            string    `json:"id"`
	StoreID       *string   `json:"store_id,omitempty"`
	RequestID     *string   `json:"request_id,omitempty"`
	InvoiceID     *string   `json:"invoice_id,omitempty"`
	PartyID       *string   `json:"party_id,omitempty"`
	PartyPhone    *string   `json:"party_phone,omitempty"`
	AmountPaise   int64     `json:"amount_paise"`
	RefundedPaise int64     `json:"refunded_paise"`
	Method        string    `json:"method"`
	Gateway       string    `json:"gateway"`
	GatewayRef    *string   `json:"gateway_ref,omitempty"`
	Status        string    `json:"status"`
	Notes         *string   `json:"notes,omitempty"`
	CapturedAt    time.Time `json:"captured_at"`
	CreatedBy     string    `json:"created_by"`
}

// ManualInput records a cash/cheque/bank receipt (POST /v1/payments/manual).
type ManualInput struct {
	InvoiceID   string `json:"invoice_id,omitempty"`
	PartyID     string `json:"party_id,omitempty"`
	PartyPhone  string `json:"party_phone,omitempty"`
	StoreID     string `json:"store_id,omitempty"`
	AmountPaise int64  `json:"amount_paise"`
	Method      string `json:"method"`              // cash | cheque | bank | upi | card
	Reference   string `json:"reference,omitempty"` // cheque no / UTR
	Notes       string `json:"notes,omitempty"`
	ReceivedAt  string `json:"received_at,omitempty"` // RFC3339 or YYYY-MM-DD; default now
}

var manualMethods = map[string]bool{"cash": true, "cheque": true, "bank": true, "upi": true, "card": true}

// Validate checks the receipt.
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
	RefundID    string // our refunds.id from refund notes, when present
	PaymentRef  string
	AmountPaise int64
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
