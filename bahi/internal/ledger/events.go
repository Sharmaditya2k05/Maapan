package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/bahi/internal/posting"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/mool/logx"
)

// EventRoutes are bahi's Pub/Sub subscriptions (ARCHITECTURE §5).
func (s *Service) EventRoutes() events.Router {
	return events.Router{
		events.TenantCreated:       s.onTenantCreated,
		events.InvoiceCreated:      s.onInvoice,
		events.NoteCreated:         s.onInvoice,
		events.InvoiceCancelled:    s.onInvoiceCancelled,
		events.PaymentCaptured:     func(ctx context.Context, e events.Envelope) error { return s.onPayment(ctx, e, false) },
		events.PaymentRefunded:     func(ctx context.Context, e events.Envelope) error { return s.onPayment(ctx, e, true) },
		events.ProductionCompleted: s.onProduction,
		events.StockMoved:          s.onStockMoved,
		events.SettlementProcessed: s.onSettlement,
	}
}

// handle runs fn in a transaction exactly once per event id.
//
// Failures are classified. A permanent one — malformed payload, a voucher that
// cannot balance, a locked period, any other 4xx from Post — quarantines the
// event at once. Anything else (database hiccup, a party bahi has not seen
// yet, a cancellation delivered before its invoice) is returned so Pub/Sub
// redelivers, and after maxEventAttempts such failures the event is
// quarantined instead of looping forever. Quarantining marks the event
// processed in the same transaction, so it is acknowledged.
func (s *Service) handle(ctx context.Context, e events.Envelope, fn func(tx pgx.Tx) error) error {
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		return fn(tx)
	})
	if err == nil {
		return nil
	}
	reason, permanent := classify(err)
	if !permanent {
		attempts, aerr := s.recordAttempt(ctx, e, err)
		if aerr != nil {
			return aerr
		}
		if attempts < maxEventAttempts {
			logx.From(ctx).Warn("event failed, will retry", "attempt", attempts, "error", err.Error())
			return err
		}
		reason = fmt.Sprintf("gave up after %d attempts: %s", attempts, reason)
	}
	return s.quarantine(ctx, e, reason)
}

// classify splits handler errors into permanent (quarantine now) and
// retryable (redeliver, within the attempt budget).
func classify(err error) (reason string, permanent bool) {
	if isPeriodLocked(err) {
		return "period locked: " + httpx.AsError(err).Message, true
	}
	var he *httpx.Error
	if errors.As(err, &he) && he.Status < 500 {
		return he.Message, true
	}
	return err.Error(), false
}

func (s *Service) onTenantCreated(ctx context.Context, e events.Envelope) error {
	return s.handle(ctx, e, func(tx pgx.Tx) error { return EnsureChart(ctx, tx, e.TenantID) })
}

func (s *Service) onInvoice(ctx context.Context, e events.Envelope) error {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed invoice payload: "+err.Error())
	}
	v, err := posting.Invoice(p)
	if err != nil {
		return s.quarantine(ctx, e, "invoice does not balance: "+err.Error())
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		if err := s.requireParties(ctx, tx, e.TenantID, v.Entries); err != nil {
			return err
		}
		_, err := Post(ctx, tx, e.TenantID, "system:"+orDefault(p.CreatedBy, "aadesh"), v)
		return err
	})
}

