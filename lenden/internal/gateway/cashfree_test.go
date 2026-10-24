package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func cfHeader(raw []byte, ts, secret string) http.Header {
	h := http.Header{}
	h.Set("x-webhook-timestamp", ts)
	h.Set("x-webhook-signature", SignCashfree(raw, ts, secret))
	return h
}

func TestCashfreeWebhookSignature(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	w := CashfreeWebhooks{Secret: "cfsecret", MaxSkew: 10 * time.Minute, Now: func() time.Time { return now }}
	raw := []byte(`{"type":"PAYMENT_SUCCESS_WEBHOOK"}`)
	ts := strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10)

	if !w.Verify(raw, cfHeader(raw, ts, "cfsecret")) {
		t.Fatal("valid signature rejected")
	}
	if w.Verify(raw, cfHeader(raw, ts, "other")) {
		t.Fatal("wrong secret accepted")
	}
	if w.Verify([]byte(`{"type":"X"}`), cfHeader(raw, ts, "cfsecret")) {
		t.Fatal("tampered body accepted")
	}
	old := strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)
	if w.Verify(raw, cfHeader(raw, old, "cfsecret")) {
		t.Fatal("stale timestamp accepted")
	}
	if w.Verify(raw, http.Header{}) {
		t.Fatal("missing headers accepted")
	}
}

func TestCashfreeParsePaymentSuccess(t *testing.T) {
	raw := `{"type":"PAYMENT_SUCCESS_WEBHOOK","event_time":"2026-10-17T15:30:00+05:30","data":{
		"order":{"order_id":"4f1c2b7e-0000-4000-8000-000000000001","order_amount":1180.5,"order_tags":{"request_id":"4f1c2b7e-0000-4000-8000-000000000001","tenant_id":"t1"}},
		"payment":{"cf_payment_id":5114910293645,"payment_status":"SUCCESS","payment_amount":1180.5,"payment_group":"upi","payment_time":"2026-10-17T15:29:58+05:30"}}}`
	ev, err := CashfreeWebhooks{}.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindCapture || ev.AmountPaise != 118050 || ev.Method != "upi" ||
		ev.RequestID != "4f1c2b7e-0000-4000-8000-000000000001" ||
		ev.PaymentRef != "4f1c2b7e-0000-4000-8000-000000000001/5114910293645" {
		t.Fatalf("unexpected %+v", ev)
	}
	if ev.CapturedAt.Format(time.RFC3339) != "2026-10-17T09:59:58Z" {
		t.Fatalf("time %v", ev.CapturedAt)
	}
}

func TestCashfreeParseLinkAndRefund(t *testing.T) {
	link := `{"type":"PAYMENT_LINK_EVENT","event_time":"2026-10-17T15:30:00+05:30","data":{"link_id":"req-1","link_status":"PAID",
		"link_amount_paid":250,"link_notes":{"request_id":"req-1"},"order":{"order_id":"CFPay_req-1_abc","transaction_id":998877,"transaction_status":"SUCCESS"}}}`
	ev, err := CashfreeWebhooks{}.Parse([]byte(link))
	if err != nil || ev.Kind != KindCapture || ev.RequestID != "req-1" || ev.AmountPaise != 25000 || ev.PaymentRef != "CFPay_req-1_abc/998877" {
		t.Fatalf("link: %+v %v", ev, err)
	}

	refund := `{"type":"REFUND_STATUS_WEBHOOK","data":{"refund":{"cf_refund_id":777,"cf_payment_id":998877,"refund_id":"rf-uuid",
		"order_id":"CFPay_req-1_abc","refund_amount":100.25,"refund_status":"SUCCESS"}}}`
	ev, err = CashfreeWebhooks{}.Parse([]byte(refund))
	if err != nil || ev.Kind != KindRefund || ev.RefundRef != "777" || ev.RefundNote != "rf-uuid" ||
		ev.AmountPaise != 10025 || ev.PaymentRef != "CFPay_req-1_abc/998877" {
		t.Fatalf("refund: %+v %v", ev, err)
	}

	for _, ignored := range []string{
		`{"type":"PAYMENT_FAILED_WEBHOOK","data":{}}`,
		`{"type":"PAYMENT_SUCCESS_WEBHOOK","data":{"order":{"order_id":"o"},"payment":{"cf_payment_id":1,"payment_status":"PENDING","payment_amount":1}}}`,
		`{"type":"REFUND_STATUS_WEBHOOK","data":{"refund":{"refund_status":"PENDING","refund_amount":1}}}`,
	} {
		if ev, err := (CashfreeWebhooks{}).Parse([]byte(ignored)); err != nil || ev.Kind != KindIgnore {
			t.Fatalf("expected ignore for %s: %+v %v", ignored, ev, err)
		}
	}
	if _, err := (CashfreeWebhooks{}).Parse([]byte(`{"data":{}}`)); err == nil {
		t.Fatal("missing type must fail")
	}
}

