package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Razorpay is the live driver (https://razorpay.com/docs/api/).
type Razorpay struct {
	KeyID     string
	KeySecret string
	BaseURL   string // https://api.razorpay.com/v1
	HTTP      *http.Client
}

// RazorpayBaseURL is the production API.
const RazorpayBaseURL = "https://api.razorpay.com/v1"

// NewRazorpay builds the driver.
func NewRazorpay(keyID, keySecret string) *Razorpay {
	return &Razorpay{KeyID: keyID, KeySecret: keySecret, BaseURL: RazorpayBaseURL,
		HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// RazorpayFactory builds per-tenant Razorpay drivers that share one HTTP client
// (the connection pool) and, for local tests, one base URL override.
func RazorpayFactory(baseURL string, client *http.Client) Factory {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = RazorpayBaseURL
	}
	return func(c Credentials) (Gateway, error) {
		return &Razorpay{KeyID: c.KeyID, KeySecret: c.KeySecret, BaseURL: baseURL, HTTP: client}, nil
	}
}

// Name implements Gateway.
func (r *Razorpay) Name() string { return "razorpay" }

// apiError is Razorpay's error envelope.
type apiError struct {
	Error struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"error"`
}

func (r *Razorpay) call(ctx context.Context, method, path string, body, out any, idemKey string) error {
	op := method + " " + path
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.KeyID, r.KeySecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("X-Razorpay-Idempotency", idemKey)
	}
	res, err := r.HTTP.Do(req)
	if err != nil {
		return &Error{Gateway: "razorpay", Op: op, Err: err}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		ge := &Error{Gateway: "razorpay", Op: op, Status: res.StatusCode}
		var e apiError
		if json.Unmarshal(raw, &e) == nil && e.Error.Description != "" {
			ge.Code, ge.Message = e.Error.Code, e.Error.Description
		}
		return ge
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return &Error{Gateway: "razorpay", Op: op, Status: res.StatusCode, Err: err}
		}
	}
	return nil
}

type rzpQR struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	ImageURL     string `json:"image_url"`
	ImageContent string `json:"image_content"`
}

// CreateQR implements Gateway (POST /payments/qr_codes, single-use fixed amount).
func (r *Razorpay) CreateQR(ctx context.Context, q QRRequest) (Created, error) {
	body := map[string]any{
		"type":           "upi_qr",
		"name":           truncate(q.Name, 40),
		"usage":          "single_use",
		"fixed_amount":   true,
		"payment_amount": q.AmountPaise,
		"description":    truncate(q.Description, 255),
		"notes":          map[string]string{"request_id": q.RequestID, "tenant_id": q.TenantID},
	}
	if !q.CloseBy.IsZero() {
		body["close_by"] = q.CloseBy.Unix()
	}
	var out rzpQR
	if err := r.call(ctx, http.MethodPost, "/payments/qr_codes", body, &out, ""); err != nil {
		return Created{}, err
	}
	c := Created{GatewayRef: out.ID, ImageURL: out.ImageURL}
	if strings.HasPrefix(out.ImageContent, "upi://") {
		c.UPIIntent = out.ImageContent
	}
	return c, nil
}

// rzpPayment is the subset of a Razorpay payment entity lenden reads.
type rzpPayment struct {
	ID             string `json:"id"`
	PaymentID      string `json:"payment_id"` // payment-link listings use payment_id
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
	Method         string `json:"method"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
}

func (p rzpPayment) remote() RemotePayment {
	id := p.ID
	if id == "" {
		id = p.PaymentID
	}
	return RemotePayment{PaymentRef: id, AmountPaise: p.Amount, Method: MapMethod(p.Method), Status: p.Status,
		CapturedAt: time.Unix(p.CreatedAt, 0).UTC(), RefundedPaise: p.AmountRefunded}
}

type rzpLink struct {
	ID       string       `json:"id"`
	ShortURL string       `json:"short_url"`
	Status   string       `json:"status"`
	Payments []rzpPayment `json:"payments"`
}

// CreateLink implements Gateway (POST /payment_links).
func (r *Razorpay) CreateLink(ctx context.Context, l LinkRequest) (Created, error) {
	body := map[string]any{
		"amount":          l.AmountPaise,
		"currency":        "INR",
		"description":     truncate(l.Description, 2048),
		"reference_id":    l.RequestID,
		"reminder_enable": true,
		"notes":           map[string]string{"request_id": l.RequestID, "tenant_id": l.TenantID},
	}
	if l.Phone != "" {
		body["customer"] = map[string]string{"contact": l.Phone}
		body["notify"] = map[string]bool{"sms": true}
	}
	if !l.ExpireBy.IsZero() {
		body["expire_by"] = l.ExpireBy.Unix()
	}
	var out rzpLink
	if err := r.call(ctx, http.MethodPost, "/payment_links", body, &out, ""); err != nil {
		return Created{}, err
	}
	return Created{GatewayRef: out.ID, ShortURL: out.ShortURL}, nil
}

// withRefunds lists the refunds of every payment that has any, so reconcile
// can book refunds whose webhook never arrived.
func (r *Razorpay) withRefunds(ctx context.Context, pays []RemotePayment) ([]RemotePayment, error) {
	for i := range pays {
		if pays[i].RefundedPaise <= 0 && pays[i].Status != "refunded" {
			continue
		}
		rf, err := r.FetchRefunds(ctx, pays[i].PaymentRef)
		if err != nil {
			return nil, err
		}
		pays[i].Refunds = rf
	}
	return pays, nil
}

// FetchQR implements Gateway (GET /payments/qr_codes/{id} + /payments).
func (r *Razorpay) FetchQR(ctx context.Context, ref string) (RemoteStatus, error) {
	var qr rzpQR
	if err := r.call(ctx, http.MethodGet, "/payments/qr_codes/"+url.PathEscape(ref), nil, &qr, ""); err != nil {
		return RemoteStatus{}, err
	}
	var list struct {
		Items []rzpPayment `json:"items"`
	}
	if err := r.call(ctx, http.MethodGet, "/payments/qr_codes/"+url.PathEscape(ref)+"/payments", nil, &list, ""); err != nil {
		return RemoteStatus{}, err
	}
	st := RemoteStatus{Closed: qr.Status == "closed"}
	for _, p := range list.Items {
		st.Payments = append(st.Payments, p.remote())
	}
	var err error
	st.Payments, err = r.withRefunds(ctx, st.Payments)
	return st, err
}

// FetchLink implements Gateway (GET /payment_links/{id}).
func (r *Razorpay) FetchLink(ctx context.Context, ref string) (RemoteStatus, error) {
	var l rzpLink
	if err := r.call(ctx, http.MethodGet, "/payment_links/"+url.PathEscape(ref), nil, &l, ""); err != nil {
		return RemoteStatus{}, err
	}
	st := RemoteStatus{Closed: l.Status == "expired" || l.Status == "cancelled"}
	for _, p := range l.Payments {
		st.Payments = append(st.Payments, p.remote())
	}
	var err error
	st.Payments, err = r.withRefunds(ctx, st.Payments)
	return st, err
}

// Refund implements Gateway (POST /payments/{id}/refund). receipt is our
// refund id, also used as the idempotency key so retries never double-refund.
func (r *Razorpay) Refund(ctx context.Context, paymentRef string, amountPaise int64, receipt string) (Refund, error) {
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	body := map[string]any{"amount": amountPaise, "receipt": receipt, "notes": map[string]string{"refund_id": receipt}}
	if err := r.call(ctx, http.MethodPost, "/payments/"+url.PathEscape(paymentRef)+"/refund", body, &out, receipt); err != nil {
		return Refund{}, err
	}
	return Refund{RefundRef: out.ID, Status: rzpRefundStatus(out.Status)}, nil
}

type rzpRefund struct {
	ID      string          `json:"id"`
	Amount  int64           `json:"amount"`
	Status  string          `json:"status"` // pending | processed | failed
	Receipt string          `json:"receipt"`
	Notes   json.RawMessage `json:"notes"`
}

// FetchRefunds implements Gateway (GET /payments/{id}/refunds).
func (r *Razorpay) FetchRefunds(ctx context.Context, paymentRef string) ([]RemoteRefund, error) {
	var list struct {
		Items []rzpRefund `json:"items"`
	}
	if err := r.call(ctx, http.MethodGet, "/payments/"+url.PathEscape(paymentRef)+"/refunds?count=100", nil, &list, ""); err != nil {
		return nil, err
	}
	out := make([]RemoteRefund, 0, len(list.Items))
	for _, it := range list.Items {
		receipt := it.Receipt
		if receipt == "" {
			var notes map[string]string
			if json.Unmarshal(it.Notes, &notes) == nil {
				receipt = notes["refund_id"]
			}
		}
		out = append(out, RemoteRefund{RefundRef: it.ID, Receipt: receipt, AmountPaise: it.Amount, Status: rzpRefundStatus(it.Status)})
	}
	return out, nil
}

func rzpRefundStatus(s string) string {
	switch s {
	case "processed", "failed":
		return s
	}
	return "pending"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