// requireParties fails (retryably) when an entry names a party bahi does not
// have yet: aadesh may simply be ahead of party replication, so the event is
// redelivered rather than silently re-pointed at Walk-in Customers / Cash
// Suppliers, which would never put the receivable on the party.
func (s *Service) requireParties(ctx context.Context, tx pgx.Tx, tenantID string, es []posting.Entry) error {
	for _, en := range es {
		if en.PartyID == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM parties WHERE tenant_id=$1 AND id::text=$2)`, tenantID, en.PartyID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errRetry(fmt.Sprintf("party %s is not known to bahi yet", en.PartyID))
		}
	}
	return nil
}

func (s *Service) onInvoiceCancelled(ctx context.Context, e events.Envelope) error {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed invoice payload: "+err.Error())
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		var vid, status string
		err := tx.QueryRow(ctx, `SELECT id, status FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_id=$3`,
			e.TenantID, posting.SrcInvoice, p.InvoiceID).Scan(&vid, &status)
		if db.IsNotFound(err) {
			// The created event hasn't been processed yet; retry later so order doesn't matter.
			return errRetry("invoice voucher not posted yet")
		}
		if err != nil || status != "posted" {
			return err
		}
		_, err = Reverse(ctx, tx, e.TenantID, "system:aadesh", vid, s.eventDate(e), posting.SrcCancel, p.InvoiceID, "invoice cancelled")
		return err
	})
}

func (s *Service) onPayment(ctx context.Context, e events.Envelope, refund bool) error {
	var p events.PaymentPayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed payment payload: "+err.Error())
	}
	if p.AmountPaise <= 0 || p.PaymentID == "" {
		return s.quarantine(ctx, e, "payment without id/amount")
	}
	if refund && p.RefundID == "" {
		logx.From(ctx).Warn("refund event without refund_id; keyed on payment_id, a second partial refund would be skipped",
			"payment_id", p.PaymentID)
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.CapturedAt); err == nil && !refund {
		date = t.In(IST).Format(time.DateOnly)
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		partyID, err := s.resolvePaymentParty(ctx, tx, e.TenantID, p)
		if err != nil {
			return err
		}
		v, err := posting.Payment(p, partyID, e.StoreID, date, refund)
		if err != nil {
			return httpx.Validation("payment voucher invalid: %s", err.Error())
		}
		_, err = Post(ctx, tx, e.TenantID, "system:lenden", v)
		return err
	})
}

// resolvePaymentParty uses the party on the payment (which must exist, else
// the event is retried), else the party on the invoice's sales voucher, else
// "" (walk-in).
func (s *Service) resolvePaymentParty(ctx context.Context, tx pgx.Tx, tenantID string, p events.PaymentPayload) (string, error) {
	if p.PartyID != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM parties WHERE tenant_id=$1 AND id::text=$2)`, tenantID, p.PartyID).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			return "", errRetry(fmt.Sprintf("party %s is not known to bahi yet", p.PartyID))
		}
		return p.PartyID, nil
	}
	if p.InvoiceID != "" {
		var pid *string
		err := tx.QueryRow(ctx, `SELECT e.party_id::text FROM vouchers v JOIN voucher_entries e ON e.voucher_id=v.id
			WHERE v.tenant_id=$1 AND v.source=$2 AND v.source_id=$3 AND e.party_id IS NOT NULL LIMIT 1`,
			tenantID, posting.SrcInvoice, p.InvoiceID).Scan(&pid)
		if err != nil && !db.IsNotFound(err) {
			return "", err
		}
		if pid != nil {
			return *pid, nil
		}
	}
	return "", nil
}

func (s *Service) onProduction(ctx context.Context, e events.Envelope) error {
	var p events.ProductionCompletedPayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed production payload: "+err.Error())
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.CompletedAt); err == nil {
		date = t.In(IST).Format(time.DateOnly)
	}
	v, err := posting.Production(p, date)
	if err != nil {
		return s.quarantine(ctx, e, "production voucher invalid: "+err.Error())
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, e.TenantID, "system:nirmaan", v)
		return err
	})
}

// onStockMoved books perpetual-inventory reclassifications (COGS, inventory,
// stock loss/gain) for bhandar's costed movements; see posting.StockMoved.
func (s *Service) onStockMoved(ctx context.Context, e events.Envelope) error {
	var p events.StockMovedPayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed stock.moved payload: "+err.Error())
	}
	if p.StoreID == "" {
		p.StoreID = e.StoreID
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.OccurredAt); err == nil {
		date = t.In(IST).Format(time.DateOnly)
	}
	v, ok, err := posting.StockMoved(p, date)
	if err != nil {
		return s.quarantine(ctx, e, "stock movement cannot be posted: "+err.Error())
	}
	if !ok { // transfers, production, zero value: nothing to book
		return nil
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, e.TenantID, "system:bhandar", v)
		return err
	})
}

// onSettlement moves a gateway payout from Payment Gateway Clearing to Bank,
// booking the MDR and the input GST on it; see posting.Settlement.
func (s *Service) onSettlement(ctx context.Context, e events.Envelope) error {
	var p events.SettlementPayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed settlement payload: "+err.Error())
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.SettledAt); err == nil {
		date = t.In(IST).Format(time.DateOnly)
	}
	v, ok, err := posting.Settlement(p, e.StoreID, date)
	if err != nil {
		return s.quarantine(ctx, e, "settlement rejected: "+err.Error())
	}
	if !ok {
		return nil
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, e.TenantID, "system:lenden", v)
		return err
	})
}

func (s *Service) eventDate(e events.Envelope) string {
	if !e.OccurredAt.IsZero() {
		return e.OccurredAt.In(IST).Format(time.DateOnly)
	}
	return s.today()
}

type retryErr string

func (r retryErr) Error() string { return string(r) }

func errRetry(msg string) error { return retryErr(msg) }
