package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Mock is an in-process provider for local development and tests. Payments
// are "made" with Pay (exposed by lenden as POST /v1/dev/simulate-payment).
type Mock struct {
	VPA string

	mu       sync.Mutex
	payments map[string][]RemotePayment // gatewayRef → payments
	closed   map[string]bool
	refunds  map[string][]RemoteRefund // paymentRef → refunds
}

// NewMock builds the mock provider.
func NewMock(vpa string) *Mock {
	if vpa == "" {
		vpa = "maapan@mock"
	}
	return &Mock{VPA: vpa, payments: map[string][]RemotePayment{}, closed: map[string]bool{}, refunds: map[string][]RemoteRefund{}}
}

// Name implements Gateway.
func (m *Mock) Name() string { return "mock" }

// CreateQR implements Gateway.
func (m *Mock) CreateQR(_ context.Context, q QRRequest) (Created, error) {
	ref := "mockqr_" + uuid.NewString()[:12]
	return Created{
		GatewayRef: ref,
		UPIIntent:  UPIIntent(m.VPA, q.Name, q.AmountPaise, q.Description, q.RequestID),
	}, nil
}

// CreateLink implements Gateway.
func (m *Mock) CreateLink(_ context.Context, l LinkRequest) (Created, error) {
	ref := "mocklink_" + uuid.NewString()[:12]
	return Created{GatewayRef: ref, ShortURL: "https://pay.mock.local/" + ref}, nil
}

// Pay records a captured payment against a QR/link and returns its ref.
func (m *Mock) Pay(gatewayRef string, amountPaise int64, method string) RemotePayment {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := RemotePayment{PaymentRef: "mockpay_" + uuid.NewString()[:12], AmountPaise: amountPaise,
		Method: MapMethod(method), Status: "captured", CapturedAt: time.Now().UTC()}
	m.payments[gatewayRef] = append(m.payments[gatewayRef], p)
	m.closed[gatewayRef] = true
	return p
}

func (m *Mock) fetch(ref string) RemoteStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := RemoteStatus{Closed: m.closed[ref]}
	for _, p := range m.payments[ref] {
		p.Refunds = append([]RemoteRefund(nil), m.refunds[p.PaymentRef]...)
		for _, r := range p.Refunds {
			if r.Status == "processed" {
				p.RefundedPaise += r.AmountPaise
			}
		}
		if p.AmountPaise > 0 && p.RefundedPaise >= p.AmountPaise {
			p.Status = "refunded"
		}
		st.Payments = append(st.Payments, p)
	}
	return st
}

// FetchQR implements Gateway.
func (m *Mock) FetchQR(_ context.Context, ref string) (RemoteStatus, error) { return m.fetch(ref), nil }

// FetchLink implements Gateway.
func (m *Mock) FetchLink(_ context.Context, ref string) (RemoteStatus, error) {
	return m.fetch(ref), nil
}

// Refund implements Gateway; mock refunds are processed immediately.
func (m *Mock) Refund(_ context.Context, paymentRef string, amountPaise int64, receipt string) (Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := RemoteRefund{RefundRef: "mockrfnd_" + uuid.NewString()[:12], Receipt: receipt, AmountPaise: amountPaise, Status: "processed"}
	m.refunds[paymentRef] = append(m.refunds[paymentRef], r)
	return Refund{RefundRef: r.RefundRef, Status: r.Status}, nil
}

// FetchRefunds implements Gateway.
func (m *Mock) FetchRefunds(_ context.Context, paymentRef string) ([]RemoteRefund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RemoteRefund(nil), m.refunds[paymentRef]...), nil
}