// fakeCashfree records requests and answers like the PG API.
func fakeCashfree(t *testing.T) (*Cashfree, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-client-id") != "cid" || r.Header.Get("x-client-secret") != "csec" || r.Header.Get("x-api-version") == "" {
			http.Error(w, `{"message":"auth","code":"request_failed"}`, 401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/orders":
			_, _ = w.Write([]byte(`{"order_id":"req-1","order_status":"ACTIVE","payment_session_id":"sess_1"}`))
		case r.Method == "POST" && r.URL.Path == "/orders/sessions":
			if strings.Contains(string(body), `"qrcode"`) {
				_, _ = w.Write([]byte(`{"data":{"payload":{"qrcode":"data:image/png;base64,AAAA"}}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"payload":{"default":"upi://pay?pa=x@cf&am=10.00"}}}`))
			}
		case r.Method == "GET" && r.URL.Path == "/orders/req-1":
			_, _ = w.Write([]byte(`{"order_id":"req-1","order_status":"PAID"}`))
		case r.Method == "GET" && r.URL.Path == "/orders/req-1/payments":
			_, _ = w.Write([]byte(`[{"cf_payment_id":42,"order_id":"req-1","payment_status":"SUCCESS","payment_amount":10,"payment_group":"credit_card","payment_time":"2026-10-17T10:00:00+05:30"}]`))
		case r.Method == "GET" && r.URL.Path == "/orders/req-1/refunds":
			_, _ = w.Write([]byte(`[{"cf_refund_id":9001,"cf_payment_id":42,"refund_id":"rf-1","refund_amount":5,"refund_status":"PENDING"}]`))
		case r.Method == "POST" && r.URL.Path == "/orders/req-1/refunds":
			_, _ = w.Write([]byte(`{"cf_refund_id":"9001","refund_status":"PENDING"}`))
		case r.Method == "GET" && r.URL.Path == "/links/lnk-1":
			_, _ = w.Write([]byte(`{"link_id":"lnk-1","link_url":"https://cf/lnk-1","link_status":"PAID","link_amount_paid":250}`))
		case r.Method == "GET" && r.URL.Path == "/links/lnk-1/orders":
			_, _ = w.Write([]byte(`[{"order_id":"CFPay_lnk-1_abc","order_status":"PAID"}]`))
		case r.Method == "GET" && r.URL.Path == "/orders/CFPay_lnk-1_abc/payments":
			_, _ = w.Write([]byte(`[{"cf_payment_id":998877,"order_id":"CFPay_lnk-1_abc","payment_status":"SUCCESS","payment_amount":250,"payment_group":"upi","payment_time":"2026-10-17T10:00:00+05:30"}]`))
		case r.Method == "GET" && r.URL.Path == "/orders/CFPay_lnk-1_abc/refunds":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, `{"message":"not found","code":"not_found"}`, 404)
		}
	}))
	t.Cleanup(srv.Close)
	cf := NewCashfree("cid", "csec", "sandbox", "", "")
	cf.BaseURL = srv.URL
	return cf, &calls
}

func TestCashfreeCreateQRFetchRefund(t *testing.T) {
	cf, calls := fakeCashfree(t)
	ctx := context.Background()

	if _, err := cf.CreateQR(ctx, QRRequest{RequestID: "req-1", AmountPaise: 1000}); err == nil {
		t.Fatal("QR without any phone must fail")
	}
	c, err := cf.CreateQR(ctx, QRRequest{RequestID: "req-1", TenantID: "t1", AmountPaise: 1000, Phone: "+919876543210", Description: "Bill"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GatewayRef != "req-1" || c.ImageURL != "data:image/png;base64,AAAA" || !strings.HasPrefix(c.UPIIntent, "upi://") {
		t.Fatalf("created %+v", c)
	}
	var order map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN((*calls)[0], " ", 3)[2]), &order)
	if order["order_amount"] != 10.0 || order["customer_details"].(map[string]any)["customer_phone"] != "9876543210" {
		t.Fatalf("order body %v", order)
	}

	st, err := cf.FetchQR(ctx, "req-1")
	if err != nil || st.Closed || len(st.Payments) != 1 {
		t.Fatalf("fetch %+v %v", st, err)
	}
	p := st.Payments[0]
	if p.PaymentRef != "req-1/42" || p.AmountPaise != 1000 || p.Status != "captured" || p.Method != "card" {
		t.Fatalf("payment %+v", p)
	}

	rf, err := cf.Refund(ctx, p.PaymentRef, 500, "rf-1")
	if err != nil || rf.RefundRef != "9001" || rf.Status != "pending" {
		t.Fatalf("refund %+v %v", rf, err)
	}
	if !strings.Contains((*calls)[len(*calls)-1], `"refund_amount":5`) {
		t.Fatalf("refund body %s", (*calls)[len(*calls)-1])
	}
	if _, err := cf.Refund(ctx, "no-slash", 500, "rf-2"); err == nil {
		t.Fatal("bad payment ref must fail")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	reg.Add(NewMock(""), nil)
	reg.Add(NewCashfree("a", "b", "sandbox", "", ""), CashfreeWebhooks{Secret: "b"})
	if err := reg.SetDefault("razorpay"); err == nil {
		t.Fatal("unconfigured default must fail")
	}
	if err := reg.SetDefault("cashfree"); err != nil || reg.Default().Name() != "cashfree" {
		t.Fatal("default")
	}
	if strings.Join(reg.Names(), ",") != "cashfree,mock" || len(reg.Webhooks()) != 1 {
		t.Fatalf("names %v hooks %d", reg.Names(), len(reg.Webhooks()))
	}
	if _, ok := reg.Mock(); !ok {
		t.Fatal("mock lookup")
	}
}
