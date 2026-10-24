package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cashfree is the Cashfree Payments PG driver (https://docs.cashfree.com/reference/pg-new-apis-endpoint).
//
// Mapping onto lenden's model:
//   - UPI QR      = an order (order_id = our request id) + "Order Pay" with upi.channel=qrcode,
//     which returns the QR as a data:image/png;base64 URL.
//   - Payment link = a link (link_id = our request id).
//   - Payment ref = "<order_id>/<cf_payment_id>", because Cashfree refunds are made against
//     the order, not the payment.
type Cashfree struct {
	ClientID      string
	ClientSecret  string
	BaseURL       string // https://api.cashfree.com/pg or https://sandbox.cashfree.com/pg
	APIVersion    string // x-api-version header
	FallbackPhone string // Cashfree requires a customer phone; used for walk-in QR payments
	HTTP          *http.Client
}

// Cashfree base URLs.
const (
	CashfreeProdURL    = "https://api.cashfree.com/pg"
	CashfreeSandboxURL = "https://sandbox.cashfree.com/pg"
)

// NewCashfree builds the driver. env is "production" or "sandbox".
func NewCashfree(clientID, clientSecret, env, apiVersion, fallbackPhone string) *Cashfree {
	base := CashfreeSandboxURL
	if env == "production" {
		base = CashfreeProdURL
	}
	if apiVersion == "" {
		apiVersion = "2023-08-01"
	}
	return &Cashfree{ClientID: clientID, ClientSecret: clientSecret, BaseURL: base, APIVersion: apiVersion,
		FallbackPhone: fallbackPhone, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// CashfreeFactory builds per-tenant Cashfree drivers. The tenant's Env picks
// sandbox or production unless baseURL overrides it (local tests only).
func CashfreeFactory(apiVersion, fallbackPhone, baseURL string, client *http.Client) Factory {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return func(c Credentials) (Gateway, error) {
		cf := NewCashfree(c.KeyID, c.KeySecret, c.Env, apiVersion, fallbackPhone)
		cf.HTTP = client
		if baseURL != "" {
			cf.BaseURL = baseURL
		}
		return cf, nil
	}
}

// Name implements Gateway.
func (c *Cashfree) Name() string { return "cashfree" }

func (c *Cashfree) call(ctx context.Context, method, path string, body, out any, idemKey string) error {
	op := method + " " + path
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-client-id", c.ClientID)
	req.Header.Set("x-client-secret", c.ClientSecret)
	req.Header.Set("x-api-version", c.APIVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("x-idempotency-key", idemKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return &Error{Gateway: "cashfree", Op: op, Err: err}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		ge := &Error{Gateway: "cashfree", Op: op, Status: res.StatusCode}
		var e struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			ge.Code, ge.Message = e.Code, e.Message
		}
		return ge
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return &Error{Gateway: "cashfree", Op: op, Status: res.StatusCode, Err: err}
		}
	}
	return nil
}

// CashfreePaymentRef joins the order and payment ids into lenden's single payment ref.
func CashfreePaymentRef(orderID string, cfPaymentID any) string {
	return fmt.Sprintf("%s/%v", orderID, cfPaymentID)
}

func splitCashfreeRef(ref string) (orderID, paymentID string, ok bool) {
	i := strings.LastIndexByte(ref, '/')
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// rupees converts paise to the decimal rupee number Cashfree expects.
func rupees(paise int64) float64 { return float64(paise) / 100 }

// paise converts a Cashfree rupee amount to paise.
func paise(r float64) int64 { return int64(math.Round(r * 100)) }

// cfPhone returns the last 10 digits of an Indian phone number, the format Cashfree accepts.
func cfPhone(p string) string {
	var d []byte
	for i := 0; i < len(p); i++ {
		if p[i] >= '0' && p[i] <= '9' {
			d = append(d, p[i])
		}
	}
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return string(d)
}

// cfExpiry keeps an expiry at least 16 minutes ahead (Cashfree rejects shorter expiries).
func cfExpiry(t time.Time) string {
	minT := time.Now().Add(16 * time.Minute)
	if t.IsZero() || t.Before(minT) {
		t = minT
	}
	return t.UTC().Format(time.RFC3339)
}

type cfOrder struct {
	OrderID          string            `json:"order_id"`
	OrderStatus      string            `json:"order_status"` // ACTIVE | PAID | EXPIRED | TERMINATED | TERMINATION_REQUESTED
	PaymentSessionID string            `json:"payment_session_id"`
	OrderTags        map[string]string `json:"order_tags"`
}

type cfPayment struct {
	CFPaymentID   json.Number `json:"cf_payment_id"`
	OrderID       string      `json:"order_id"`
	PaymentStatus string      `json:"payment_status"` // SUCCESS | FAILED | PENDING | USER_DROPPED | CANCELLED | ...
	PaymentAmount float64     `json:"payment_amount"`
	PaymentGroup  string      `json:"payment_group"`
	PaymentTime   string      `json:"payment_time"`
}

func (p cfPayment) remote(orderID string) RemotePayment {
	if p.OrderID != "" {
		orderID = p.OrderID
	}
	st := strings.ToLower(p.PaymentStatus)
	if p.PaymentStatus == "SUCCESS" {
		st = "captured"
	}
	return RemotePayment{PaymentRef: CashfreePaymentRef(orderID, p.CFPaymentID), AmountPaise: paise(p.PaymentAmount),
		Method: MapMethod(p.PaymentGroup), Status: st, CapturedAt: parseCFTime(p.PaymentTime)}
}

func parseCFTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

// CreateQR implements Gateway: create an order, then ask for a UPI QR for it.
func (c *Cashfree) CreateQR(ctx context.Context, q QRRequest) (Created, error) {
	phone := cfPhone(q.Phone)
	if phone == "" {
		phone = cfPhone(c.FallbackPhone)
	}
	if len(phone) != 10 {
		return Created{}, fmt.Errorf("cashfree needs a 10-digit customer phone: pass party_phone or set CASHFREE_FALLBACK_PHONE")
	}
	body := map[string]any{
		"order_id":       q.RequestID,
		"order_amount":   rupees(q.AmountPaise),
		"order_currency": "INR",
		"order_note":     truncate(q.Description, 200),
		"customer_details": map[string]string{
			"customer_id":    "trj-" + phone,
			"customer_phone": phone,
		},
		"order_expiry_time": cfExpiry(q.CloseBy),
		"order_tags":        map[string]string{"request_id": q.RequestID, "tenant_id": q.TenantID},
	}
	var ord cfOrder
	if err := c.call(ctx, http.MethodPost, "/orders", body, &ord, q.RequestID); err != nil {
		return Created{}, err
	}
	if ord.PaymentSessionID == "" {
		return Created{}, fmt.Errorf("cashfree /orders: no payment_session_id returned")
	}
	created := Created{GatewayRef: ord.OrderID}

	qr, err := c.orderPay(ctx, ord.PaymentSessionID, "qrcode")
	if err != nil {
		return Created{}, err
	}
	created.ImageURL = qr["qrcode"]
	if created.ImageURL == "" {
		return Created{}, fmt.Errorf("cashfree order pay: no qrcode in response")
	}
	// The intent link lets a phone on the same device open a UPI app; nice to have, not required.
	if links, err := c.orderPay(ctx, ord.PaymentSessionID, "link"); err == nil {
		created.UPIIntent = links["default"]
	}
	return created, nil
}

// orderPay calls POST /orders/sessions for a UPI channel and returns data.payload.
func (c *Cashfree) orderPay(ctx context.Context, sessionID, channel string) (map[string]string, error) {
	var out struct {
		Data struct {
			Payload map[string]string `json:"payload"`
		} `json:"data"`
	}
	body := map[string]any{
		"payment_session_id": sessionID,
		"payment_method":     map[string]any{"upi": map[string]string{"channel": channel}},
	}
	if err := c.call(ctx, http.MethodPost, "/orders/sessions", body, &out, ""); err != nil {
		return nil, err
	}
	return out.Data.Payload, nil
}

type cfLink struct {
	LinkID         string  `json:"link_id"`
	LinkURL        string  `json:"link_url"`
	LinkStatus     string  `json:"link_status"` // ACTIVE | PAID | PARTIALLY_PAID | EXPIRED | CANCELLED
	LinkAmountPaid float64 `json:"link_amount_paid"`
}

// CreateLink implements Gateway (POST /links).
func (c *Cashfree) CreateLink(ctx context.Context, l LinkRequest) (Created, error) {
	phone := cfPhone(l.Phone)
	body := map[string]any{
		"link_id":          l.RequestID,
		"link_amount":      rupees(l.AmountPaise),
		"link_currency":    "INR",
		"link_purpose":     truncate(l.Description, 500),
		"customer_details": map[string]string{"customer_phone": phone},
		"link_notify":      map[string]bool{"send_sms": phone != ""},
		"link_notes":       map[string]string{"request_id": l.RequestID, "tenant_id": l.TenantID},
	}
	if !l.ExpireBy.IsZero() {
		body["link_expiry_time"] = cfExpiry(l.ExpireBy)
	}
	var out cfLink
	if err := c.call(ctx, http.MethodPost, "/links", body, &out, l.RequestID); err != nil {
		return Created{}, err
	}
	return Created{GatewayRef: out.LinkID, ShortURL: out.LinkURL}, nil
}

// orderPayments lists an order's payments; successful ones also carry the
// order's refunds so reconcile can book a refund whose webhook never came.
func (c *Cashfree) orderPayments(ctx context.Context, orderID string) ([]RemotePayment, error) {
	var list []cfPayment
	if err := c.call(ctx, http.MethodGet, "/orders/"+url.PathEscape(orderID)+"/payments", nil, &list, ""); err != nil {
		return nil, err
	}
	out := make([]RemotePayment, 0, len(list))
	var refunds []cfRefund
	fetched := false
	for _, p := range list {
		rp := p.remote(orderID)
		if rp.Status == "captured" {
			if !fetched {
				var err error
				if refunds, err = c.orderRefunds(ctx, orderID); err != nil {
					return nil, err
				}
				fetched = true
			}
			for _, rf := range refunds {
				if rf.CFPaymentID.String() != "" && rf.CFPaymentID.String() != p.CFPaymentID.String() {
					continue
				}
				rr := rf.remote()
				rp.Refunds = append(rp.Refunds, rr)
				if rr.Status == "processed" {
					rp.RefundedPaise += rr.AmountPaise
				}
			}
			if rp.RefundedPaise >= rp.AmountPaise && rp.AmountPaise > 0 {
				rp.Status = "refunded"
			}
		}
		out = append(out, rp)
	}
	return out, nil
}

// FetchQR implements Gateway (GET /orders/{id} + /orders/{id}/payments).
func (c *Cashfree) FetchQR(ctx context.Context, orderID string) (RemoteStatus, error) {
	var ord cfOrder
	if err := c.call(ctx, http.MethodGet, "/orders/"+url.PathEscape(orderID), nil, &ord, ""); err != nil {
		return RemoteStatus{}, err
	}
	pays, err := c.orderPayments(ctx, orderID)
	if err != nil {
		return RemoteStatus{}, err
	}
	closed := ord.OrderStatus == "EXPIRED" || ord.OrderStatus == "TERMINATED" || ord.OrderStatus == "TERMINATION_REQUESTED"
	return RemoteStatus{Closed: closed, Payments: pays}, nil
}

// FetchLink implements Gateway (GET /links/{id} + its orders' payments).
func (c *Cashfree) FetchLink(ctx context.Context, linkID string) (RemoteStatus, error) {
	var l cfLink
	if err := c.call(ctx, http.MethodGet, "/links/"+url.PathEscape(linkID), nil, &l, ""); err != nil {
		return RemoteStatus{}, err
	}
	st := RemoteStatus{Closed: l.LinkStatus == "EXPIRED" || l.LinkStatus == "CANCELLED"}
	if l.LinkAmountPaid <= 0 {
		return st, nil
	}
	var orders []cfOrder
	if err := c.call(ctx, http.MethodGet, "/links/"+url.PathEscape(linkID)+"/orders", nil, &orders, ""); err != nil {
		return RemoteStatus{}, err
	}
	for _, o := range orders {
		if o.OrderStatus != "PAID" {
			continue
		}
		pays, err := c.orderPayments(ctx, o.OrderID)
		if err != nil {
			return RemoteStatus{}, err
		}
		st.Payments = append(st.Payments, pays...)
	}
	return st, nil
}

// Refund implements Gateway (POST /orders/{order_id}/refunds). receipt is our refund id,
// sent as Cashfree's refund_id so a retry can never refund twice.
func (c *Cashfree) Refund(ctx context.Context, paymentRef string, amountPaise int64, receipt string) (Refund, error) {
	orderID, _, ok := splitCashfreeRef(paymentRef)
	if !ok {
		return Refund{}, fmt.Errorf("cashfree refund: payment ref %q is not <order_id>/<cf_payment_id>", paymentRef)
	}
	var out struct {
		CFRefundID   json.Number `json:"cf_refund_id"`
		RefundStatus string      `json:"refund_status"` // SUCCESS | PENDING | CANCELLED | ONHOLD
	}
	body := map[string]any{"refund_amount": rupees(amountPaise), "refund_id": receipt, "refund_note": "Taraju refund"}
	if err := c.call(ctx, http.MethodPost, "/orders/"+url.PathEscape(orderID)+"/refunds", body, &out, receipt); err != nil {
		return Refund{}, err
	}
	return Refund{RefundRef: out.CFRefundID.String(), Status: cfRefundStatus(out.RefundStatus)}, nil
}

type cfRefund struct {
	CFRefundID   json.Number `json:"cf_refund_id"`
	CFPaymentID  json.Number `json:"cf_payment_id"`
	RefundID     string      `json:"refund_id"`
	RefundAmount float64     `json:"refund_amount"`
	RefundStatus string      `json:"refund_status"`
}

func (r cfRefund) remote() RemoteRefund {
	return RemoteRefund{RefundRef: r.CFRefundID.String(), Receipt: r.RefundID, AmountPaise: paise(r.RefundAmount),
		Status: cfRefundStatus(r.RefundStatus)}
}

func (c *Cashfree) orderRefunds(ctx context.Context, orderID string) ([]cfRefund, error) {
	var list []cfRefund
	if err := c.call(ctx, http.MethodGet, "/orders/"+url.PathEscape(orderID)+"/refunds", nil, &list, ""); err != nil {
		return nil, err
	}
	return list, nil
}

// FetchRefunds implements Gateway (GET /orders/{order_id}/refunds, filtered to the payment).
func (c *Cashfree) FetchRefunds(ctx context.Context, paymentRef string) ([]RemoteRefund, error) {
	orderID, payID, ok := splitCashfreeRef(paymentRef)
	if !ok {
		return nil, fmt.Errorf("cashfree refunds: payment ref %q is not <order_id>/<cf_payment_id>", paymentRef)
	}
	list, err := c.orderRefunds(ctx, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteRefund, 0, len(list))
	for _, rf := range list {
		if rf.CFPaymentID.String() != "" && rf.CFPaymentID.String() != payID {
			continue
		}
		out = append(out, rf.remote())
	}
	return out, nil
}

func cfRefundStatus(s string) string {
	switch s {
	case "SUCCESS":
		return "processed"
	case "CANCELLED":
		return "failed"
	}
	return "pending"
}
