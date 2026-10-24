package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Item 7: Cashfree link payments must be keyed on cf_payment_id in webhooks
// exactly as FetchLink keys them, so reconcile after a webhook never double-books.
func TestCashfreeLinkPaymentRefMatchesFetchLink(t *testing.T) {
	cf, _ := fakeCashfree(t)
	st, err := cf.FetchLink(context.Background(), "lnk-1")
	if err != nil || len(st.Payments) != 1 {
		t.Fatalf("fetch link: %+v %v", st, err)
	}
	fetched := st.Payments[0].PaymentRef // CFPay_lnk-1_abc/998877

	both := `{"type":"PAYMENT_LINK_EVENT","event_time":"2026-10-17T15:30:00+05:30","data":{"link_id":"lnk-1","link_status":"PAID",
		"link_amount_paid":250,"link_notes":{"request_id":"lnk-1"},"order":{"order_id":"CFPay_lnk-1_abc","cf_payment_id":998877,"transaction_id":555,"transaction_status":"SUCCESS"}}}`
	ev, err := CashfreeWebhooks{}.Parse([]byte(both))
	if err != nil || ev.Kind != KindCapture {
		t.Fatalf("parse: %+v %v", ev, err)
	}
	if ev.PaymentRef != fetched {
		t.Fatalf("webhook keyed on %q but FetchLink on %q", ev.PaymentRef, fetched)
	}

	// Older payloads carry only transaction_id (the same payment id under another name).
	legacy := `{"type":"PAYMENT_LINK_EVENT","event_time":"2026-10-17T15:30:00+05:30","data":{"link_id":"lnk-1","link_status":"PAID",
		"link_amount_paid":250,"order":{"order_id":"CFPay_lnk-1_abc","transaction_id":998877,"transaction_status":"SUCCESS"}}}`
	ev, err = CashfreeWebhooks{}.Parse([]byte(legacy))
	if err != nil || ev.PaymentRef != fetched {
		t.Fatalf("legacy payload: %+v %v", ev, err)
	}
}

func TestCashfreeFetchRefundsAndErrorClass(t *testing.T) {
	cf, _ := fakeCashfree(t)
	ctx := context.Background()
	rf, err := cf.FetchRefunds(ctx, "req-1/42")
	if err != nil || len(rf) != 1 || rf[0].RefundRef != "9001" || rf[0].Receipt != "rf-1" || rf[0].AmountPaise != 500 || rf[0].Status != "pending" {
		t.Fatalf("refunds %+v %v", rf, err)
	}
	// FetchQR carries the order's refunds on the payment.
	st, err := cf.FetchQR(ctx, "req-1")
	if err != nil || len(st.Payments) != 1 || len(st.Payments[0].Refunds) != 1 {
		t.Fatalf("fetch qr %+v %v", st, err)
	}
	// A 404 from the API is a definitive rejection.
	_, err = cf.FetchRefunds(ctx, "nope/1")
	if !IsDefinitive(err) {
		t.Fatalf("404 must be definitive: %v", err)
	}
}

