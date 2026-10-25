package payment

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/mool/httpx"
)

// fakeLedger mimics PGLedger's idempotency rules in memory.
type fakeLedger struct {
	webhooks    map[string]bool
	payments    map[string]Capture // gateway_ref → capture
	requests    map[string]bool    // known request ids
	tenants     map[string]string  // request id → tenant
	refunds     int
	settlements []SettlementDone
	fail        bool
}

func newFake(reqs ...string) *fakeLedger {
	f := &fakeLedger{webhooks: map[string]bool{}, payments: map[string]Capture{}, requests: map[string]bool{}, tenants: map[string]string{}}
	for _, r := range reqs {
		f.requests[r] = true
	}
	return f
}

func (f *fakeLedger) seen(id string) bool {
	if id == "" {
		return false
	}
	if f.webhooks[id] {
		return true
	}
	f.webhooks[id] = true
	return false
}

func (f *fakeLedger) Capture(_ context.Context, eventID string, _ []byte, c Capture) (Result, error) {
	if f.fail {
		return "", errors.New("db down")
	}
	if f.seen(eventID) {
		return Duplicate, nil
	}
	if !f.requests[c.RequestID] {
		return UnknownRequest, nil
	}
	if _, ok := f.payments[c.PaymentRef]; ok {
		return Duplicate, nil
	}
	f.payments[c.PaymentRef] = c
	return Applied, nil
}

func (f *fakeLedger) RefundProcessed(_ context.Context, eventID string, _ []byte, _ RefundDone) (Result, error) {
	if f.seen(eventID) {
		return Duplicate, nil
	}
	f.refunds++
	return Applied, nil
}

func (f *fakeLedger) Ignore(_ context.Context, eventID, _, _ string, _ []byte) (Result, error) {
	if f.seen(eventID) {
		return Duplicate, nil
	}
	return Applied, nil
}

func (f *fakeLedger) SettlementProcessed(_ context.Context, eventID string, _ []byte, s SettlementDone) (Result, error) {
	if f.seen(eventID) {
		return Duplicate, nil
	}
	for _, have := range f.settlements {
		if have.GatewayRef == s.GatewayRef {
			return Duplicate, nil
		}
	}
	f.settlements = append(f.settlements, s)
	return Applied, nil
}

// TenantFor mirrors PGLedger: a known request id resolves to its tenant.
func (f *fakeLedger) TenantFor(_ context.Context, _ string, ev gateway.WebhookEvent) (string, error) {
	if f.requests[ev.RequestID] {
		return f.tenants[ev.RequestID], nil
	}
	return "", nil
}

const reqID = "11111111-2222-3333-4444-555555555555"

func qrCredited(payRef string) []byte {
	return []byte(`{"event":"qr_code.credited","payload":{
		"payment":{"entity":{"id":"` + payRef + `","amount":11800,"method":"upi","status":"captured","created_at":1760000000}},
		"qr_code":{"entity":{"id":"qr_1","notes":{"request_id":"` + reqID + `"}}}}}`)
}

func paymentCaptured(payRef string) []byte {
	return []byte(`{"event":"payment.captured","payload":{
		"payment":{"entity":{"id":"` + payRef + `","amount":11800,"method":"upi","status":"captured","notes":{"request_id":"` + reqID + `"}}}}}`)
}

