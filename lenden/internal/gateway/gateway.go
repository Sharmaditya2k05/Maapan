// Package gateway abstracts the payment provider. The razorpay and cashfree
// drivers talk to the real APIs; the mock driver lets the POS flow run locally
// without keys. Registry holds every configured driver at once and resolves a
// tenant's own keys (per-tenant credentials) ahead of the platform keys.
package gateway

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// QRRequest asks for a single-use, fixed-amount dynamic UPI QR.
type QRRequest struct {
	RequestID   string // our payment_request id, echoed back in notes
	TenantID    string
	AmountPaise int64
	Name        string // shown in the payer's UPI app
	Phone       string // customer phone if known (+91XXXXXXXXXX); Cashfree requires one
	Description string
	CloseBy     time.Time
}

// LinkRequest asks for a payment link (sent by SMS/WhatsApp).
type LinkRequest struct {
	RequestID   string
	TenantID    string
	AmountPaise int64
	Description string
	Phone       string // +91XXXXXXXXXX
	ExpireBy    time.Time
}

// Created is what the provider returns for a QR or link.
type Created struct {
	GatewayRef string // qr_xxx / plink_xxx
	ImageURL   string // QR PNG (QR only)
	UPIIntent  string // upi://pay?... (QR only, when available)
	ShortURL   string // link only
}

// RemotePayment is one payment the provider has seen against a QR/link.
type RemotePayment struct {
	PaymentRef    string // pay_xxx
	AmountPaise   int64
	Method        string
	Status        string // captured | authorized | failed | refunded
	CapturedAt    time.Time
	RefundedPaise int64          // Σ refunds the provider has processed against it (0 when unknown)
	Refunds       []RemoteRefund // the refunds themselves, when the provider lists them cheaply
}

// RemoteRefund is one refund the provider knows about for a payment.
type RemoteRefund struct {
	RefundRef   string // rfnd_xxx / cf_refund_id
	Receipt     string // our refunds.id when we initiated it (Razorpay receipt/notes, Cashfree refund_id)
	AmountPaise int64
	Status      string // pending | processed | failed
}

// RemoteStatus is the provider's view of a QR/link (for reconciliation).
type RemoteStatus struct {
	Closed   bool // closed/expired/cancelled on the provider side
	Payments []RemotePayment
}

// Refund is the provider's answer to a refund call.
type Refund struct {
	RefundRef string // rfnd_xxx
	Status    string // pending | processed | failed
}

// Gateway is a payment provider.
type Gateway interface {
	Name() string
	CreateQR(ctx context.Context, r QRRequest) (Created, error)
	CreateLink(ctx context.Context, r LinkRequest) (Created, error)
	FetchQR(ctx context.Context, gatewayRef string) (RemoteStatus, error)
	FetchLink(ctx context.Context, gatewayRef string) (RemoteStatus, error)
	Refund(ctx context.Context, paymentRef string, amountPaise int64, receipt string) (Refund, error)
	// FetchRefunds lists every refund the provider has for a payment, so a
	// refund whose outcome we never learned (timeout) can be settled.
	FetchRefunds(ctx context.Context, paymentRef string) ([]RemoteRefund, error)
}

// Credentials are one tenant's own keys for a gateway (MATURITY-PLAN D5).
type Credentials struct {
	TenantID      string
	Gateway       string
	KeyID         string
	KeySecret     string
	WebhookSecret string // Razorpay webhook secret; Cashfree signs webhooks with KeySecret
	Env           string // razorpay: test | live; cashfree: sandbox | production
	UpdatedAt     time.Time
}

// CredentialSource looks up a tenant's own gateway keys. Implemented by creds.Store.
type CredentialSource interface {
	// Credentials returns the tenant's keys for gateway, ok=false when none are stored.
	Credentials(ctx context.Context, tenantID, gateway string) (Credentials, bool, error)
}

// Factory builds a driver from a tenant's credentials.
type Factory func(c Credentials) (Gateway, error)

// RupeeString formats paise as a decimal rupee amount ("1234.50") — the
// format UPI intents and dashboards expect.
func RupeeString(paise int64) string {
	sign := ""
	if paise < 0 {
		sign, paise = "-", -paise
	}
	return fmt.Sprintf("%s%d.%02d", sign, paise/100, paise%100)
}

// UPIIntent builds a upi://pay link for a VPA.
func UPIIntent(vpa, payeeName string, amountPaise int64, note, ref string) string {
	q := url.Values{}
	q.Set("pa", vpa)
	q.Set("pn", payeeName)
	q.Set("am", RupeeString(amountPaise))
	q.Set("cu", "INR")
	if note != "" {
		q.Set("tn", note)
	}
	if ref != "" {
		q.Set("tr", ref)
	}
	return "upi://pay?" + q.Encode()
}

// MapMethod normalises provider method names to the platform set
// (upi | card | netbanking | wallet).
func MapMethod(m string) string {
	switch m {
	case "upi", "card", "netbanking", "wallet":
		return m
	case "emi", "cardless_emi", "paylater", "pay_later",
		"credit_card", "debit_card", "credit_card_emi", "debit_card_emi", "prepaid_card", "corporate_credit_card":
		return "card"
	case "bank_transfer", "nach", "emandate", "net_banking":
		return "netbanking"
	case "upi_ppi", "upi_credit_card", "upi_ppi_offline", "upi_credit_card_offline":
		return "upi"
	case "":
		return "upi"
	}
	return m
}
