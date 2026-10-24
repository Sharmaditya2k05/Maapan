package payment

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/lenden/internal/gateway"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/logx"
)

// Result of applying a provider event.
type Result string

// Results.
const (
	Applied        Result = "applied"
	Duplicate      Result = "duplicate"       // webhook or payment already recorded
	UnknownRequest Result = "unknown_request" // not one of ours (or unknown refund); acknowledged, nothing changed
)

// Ledger applies provider events atomically: webhook log, business rows and
// outbox event commit together, so a retry after a crash is always safe.
type Ledger interface {
	// Capture records a captured payment. eventID (may be empty for
	// reconciliation) is logged in webhook_events in the same transaction.
	Capture(ctx context.Context, eventID string, raw []byte, c Capture) (Result, error)
	// RefundProcessed finalises a refund confirmed by the provider.
	RefundProcessed(ctx context.Context, eventID string, raw []byte, r RefundDone) (Result, error)
	// SettlementProcessed records a payout and queues lenden.settlement.processed.
	SettlementProcessed(ctx context.Context, eventID string, raw []byte, s SettlementDone) (Result, error)
	// Ignore logs a webhook we do not act on. Duplicate if already seen.
	Ignore(ctx context.Context, eventID, gateway, eventType string, raw []byte) (Result, error)
	// TenantFor resolves the tenant a webhook body refers to (through the
	// payment request or payment it names), "" when none is found.
	TenantFor(ctx context.Context, gw string, ev gateway.WebhookEvent) (string, error)
}

// PGLedger is the Postgres Ledger.
type PGLedger struct {
	DB    *pgxpool.Pool
	Relay *events.Relay
}