// Item 1: only a 4xx answer proves the refund did not happen.
func TestRazorpayErrorClassification(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"BAD_REQUEST_ERROR","description":"The payment has been fully refunded already"}}`))
	}))
	defer srv.Close()
	rz := NewRazorpay("k", "s")
	rz.BaseURL = srv.URL
	ctx := context.Background()

	status = 400
	_, err := rz.Refund(ctx, "pay_1", 100, "rf-1")
	var ge *Error
	if !errors.As(err, &ge) || !IsDefinitive(err) || ge.Code != "BAD_REQUEST_ERROR" || !strings.Contains(err.Error(), "fully refunded") {
		t.Fatalf("400 must be definitive with the gateway message: %v", err)
	}
	status = 502
	if _, err := rz.Refund(ctx, "pay_1", 100, "rf-1"); IsDefinitive(err) || err == nil {
		t.Fatalf("5xx must not be definitive: %v", err)
	}

	// Transport failure (connection refused): not definitive.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	rz.BaseURL = "http://" + addr
	rz.HTTP = &http.Client{Timeout: 2 * time.Second}
	_, err = rz.Refund(ctx, "pay_1", 100, "rf-1")
	if err == nil || IsDefinitive(err) {
		t.Fatalf("transport error must not be definitive: %v", err)
	}
	if !errors.As(err, &ge) || ge.Status != 0 {
		t.Fatalf("transport error should be a gateway Error with status 0: %v", err)
	}
	if IsDefinitive(errors.New("plain")) {
		t.Fatal("plain errors are never definitive")
	}
}

func TestRazorpayFetchRefundsAndQRRefunds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/payments/pay_1/refunds":
			_, _ = w.Write([]byte(`{"items":[{"id":"rfnd_1","amount":300,"status":"processed","receipt":"rf-uuid"},
				{"id":"rfnd_2","amount":200,"status":"pending","notes":{"refund_id":"rf-2"}},{"id":"rfnd_3","amount":50,"status":"failed"}]}`))
		case "/payments/qr_codes/qr_1":
			_, _ = w.Write([]byte(`{"id":"qr_1","status":"closed"}`))
		case "/payments/qr_codes/qr_1/payments":
			_, _ = w.Write([]byte(`{"items":[{"id":"pay_1","amount":1000,"method":"upi","status":"captured","amount_refunded":300,"created_at":1760000000},
				{"id":"pay_2","amount":500,"method":"upi","status":"captured","created_at":1760000001}]}`))
		case "/payments/pay_2/refunds":
			t.Error("payments without refunds must not be asked for refunds")
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	rz := NewRazorpay("k", "s")
	rz.BaseURL = srv.URL
	ctx := context.Background()

	rf, err := rz.FetchRefunds(ctx, "pay_1")
	if err != nil || len(rf) != 3 {
		t.Fatalf("refunds %+v %v", rf, err)
	}
	if rf[0].Receipt != "rf-uuid" || rf[0].Status != "processed" || rf[1].Receipt != "rf-2" || rf[1].Status != "pending" || rf[2].Status != "failed" {
		t.Fatalf("mapping %+v", rf)
	}
	st, err := rz.FetchQR(ctx, "qr_1")
	if err != nil || !st.Closed || len(st.Payments) != 2 {
		t.Fatalf("fetch qr %+v %v", st, err)
	}
	if st.Payments[0].RefundedPaise != 300 || len(st.Payments[0].Refunds) != 3 || len(st.Payments[1].Refunds) != 0 {
		t.Fatalf("refunds on payments %+v", st.Payments)
	}
}

// Item 8: Razorpay settlement.processed → Settlement with fee and tax split.
func TestParseSettlementProcessed(t *testing.T) {
	raw := `{"entity":"event","event":"settlement.processed","created_at":1760000100,"payload":{"settlement":{"entity":{
		"id":"setl_1","entity":"settlement","amount":97640,"status":"processed","fees":2000,"tax":360,"utr":"UTR123","created_at":1760000000}}}}`
	ev, err := ParseWebhook([]byte(raw))
	if err != nil || ev.Kind != KindSettlement || ev.Settlement == nil {
		t.Fatalf("parse: %+v %v", ev, err)
	}
	s := ev.Settlement
	if s.GatewayRef != "setl_1" || s.UTR != "UTR123" || s.NetPaise != 97640 || s.FeePaise != 2000 || s.FeeTaxPaise != 360 ||
		s.GrossPaise != 100000 || s.RefundsPaise != 0 || s.SettledAt.Unix() != 1760000000 {
		t.Fatalf("settlement %+v", s)
	}
	if s.GrossPaise-s.RefundsPaise != s.NetPaise+s.FeePaise+s.FeeTaxPaise {
		t.Fatal("gross − refunds must equal net + fee + tax")
	}
	if ev, err := ParseWebhook([]byte(`{"event":"settlement.processed","payload":{"settlement":{"entity":{"id":"setl_2","amount":1,"status":"created"}}}}`)); err != nil || ev.Kind != KindIgnore {
		t.Fatalf("non-processed settlement must be ignored: %+v %v", ev, err)
	}
	if _, err := ParseWebhook([]byte(`{"event":"settlement.processed","payload":{}}`)); err == nil {
		t.Fatal("missing entity must error")
	}
}

func TestRefundWebhookFallsBackToReceipt(t *testing.T) {
	ev, err := ParseWebhook([]byte(`{"event":"refund.processed","payload":{"refund":{"entity":{"id":"rfnd_9","payment_id":"pay_1","amount":500,"receipt":"rf-receipt","notes":[]}}}}`))
	if err != nil || ev.RefundNote != "rf-receipt" {
		t.Fatalf("receipt fallback: %+v %v", ev, err)
	}
}

type memCreds map[string]Credentials // tenant|gateway → creds

func (m memCreds) Credentials(_ context.Context, tenantID, gw string) (Credentials, bool, error) {
	c, ok := m[tenantID+"|"+gw]
	return c, ok, nil
}

// Item 9: tenant keys win over platform keys; platform keys remain the fallback.
func TestRegistryResolvesPerTenant(t *testing.T) {
	reg := NewRegistry()
	global := NewRazorpay("rzp_live_platform", "s")
	reg.Add(global, RazorpayWebhooks{Secret: "global"})
	reg.Enable("razorpay", RazorpayFactory("", nil), RazorpayWebhooks{Secret: "global"})
	reg.Enable("cashfree", CashfreeFactory("", "", "", nil), CashfreeWebhooks{})
	if err := reg.SetDefault("razorpay"); err != nil {
		t.Fatal(err)
	}
	reg.Creds = memCreds{
		"t1|razorpay": {TenantID: "t1", Gateway: "razorpay", KeyID: "rzp_test_t1", KeySecret: "s1", Env: "test"},
		"t1|cashfree": {TenantID: "t1", Gateway: "cashfree", KeyID: "TESTcf", KeySecret: "c1", Env: "sandbox"},
	}
	ctx := context.Background()

	g, err := reg.For(ctx, "t1", "razorpay")
	if err != nil || g.(*Razorpay).KeyID != "rzp_test_t1" {
		t.Fatalf("tenant keys must win: %+v %v", g, err)
	}
	if g, err := reg.For(ctx, "t2", "razorpay"); err != nil || g != Gateway(global) {
		t.Fatalf("platform fallback: %+v %v", g, err)
	}
	if g, err := reg.DefaultFor(ctx, "t1"); err != nil || g.(*Razorpay).KeyID != "rzp_test_t1" {
		t.Fatalf("default for tenant: %+v %v", g, err)
	}
	// Cashfree has no platform keys: only tenants with their own keys may use it.
	if g, err := reg.For(ctx, "t1", "cashfree"); err != nil || g.(*Cashfree).BaseURL != CashfreeSandboxURL {
		t.Fatalf("tenant cashfree: %+v %v", g, err)
	}
	if _, err := reg.For(ctx, "t2", "cashfree"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("t2 has no cashfree: %v", err)
	}
	n1, _ := reg.NamesFor(ctx, "t1")
	n2, _ := reg.NamesFor(ctx, "t2")
	if strings.Join(n1, ",") != "cashfree,razorpay" || strings.Join(n2, ",") != "razorpay" {
		t.Fatalf("names t1=%v t2=%v", n1, n2)
	}
	if strings.Join(reg.Supported(), ",") != "cashfree,razorpay" || len(reg.Webhooks()) != 2 || !reg.TenantConfigurable("cashfree") || reg.TenantConfigurable("mock") {
		t.Fatalf("supported %v hooks %d", reg.Supported(), len(reg.Webhooks()))
	}
}

func TestVerifyWithTenantSecret(t *testing.T) {
	body := []byte(`{"event":"payment.captured"}`)
	w := RazorpayWebhooks{Secret: "global"}
	h := http.Header{}
	h.Set("X-Razorpay-Signature", Sign(body, "tenant"))
	if w.Verify(body, h) || !w.VerifyWith(body, h, "tenant") || w.VerifyWith(body, h, "") {
		t.Fatal("tenant secret verification")
	}
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	cw := CashfreeWebhooks{Secret: "", MaxSkew: time.Hour, Now: func() time.Time { return now }}
	ch := cfHeader(body, strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10), "tsec")
	if cw.Verify(body, ch) || !cw.VerifyWith(body, ch, "tsec") {
		t.Fatal("cashfree tenant secret verification")
	}
}