func TestWebhookIdempotency(t *testing.T) {
	led := newFake(reqID)
	wh := &Webhooks{Source: gateway.RazorpayWebhooks{Secret: "whsec"}, Ledger: led}
	ctx := context.Background()

	body := qrCredited("pay_1")
	out, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_1"))
	if err != nil || out.Result != Applied {
		t.Fatalf("first delivery: %+v %v", out, err)
	}
	// Razorpay retries the same event.
	out, _ = wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_1"))
	if out.Result != Duplicate {
		t.Fatalf("redelivery must be duplicate, got %s", out.Result)
	}
	// The sibling event for the same payment (different event id).
	body2 := paymentCaptured("pay_1")
	out, _ = wh.Process(ctx, body2, rzpHeader(gateway.Sign(body2, "whsec"), "evt_2"))
	if out.Result != Duplicate || len(led.payments) != 1 {
		t.Fatalf("same payment twice must record once: %s, %d payments", out.Result, len(led.payments))
	}
	if got := led.payments["pay_1"]; got.AmountPaise != 11800 || got.RequestID != reqID || got.Gateway != "razorpay" {
		t.Fatalf("capture mapping %+v", got)
	}
}

func TestWebhookRejectsAndRetries(t *testing.T) {
	led := newFake(reqID)
	wh := &Webhooks{Source: gateway.RazorpayWebhooks{Secret: "whsec"}, Ledger: led}
	ctx := context.Background()
	body := qrCredited("pay_2")

	if _, err := wh.Process(ctx, body, rzpHeader("bad", "evt_x")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("bad signature must be 401, got %v", err)
	}
	junk := []byte(`not json`)
	if _, err := wh.Process(ctx, junk, rzpHeader(gateway.Sign(junk, "whsec"), "evt_y")); httpx.AsError(err).Status != http.StatusBadRequest {
		t.Fatalf("malformed must be 400, got %v", err)
	}
	led.fail = true
	if _, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), "evt_z")); err == nil || httpx.AsError(err).Status != 500 {
		t.Fatalf("db failure must be 5xx so Razorpay retries, got %v", err)
	}
	led.fail = false
	other := newFake() // request not ours
	wh.Ledger = other
	out, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "whsec"), ""))
	if err != nil || out.Result != UnknownRequest {
		t.Fatalf("unknown request must be acknowledged: %+v %v", out, err)
	}
}

func TestRequestValidation(t *testing.T) {
	ok := RequestInput{InvoiceID: reqID, AmountPaise: 100, Purpose: "invoice", Kind: "upi_qr"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []RequestInput{
		{InvoiceID: reqID, AmountPaise: 0, Purpose: "invoice", Kind: "upi_qr"},
		{AmountPaise: 100, Purpose: "invoice", Kind: "upi_qr"},
		{PartyID: reqID, AmountPaise: 100, Purpose: "on_account", Kind: "payment_link"}, // link needs phone
		{PartyPhone: "98765", AmountPaise: 100, Purpose: "on_account", Kind: "upi_qr"},
		{InvoiceID: reqID, AmountPaise: 100, Purpose: "invoice", Kind: "cash"},
	}
	for i, in := range bad {
		if err := in.Validate(); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	if got := ok.Expiry(now); got != now.Add(15*time.Minute) {
		t.Errorf("default QR expiry %v", got)
	}
	short := RequestInput{Kind: "upi_qr", ExpiresInSec: 30}
	if got := short.Expiry(now); got != now.Add(3*time.Minute) {
		t.Errorf("minimum expiry %v", got)
	}
}

func TestManualValidationAndRefundStatus(t *testing.T) {
	if err := (&ManualInput{PartyID: reqID, AmountPaise: 500, Method: "cheque"}).Validate(); err == nil {
		t.Error("cheque without number must fail")
	}
	if err := (&ManualInput{InvoiceID: reqID, AmountPaise: 500, Method: "cash", ReceivedAt: "2026-10-17"}).Validate(); err != nil {
		t.Error(err)
	}
	if refundStatus(1000, 0) != "captured" || refundStatus(1000, 400) != "partially_refunded" || refundStatus(1000, 1000) != "refunded" {
		t.Error("refund status")
	}
}

func rzpHeader(sig, eventID string) http.Header {
	h := http.Header{}
	h.Set("X-Razorpay-Signature", sig)
	if eventID != "" {
		h.Set("X-Razorpay-Event-Id", eventID)
	}
	return h
}