func logWebhook(ctx context.Context, tx pgx.Tx, eventID, gateway, eventType string, raw []byte) (bool, error) {
	if eventID == "" {
		return true, nil
	}
	if !json.Valid(raw) {
		raw = []byte("{}")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO webhook_events (event_id, gateway, event_type, payload)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, eventID, gateway, eventType, raw)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (l *PGLedger) kick() {
	if l.Relay != nil {
		l.Relay.Kick()
	}
}

// Ignore implements Ledger.
func (l *PGLedger) Ignore(ctx context.Context, eventID, gateway, eventType string, raw []byte) (Result, error) {
	res := Applied
	err := db.WithTx(ctx, l.DB, func(tx pgx.Tx) error {
		fresh, err := logWebhook(ctx, tx, eventID, gateway, eventType, raw)
		if !fresh {
			res = Duplicate
		}
		return err
	})
	return res, err
}

// TenantFor implements Ledger.
func (l *PGLedger) TenantFor(ctx context.Context, gw string, ev gateway.WebhookEvent) (string, error) {
	var tenantID string
	if isUUID(ev.RequestID) {
		err := l.DB.QueryRow(ctx, `SELECT tenant_id FROM payment_requests WHERE id=$1`, ev.RequestID).Scan(&tenantID)
		if err == nil {
			return tenantID, nil
		}
		if !db.IsNotFound(err) {
			return "", err
		}
	}
	if ev.PaymentRef != "" {
		err := l.DB.QueryRow(ctx, `SELECT tenant_id FROM payments WHERE gateway=$1 AND gateway_ref=$2`, gw, ev.PaymentRef).Scan(&tenantID)
		if err == nil {
			return tenantID, nil
		}
		if !db.IsNotFound(err) {
			return "", err
		}
	}
	if ev.GatewayRef != "" {
		err := l.DB.QueryRow(ctx, `SELECT tenant_id FROM payment_requests WHERE gateway=$1 AND gateway_ref=$2`, gw, ev.GatewayRef).Scan(&tenantID)
		if err == nil {
			return tenantID, nil
		}
		if !db.IsNotFound(err) {
			return "", err
		}
	}
	return "", nil
}

// Capture implements Ledger. The captured amount is compared with what the
// request still had outstanding: a short capture leaves the request 'partial'
// (another capture may complete it) and records short_paise on the payment;
// an over-payment records excess_paise. Both are logged.
func (l *PGLedger) Capture(ctx context.Context, eventID string, raw []byte, c Capture) (Result, error) {
	res := Applied
	err := db.WithTx(ctx, l.DB, func(tx pgx.Tx) error {
		fresh, err := logWebhook(ctx, tx, eventID, c.Gateway, "capture", raw)
		if err != nil {
			return err
		}
		if !fresh {
			res = Duplicate
			return nil
		}
		if !isUUID(c.RequestID) {
			res = UnknownRequest
			return nil
		}
		var (
			tenantID, status            string
			storeID, invoiceID, partyID *string
			partyPhone                  *string
			amount, paid                int64
		)
		err = tx.QueryRow(ctx, `SELECT tenant_id, store_id, invoice_id, party_id, party_phone, amount_paise, paid_paise, status
			FROM payment_requests WHERE id=$1 FOR UPDATE`, c.RequestID).
			Scan(&tenantID, &storeID, &invoiceID, &partyID, &partyPhone, &amount, &paid, &status)
		if db.IsNotFound(err) {
			res = UnknownRequest
			return nil
		}
		if err != nil {
			return err
		}
		due := max(amount-paid, 0)
		var short, excess int64
		if c.AmountPaise < due {
			short = due - c.AmountPaise
		} else if c.AmountPaise > due {
			excess = c.AmountPaise - due
		}
		var paymentID string
		err = tx.QueryRow(ctx, `INSERT INTO payments (tenant_id, store_id, request_id, invoice_id, party_id, party_phone,
				amount_paise, short_paise, excess_paise, method, gateway, gateway_ref, captured_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'gateway')
			ON CONFLICT (gateway, gateway_ref) WHERE gateway <> 'manual' DO NOTHING
			RETURNING id`,
			tenantID, storeID, c.RequestID, invoiceID, partyID, partyPhone, c.AmountPaise, short, excess, c.Method,
			c.Gateway, c.PaymentRef, c.CapturedAt).Scan(&paymentID)
		if db.IsNotFound(err) {
			res = Duplicate // the sibling webhook (payment.captured vs qr_code.credited) got here first
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_requests SET paid_paise = paid_paise + $2,
				status = CASE WHEN paid_paise + $2 >= amount_paise THEN 'paid'
				              WHEN status IN ('created','partial') THEN 'partial'
				              ELSE status END,
				updated_at = now()
			WHERE id=$1`, c.RequestID, c.AmountPaise); err != nil {
			return err
		}
		if short > 0 || excess > 0 {
			logx.From(ctx).Warn("capture differs from request", "payment_id", paymentID, "request_id", c.RequestID,
				"gateway", c.Gateway, "payment_ref", c.PaymentRef, "requested_paise", amount, "already_paid_paise", paid,
				"captured_paise", c.AmountPaise, "short_paise", short, "excess_paise", excess)
		}
		ev, err := events.New(events.PaymentCaptured, "lenden", tenantID, deref(storeID), events.PaymentPayload{
			PaymentID: paymentID, PaymentRequestID: c.RequestID,
			InvoiceID: deref(invoiceID), PartyID: deref(partyID), PartyPhone: deref(partyPhone),
			AmountPaise: c.AmountPaise, Method: c.Method, Gateway: c.Gateway, GatewayRef: c.PaymentRef,
			CapturedAt: c.CapturedAt.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err == nil && res == Applied {
		l.kick()
	}
	return res, err
}

// RefundProcessed implements Ledger. The refund is matched by gateway refund
// id first, then by our refund id (sent as receipt/notes); any row not yet
// processed (pending, unknown or failed) is promoted, because the gateway's
// word is final. A refund we never initiated (gateway dashboard) gets a row
// with source 'gateway'.
func (l *PGLedger) RefundProcessed(ctx context.Context, eventID string, raw []byte, r RefundDone) (Result, error) {
	res := Applied
	err := db.WithTx(ctx, l.DB, func(tx pgx.Tx) error {
		fresh, err := logWebhook(ctx, tx, eventID, r.Gateway, "refund", raw)
		if err != nil {
			return err
		}
		if !fresh {
			res = Duplicate
			return nil
		}
		var refundID, status string
		var amount int64
		err = pgx.ErrNoRows
		if r.RefundRef != "" {
			err = tx.QueryRow(ctx, `SELECT id, status, amount_paise FROM refunds WHERE gateway_ref=$1 ORDER BY created_at LIMIT 1 FOR UPDATE`,
				r.RefundRef).Scan(&refundID, &status, &amount)
		}
		if db.IsNotFound(err) && isUUID(r.RefundID) {
			err = tx.QueryRow(ctx, `SELECT id, status, amount_paise FROM refunds WHERE id=$1 FOR UPDATE`, r.RefundID).
				Scan(&refundID, &status, &amount)
		}
		if db.IsNotFound(err) {
			// Refund issued from the gateway dashboard: record it so books stay right.
			var paymentID, tenantID string
			err = tx.QueryRow(ctx, `SELECT id, tenant_id FROM payments WHERE gateway=$1 AND gateway_ref=$2`,
				r.Gateway, r.PaymentRef).Scan(&paymentID, &tenantID)
			if db.IsNotFound(err) {
				res = UnknownRequest
				return nil
			}
			if err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `INSERT INTO refunds (tenant_id, payment_id, amount_paise, gateway_ref, reason, created_by, source)
				VALUES ($1,$2,$3,$4,'refunded outside Taraju (gateway dashboard)','gateway','gateway') RETURNING id, status, amount_paise`,
				tenantID, paymentID, r.AmountPaise, nilIfEmpty(r.RefundRef)).Scan(&refundID, &status, &amount)
		}
		if err != nil {
			return err
		}
		if status == "processed" {
			res = Duplicate
			return nil
		}
		if r.AmountPaise > 0 && amount != r.AmountPaise {
			logx.From(ctx).Warn("refund amount differs from gateway", "refund_id", refundID, "ours_paise", amount, "gateway_paise", r.AmountPaise)
		}
		if _, err := tx.Exec(ctx, `UPDATE refunds SET gateway_ref = COALESCE(gateway_ref, $2), updated_at=now() WHERE id=$1`,
			refundID, nilIfEmpty(r.RefundRef)); err != nil {
			return err
		}
		return applyRefund(ctx, tx, refundID)
	})
	if err == nil && res == Applied {
		l.kick()
	}
	return res, err
}

// applyRefund marks a refund processed (from pending, unknown or failed),
// updates the payment and queues lenden.payment.refunded, one event per refund
// carrying the refund id. Must run inside the caller's transaction.
func applyRefund(ctx context.Context, tx pgx.Tx, refundID string) error {
	var (
		tenantID, paymentID, method, gateway string
		amount, payAmount, refunded          int64
		storeID, invoiceID, partyID, phone   *string
		gatewayRef                           *string
		requestID                            *string
	)
	err := tx.QueryRow(ctx, `UPDATE refunds SET status='processed', processed_at=now(), updated_at=now(), last_error=NULL
		WHERE id=$1 AND status <> 'processed' RETURNING tenant_id, payment_id, amount_paise`, refundID).
		Scan(&tenantID, &paymentID, &amount)
	if db.IsNotFound(err) {
		return nil // already processed
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `UPDATE payments SET refunded_paise = LEAST(amount_paise, refunded_paise + $3),
			status = CASE WHEN refunded_paise + $3 >= amount_paise THEN 'refunded' ELSE 'partially_refunded' END
		WHERE tenant_id=$1 AND id=$2
		RETURNING store_id, request_id, invoice_id, party_id, party_phone, method, gateway, gateway_ref, amount_paise, refunded_paise`,
		tenantID, paymentID, amount).
		Scan(&storeID, &requestID, &invoiceID, &partyID, &phone, &method, &gateway, &gatewayRef, &payAmount, &refunded)
	if err != nil {
		return err
	}
	ev, err := events.New(events.PaymentRefunded, "lenden", tenantID, deref(storeID), events.PaymentPayload{
		PaymentID: paymentID, RefundID: refundID, PaymentRequestID: deref(requestID),
		InvoiceID: deref(invoiceID), PartyID: deref(partyID), PartyPhone: deref(phone),
		AmountPaise: amount, Method: method, Gateway: gateway, GatewayRef: deref(gatewayRef),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return events.Enqueue(ctx, tx, ev)
}

// SettlementProcessed implements Ledger. Idempotent on (gateway, gateway_ref).
// The gateway's figures are stored as reported; when gross − refunds ≠ net +
// fee + fee_tax a warning is logged and the event still carries them as-is.
func (l *PGLedger) SettlementProcessed(ctx context.Context, eventID string, raw []byte, s SettlementDone) (Result, error) {
	res := Applied
	err := db.WithTx(ctx, l.DB, func(tx pgx.Tx) error {
		fresh, err := logWebhook(ctx, tx, eventID, s.Gateway, "settlement", raw)
		if err != nil {
			return err
		}
		if !fresh {
			res = Duplicate
			return nil
		}
		if s.GrossPaise-s.RefundsPaise != s.NetPaise+s.FeePaise+s.FeeTaxPaise {
			logx.From(ctx).Warn("settlement figures do not add up (gross − refunds ≠ net + fee + fee_tax); stored as reported",
				"gateway", s.Gateway, "gateway_ref", s.GatewayRef, "gross_paise", s.GrossPaise, "refunds_paise", s.RefundsPaise,
				"net_paise", s.NetPaise, "fee_paise", s.FeePaise, "fee_tax_paise", s.FeeTaxPaise)
		}
		payload := raw
		if !json.Valid(payload) {
			payload = []byte("{}")
		}
		var settlementID string
		err = tx.QueryRow(ctx, `INSERT INTO settlements (tenant_id, gateway, gateway_ref, utr, gross_paise, fee_paise, fee_tax_paise,
				refunds_paise, net_paise, settled_at, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (gateway, gateway_ref) DO NOTHING RETURNING id`,
			nilIfEmpty(s.TenantID), s.Gateway, s.GatewayRef, nilIfEmpty(s.UTR), s.GrossPaise, s.FeePaise, s.FeeTaxPaise,
			s.RefundsPaise, s.NetPaise, s.SettledAt.UTC(), payload).Scan(&settlementID)
		if db.IsNotFound(err) {
			res = Duplicate
			return nil
		}
		if err != nil {
			return err
		}
		if s.TenantID == "" {
			logx.From(ctx).Warn("settlement stored without a tenant (platform-wide secret, no tenant reference); no event published",
				"gateway", s.Gateway, "gateway_ref", s.GatewayRef, "net_paise", s.NetPaise)
			return nil
		}
		ev, err := events.New(events.SettlementProcessed, "lenden", s.TenantID, "", events.SettlementPayload{
			SettlementID: settlementID, Gateway: s.Gateway, GatewayRef: s.GatewayRef, UTR: s.UTR,
			GrossPaise: s.GrossPaise, FeePaise: s.FeePaise, FeeTaxPaise: s.FeeTaxPaise, RefundsPaise: s.RefundsPaise,
			NetPaise: s.NetPaise, SettledAt: s.SettledAt.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err == nil && res == Applied {
		l.kick()
	}
	return res, err
}
