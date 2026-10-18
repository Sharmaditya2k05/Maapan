package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"

	"github.com/taraju/lenden/internal/gateway"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/mool/logx"
)

// Webhooks receives one gateway's webhooks (POST /v1/webhooks/{gateway}).
type Webhooks struct {
	Source gateway.WebhookSource
	Ledger Ledger
	Log    *slog.Logger
}

// Outcome is the JSON answer to a webhook (gateways only look at the status code).
type Outcome struct {
	Event  string `json:"event"`
	Result Result `json:"result"`
}

// Process verifies and applies one webhook. A non-nil error means "retry later"
// (5xx); bad signatures and malformed bodies return an *httpx.Error (4xx).
func (wh *Webhooks) Process(ctx context.Context, raw []byte, h http.Header) (Outcome, error) {
	if !wh.Source.Verify(raw, h) {
		return Outcome{}, httpx.Unauthorized("invalid webhook signature")
	}
	gw := wh.Source.Gateway()
	eventID := wh.Source.EventID(h)
	ev, err := wh.Source.Parse(raw)
	if err != nil {
		return Outcome{}, httpx.BadRequest("malformed webhook: %s", err.Error())
	}
	if eventID == "" {
		// No delivery id header (Cashfree): retries carry the same body, so its hash is stable.
		sum := sha256.Sum256(raw)
		eventID = gw + ":sha256:" + hex.EncodeToString(sum[:])
	}
	out := Outcome{Event: ev.Type}
	switch ev.Kind {
	case gateway.KindCapture:
		out.Result, err = wh.Ledger.Capture(ctx, eventID, raw, Capture{
			RequestID: ev.RequestID, Gateway: gw, PaymentRef: ev.PaymentRef,
			AmountPaise: ev.AmountPaise, Method: ev.Method, CapturedAt: ev.CapturedAt,
		})
	case gateway.KindRefund:
		out.Result, err = wh.Ledger.RefundProcessed(ctx, eventID, raw, RefundDone{
			Gateway: gw, RefundRef: ev.RefundRef, RefundID: ev.RefundNote,
			PaymentRef: ev.PaymentRef, AmountPaise: ev.AmountPaise,
		})
	default:
		out.Result, err = wh.Ledger.Ignore(ctx, eventID, gw, ev.Type, raw)
	}
	if err != nil {
		return Outcome{}, err
	}
	if out.Result == UnknownRequest {
		logx.From(ctx).Warn("webhook for unknown request/refund", "gateway", gw, "event", ev.Type, "payment_ref", ev.PaymentRef, "request_id", ev.RequestID)
	}
	return out, nil
}

// ServeHTTP is the public webhook endpoint.
func (wh *Webhooks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("read body"))
		return
	}
	out, err := wh.Process(r.Context(), raw, r.Header)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.OK(w, out)
}
