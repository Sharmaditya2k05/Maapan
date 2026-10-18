package payment

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
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
	// Ignore logs a webhook we do not act on. Duplicate if already seen.
	Ignore(ctx context.Context, eventID, gateway, eventType string, raw []byte) (Result, error)
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

// Capture implements Ledger.
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
			tenantID                    string
			storeID, invoiceID, partyID *string
			partyPhone                  *string
			amount, paid                int64
		)
		err = tx.QueryRow(ctx, `SELECT tenant_id, store_id, invoice_id, party_id, party_phone, amount_paise, paid_paise
			FROM payment_requests WHERE id=$1 FOR UPDATE`, c.RequestID).
			Scan(&tenantID, &storeID, &invoiceID, &partyID, &partyPhone, &amount, &paid)
		if db.IsNotFound(err) {
			res = UnknownRequest
			return nil
		}
		if err != nil {
			return err
		}
		var paymentID string
		err = tx.QueryRow(ctx, `INSERT INTO payments (tenant_id, store_id, request_id, invoice_id, party_id, party_phone,
				amount_paise, method, gateway, gateway_ref, captured_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'gateway')
			ON CONFLICT (gateway, gateway_ref) WHERE gateway <> 'manual' DO NOTHING
			RETURNING id`,
			tenantID, storeID, c.RequestID, invoiceID, partyID, partyPhone, c.AmountPaise, c.Method,
			c.Gateway, c.PaymentRef, c.CapturedAt).Scan(&paymentID)
		if db.IsNotFound(err) {
			res = Duplicate // the sibling webhook (payment.captured vs qr_code.credited) got here first
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_requests SET paid_paise = paid_paise + $2,
				status = CASE WHEN paid_paise + $2 >= amount_paise THEN 'paid' ELSE status END, updated_at = now()
			WHERE id=$1`, c.RequestID, c.AmountPaise); err != nil {
			return err
		}
		ev, err := events.New(events.PaymentCaptured, "lenden", tenantID, deref(storeID), events.PaymentPayload{
			PaymentID: paymentID, InvoiceID: deref(invoiceID), PartyID: deref(partyID), PartyPhone: deref(partyPhone),
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

// RefundProcessed implements Ledger.
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
		// Find the refund we initiated (by provider ref, or our id in notes).
		var refundID, status string
		err = tx.QueryRow(ctx, `SELECT id, status FROM refunds
			WHERE gateway_ref = $1 OR ($2 <> '' AND id::text = $2) LIMIT 1 FOR UPDATE`,
			r.RefundRef, r.RefundID).Scan(&refundID, &status)
		if db.IsNotFound(err) {
			// Refund issued from the Razorpay dashboard: record it so books stay right.
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
			err = tx.QueryRow(ctx, `INSERT INTO refunds (tenant_id, payment_id, amount_paise, gateway_ref, reason, created_by)
				VALUES ($1,$2,$3,$4,'refunded outside Taraju (gateway dashboard)','gateway') RETURNING id, status`,
				tenantID, paymentID, r.AmountPaise, r.RefundRef).Scan(&refundID, &status)
		}
		if err != nil {
			return err
		}
		if status == "processed" {
			res = Duplicate
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE refunds SET gateway_ref = COALESCE(gateway_ref, $2) WHERE id=$1`, refundID, r.RefundRef); err != nil {
			return err
		}
		return applyRefund(ctx, tx, refundID)
	})
	if err == nil && res == Applied {
		l.kick()
	}
	return res, err
}

// applyRefund marks a pending refund processed, updates the payment and queues
// lenden.payment.refunded. Must run inside the caller's transaction.
func applyRefund(ctx context.Context, tx pgx.Tx, refundID string) error {
	var (
		tenantID, paymentID, method, gateway string
		amount, payAmount, refunded          int64
		storeID, invoiceID, partyID, phone   *string
		gatewayRef                           *string
	)
	err := tx.QueryRow(ctx, `UPDATE refunds SET status='processed', processed_at=now()
		WHERE id=$1 AND status='pending' RETURNING tenant_id, payment_id, amount_paise`, refundID).
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
		RETURNING store_id, invoice_id, party_id, party_phone, method, gateway, gateway_ref, amount_paise, refunded_paise`,
		tenantID, paymentID, amount).
		Scan(&storeID, &invoiceID, &partyID, &phone, &method, &gateway, &gatewayRef, &payAmount, &refunded)
	if err != nil {
		return err
	}
	ev, err := events.New(events.PaymentRefunded, "lenden", tenantID, deref(storeID), events.PaymentPayload{
		PaymentID: paymentID, InvoiceID: deref(invoiceID), PartyID: deref(partyID), PartyPhone: deref(phone),
		AmountPaise: amount, Method: method, Gateway: gateway, GatewayRef: deref(gatewayRef),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return events.Enqueue(ctx, tx, ev)
}
