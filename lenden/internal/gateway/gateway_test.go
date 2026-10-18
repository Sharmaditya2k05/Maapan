package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"event":"payment.captured"}`)
	sig := Sign(body, "whsec")
	if !VerifySignature(body, sig, "whsec") || !VerifySignature(body, strings.ToUpper(sig), "whsec") {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature(body, sig, "other") || VerifySignature([]byte(`{"event":"x"}`), sig, "whsec") {
		t.Fatal("bad signature accepted")
	}
	if VerifySignature(body, "", "whsec") || VerifySignature(body, sig, "") {
		t.Fatal("empty signature/secret must fail")
	}
}

func TestParseQRCredited(t *testing.T) {
	raw := `{"event":"qr_code.credited","payload":{
		"payment":{"entity":{"id":"pay_1","amount":11800,"method":"upi","status":"captured","created_at":1760000000,"notes":[]}},
		"qr_code":{"entity":{"id":"qr_9","notes":{"request_id":"req-1","tenant_id":"t1"}}}}}`
	ev, err := ParseWebhook([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindCapture || ev.PaymentRef != "pay_1" || ev.AmountPaise != 11800 ||
		ev.RequestID != "req-1" || ev.GatewayRef != "qr_9" || ev.Method != "upi" {
		t.Fatalf("unexpected %+v", ev)
	}
}

func TestParsePaymentLinkPaidUsesReferenceID(t *testing.T) {
	raw := `{"event":"payment_link.paid","payload":{
		"payment":{"entity":{"id":"pay_2","amount":50000,"method":"emi","status":"captured"}},
		"payment_link":{"entity":{"id":"plink_1","reference_id":"req-2","notes":[]}}}}`
	ev, err := ParseWebhook([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ev.RequestID != "req-2" || ev.Method != "card" || ev.AmountPaise != 50000 {
		t.Fatalf("unexpected %+v", ev)
	}
}

func TestParseRefundAndIgnored(t *testing.T) {
	ev, err := ParseWebhook([]byte(`{"event":"refund.processed","payload":{"refund":{"entity":{"id":"rfnd_1","payment_id":"pay_1","amount":500,"notes":{"refund_id":"r-1"}}}}}`))
	if err != nil || ev.Kind != KindRefund || ev.RefundRef != "rfnd_1" || ev.PaymentRef != "pay_1" || ev.AmountPaise != 500 || ev.RefundNote != "r-1" {
		t.Fatalf("refund: %+v %v", ev, err)
	}
	ev, err = ParseWebhook([]byte(`{"event":"order.paid","payload":{}}`))
	if err != nil || ev.Kind != KindIgnore {
		t.Fatalf("ignored: %+v %v", ev, err)
	}
	ev, err = ParseWebhook([]byte(`{"event":"payment.captured","payload":{"payment":{"entity":{"id":"pay_3","amount":100,"status":"authorized"}}}}`))
	if err != nil || ev.Kind != KindIgnore {
		t.Fatalf("authorized-only must wait: %+v %v", ev, err)
	}
	if _, err := ParseWebhook([]byte(`{"event":"payment.captured","payload":{}}`)); err == nil {
		t.Fatal("missing entity must error")
	}
}

func TestAmountMapping(t *testing.T) {
	cases := map[int64]string{0: "0.00", 5: "0.05", 11800: "118.00", 1234567: "12345.67", -150: "-1.50"}
	for in, want := range cases {
		if got := RupeeString(in); got != want {
			t.Errorf("RupeeString(%d)=%s want %s", in, got, want)
		}
	}
	intent := UPIIntent("shop@upi", "Sharma Store", 11850, "INV/26-27/00001", "req-1")
	if !strings.Contains(intent, "am=118.50") || !strings.HasPrefix(intent, "upi://pay?") {
		t.Fatalf("intent %s", intent)
	}
}

func TestRazorpayCreateQR(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "key" || p != "secret" || r.URL.Path != "/payments/qr_codes" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"qr_1","status":"active","image_url":"https://rzp.io/qr.png","image_content":"upi://pay?pa=x"}`))
	}))
	defer srv.Close()
	rz := NewRazorpay("key", "secret")
	rz.BaseURL = srv.URL
	c, err := rz.CreateQR(context.Background(), QRRequest{RequestID: "req-1", AmountPaise: 11800, Name: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GatewayRef != "qr_1" || c.UPIIntent != "upi://pay?pa=x" || c.ImageURL == "" {
		t.Fatalf("%+v", c)
	}
	if got["payment_amount"].(float64) != 11800 || got["usage"] != "single_use" {
		t.Fatalf("body %v", got)
	}
}
