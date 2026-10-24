package payment

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/taraju/lenden/internal/gateway"
	"github.com/taraju/mool/httpx"
)

// memSecrets is an in-memory gateway.WebhookSecretSource.
type memSecrets map[string]string // tenant → secret

func (m memSecrets) WebhookSecret(_ context.Context, tenantID, _ string) (string, bool, error) {
	s, ok := m[tenantID]
	return s, ok, nil
}

func (m memSecrets) AllWebhookSecrets(_ context.Context, _ string) ([]gateway.TenantSecret, error) {
	var out []gateway.TenantSecret
	for t, s := range m {
		out = append(out, gateway.TenantSecret{TenantID: t, Secret: s})
	}
	return out, nil
}

// Item 9: a webhook is verified with the secret of the tenant its body refers
// to, then with the platform secret; nothing else gets through.
func TestWebhookTenantSecretResolution(t *testing.T) {
	led := newFake(reqID)
	led.tenants[reqID] = "tenant-a"
	wh := &Webhooks{Source: gateway.RazorpayWebhooks{Secret: "global"}, Ledger: led,
		Secrets: memSecrets{"tenant-a": "secret-a", "tenant-b": "secret-b"}}
	ctx := context.Background()
	body := qrCredited("pay_t1")

	out, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "secret-a"), "evt_a1"))
	if err != nil || out.Result != Applied {
		t.Fatalf("tenant secret must verify: %+v %v", out, err)
	}
	// Another tenant's secret does not verify a body that belongs to tenant-a.
	if _, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "secret-b"), "evt_a2")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("other tenant's secret must be rejected: %v", err)
	}
	// The platform secret is still the fallback.
	out, err = wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "global"), "evt_a3"))
	if err != nil || out.Result != Duplicate {
		t.Fatalf("platform secret fallback: %+v %v", out, err)
	}
	// Without any platform secret only tenant keys work.
	wh.Source = gateway.RazorpayWebhooks{}
	if _, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "global"), "evt_a4")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("no platform secret: %v", err)
	}
	if _, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "secret-a"), "evt_a5")); err != nil {
		t.Fatalf("tenant secret without platform secret: %v", err)
	}
	// A malformed body with a valid signature is 400, an unsigned one 401.
	junk := []byte(`{`)
	if _, err := wh.Process(ctx, junk, rzpHeader(gateway.Sign(junk, "secret-a"), "evt_j")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("malformed body cannot name a tenant and no platform secret: want 401, got %v", err)
	}
	wh.Source = gateway.RazorpayWebhooks{Secret: "global"}
	if _, err := wh.Process(ctx, junk, rzpHeader(gateway.Sign(junk, "global"), "evt_j2")); httpx.AsError(err).Status != http.StatusBadRequest {
		t.Fatalf("malformed but signed: want 400, got %v", err)
	}
}

// Item 8: settlement webhooks name no payment, so every tenant's secret is tried.
func TestSettlementWebhookDispatch(t *testing.T) {
	led := newFake()
	wh := &Webhooks{Source: gateway.RazorpayWebhooks{Secret: "global"}, Ledger: led,
		Secrets: memSecrets{"tenant-a": "secret-a"}}
	ctx := context.Background()
	body := []byte(`{"event":"settlement.processed","created_at":1760000100,"payload":{"settlement":{"entity":{
		"id":"setl_1","amount":97640,"status":"processed","fees":2000,"tax":360,"utr":"UTR1","created_at":1760000000}}}}`)

	out, err := wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "secret-a"), "evt_s1"))
	if err != nil || out.Result != Applied || len(led.settlements) != 1 {
		t.Fatalf("settlement: %+v %v (%d)", out, err, len(led.settlements))
	}
	s := led.settlements[0]
	if s.TenantID != "tenant-a" || s.Gateway != "razorpay" || s.GatewayRef != "setl_1" || s.NetPaise != 97640 || s.FeePaise != 2000 || s.FeeTaxPaise != 360 || s.GrossPaise != 100000 {
		t.Fatalf("settlement done %+v", s)
	}
	out, _ = wh.Process(ctx, body, rzpHeader(gateway.Sign(body, "secret-a"), "evt_s1"))
	if out.Result != Duplicate {
		t.Fatalf("redelivery: %s", out.Result)
	}
	// Platform-signed settlement with no tenant reference: stored without a tenant.
	body2 := []byte(`{"event":"settlement.processed","payload":{"settlement":{"entity":{"id":"setl_2","amount":100,"status":"processed","fees":0,"tax":0,"created_at":1760000000}}}}`)
	out, err = wh.Process(ctx, body2, rzpHeader(gateway.Sign(body2, "global"), "evt_s2"))
	if err != nil || out.Result != Applied || led.settlements[1].TenantID != "" {
		t.Fatalf("platform settlement: %+v %v %+v", out, err, led.settlements)
	}
	if _, err := wh.Process(ctx, body2, rzpHeader(gateway.Sign(body2, "wrong"), "evt_s3")); httpx.AsError(err).Status != http.StatusUnauthorized {
		t.Fatalf("unknown secret: %v", err)
	}
}

func TestManualIdempotencyKeyValidation(t *testing.T) {
	ok := &ManualInput{InvoiceID: reqID, AmountPaise: 500, Method: "cash", IdempotencyKey: strings.Repeat("k", MaxIdempotencyKey)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	long := &ManualInput{InvoiceID: reqID, AmountPaise: 500, Method: "cash", IdempotencyKey: strings.Repeat("k", MaxIdempotencyKey+1)}
	if err := long.Validate(); err == nil {
		t.Fatal("65-char key must fail")
	}
	// Presence is enforced by Service.Manual (the header may still supply it).
	none := &ManualInput{InvoiceID: reqID, AmountPaise: 500, Method: "cash"}
	if err := none.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayCredentialInputValidation(t *testing.T) {
	in := &GatewayCredentialInput{KeyID: " rzp_test_x ", KeySecret: "s", Env: " TEST "}
	if err := in.Validate(); err != nil || in.KeyID != "rzp_test_x" || in.Env != "test" {
		t.Fatalf("trim: %+v %v", in, err)
	}
	if err := (&GatewayCredentialInput{KeyID: "x"}).Validate(); err == nil {
		t.Fatal("missing secret must fail")
	}
	if err := (&GatewayCredentialInput{KeyID: strings.Repeat("x", 257), KeySecret: "s"}).Validate(); err == nil {
		t.Fatal("too long must fail")
	}
}
