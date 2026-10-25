package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"

	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// Webhooks receives one gateway's webhooks (POST /v1/webhooks/{gateway}).
//
// Webhooks arrive without a tenant in the path. The body is parsed first (it is
// untrusted until a signature verifies and nothing is acted on), the tenant it
// refers to is resolved through the payment request / payment it names, and the
// signature is checked with that tenant's own webhook secret; the platform-wide
// secret is the fallback. Settlements name no payment, so every tenant's secret
// for the gateway is tried. If nothing verifies the webhook is rejected.
type Webhooks struct {
	Source  gateway.WebhookSource
	Ledger  Ledger
	Secrets gateway.WebhookSecretSource // nil = platform secret only
	Log     *slog.Logger
}

// Outcome is the JSON answer to a webhook (gateways only look at the status code).
type Outcome struct {
	Event  string `json:"event"`
	Result Result `json:"result"`
}

// Process verifies and applies one webhook. A non-nil error means "retry later"
// (5xx); bad signatures and malformed bodies return an *httpx.Error (4xx).
func (wh *Webhooks) Process(ctx context.Context, raw []byte, h http.Header) (Outcome, error) {
	gw := wh.Source.Gateway()
	ev, perr := wh.Source.Parse(raw)
	tenantID, ok := wh.verify(ctx, raw, h, ev, perr == nil)
	if !ok {
		return Outcome{}, httpx.Unauthorized("invalid webhook signature")
	}
	if perr != nil {
		return Outcome{}, httpx.BadRequest("malformed webhook: %s", perr.Error())
	}
	eventID := wh.Source.EventID(h)
	if eventID == "" {
		// No delivery id header (Cashfree): retries carry the same body, so its hash is stable.
		sum := sha256.Sum256(raw)
		eventID = gw + ":sha256:" + hex.EncodeToString(sum[:])
	}
	out := Outcome{Event: ev.Type}
	var err error
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
	case gateway.KindSettlement:
		out.Result, err = wh.Ledger.SettlementProcessed(ctx, eventID, raw, SettlementDone{
			Gateway: gw, TenantID: tenantID, Settlement: *ev.Settlement,
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

// verify returns the tenant the webhook belongs to ("" when only the platform
// secret matched and the body names no tenant) and whether any secret verified.
func (wh *Webhooks) verify(ctx context.Context, raw []byte, h http.Header, ev gateway.WebhookEvent, parsed bool) (string, bool) {
	gw := wh.Source.Gateway()
	log := logx.From(ctx)
	var tenantID string
	if parsed && wh.Secrets != nil {
		if ev.Kind == gateway.KindSettlement {
			all, err := wh.Secrets.AllWebhookSecrets(ctx, gw)
			if err != nil {
				log.Error("webhook: list tenant secrets", "gateway", gw, "error", err.Error())
			}
			for _, ts := range all {
				if wh.Source.VerifyWith(raw, h, ts.Secret) {
					return ts.TenantID, true
				}
			}
		} else {
			t, err := wh.Ledger.TenantFor(ctx, gw, ev)
			if err != nil {
				log.Error("webhook: resolve tenant", "gateway", gw, "error", err.Error())
			}
			tenantID = t
			if t != "" {
				sec, found, err := wh.Secrets.WebhookSecret(ctx, t, gw)
				if err != nil {
					log.Error("webhook: tenant secret", "gateway", gw, "tenant_id", t, "error", err.Error())
				}
				if found && wh.Source.VerifyWith(raw, h, sec) {
					return t, true
				}
			}
		}
	}
	if wh.Source.Verify(raw, h) {
		return tenantID, true
	}
	return "", false
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
