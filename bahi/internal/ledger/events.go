package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/bahi/internal/posting"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
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
	}
}

// once runs fn in a transaction exactly once per event id.
func (s *Service) once(ctx context.Context, e events.Envelope, fn func(tx pgx.Tx) error) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		return fn(tx)
	})
}

// drop logs a message that can never succeed and acknowledges it.
func drop(ctx context.Context, e events.Envelope, why string, err error) error {
	logx.From(ctx).Error("dropping event", "reason", why, "error", errString(err), "event_id", e.ID)
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) onTenantCreated(ctx context.Context, e events.Envelope) error {
	return s.once(ctx, e, func(tx pgx.Tx) error { return EnsureChart(ctx, tx, e.TenantID) })
}

func (s *Service) onInvoice(ctx context.Context, e events.Envelope) error {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return drop(ctx, e, "malformed invoice payload", err)
	}
	v, err := posting.Invoice(p)
	if err != nil {
		return drop(ctx, e, "invoice does not balance", err)
	}
	return s.once(ctx, e, func(tx pgx.Tx) error {
		v.Entries = s.knownParties(ctx, tx, e.TenantID, v.Entries, p.DocType)
		_, err := Post(ctx, tx, e.TenantID, "system:"+orDefault(p.CreatedBy, "aadesh"), v)
		return err
	})
}

// knownParties replaces entries for parties bahi doesn't know (e.g. deleted or
// not yet synced) with the walk-in/cash-supplier account so posting never fails.
func (s *Service) knownParties(ctx context.Context, tx pgx.Tx, tenantID string, es []posting.Entry, docType string) []posting.Entry {
	fallback := posting.WalkInCustomers
	if docType == "purchase" || docType == "purchase_return" {
		fallback = posting.CashSuppliers
	}
	for i, en := range es {
		if en.PartyID == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM parties WHERE tenant_id=$1 AND id::text=$2)`, tenantID, en.PartyID).Scan(&ok); err != nil || !ok {
			logx.From(ctx).Warn("unknown party on event, using fallback account", "party_id", en.PartyID)
			es[i].PartyID, es[i].AccountCode = "", fallback
		}
	}
	return es
}

func (s *Service) onInvoiceCancelled(ctx context.Context, e events.Envelope) error {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return drop(ctx, e, "malformed invoice payload", err)
	}
	return s.once(ctx, e, func(tx pgx.Tx) error {
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
		return drop(ctx, e, "malformed payment payload", err)
	}
	if p.AmountPaise <= 0 || p.PaymentID == "" {
		return drop(ctx, e, "payment without id/amount", nil)
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.CapturedAt); err == nil && !refund {
		date = t.In(IST).Format(time.DateOnly)
	}
	return s.once(ctx, e, func(tx pgx.Tx) error {
		partyID := s.resolvePaymentParty(ctx, tx, e.TenantID, p)
		v, err := posting.Payment(p, partyID, e.StoreID, date, refund)
		if err != nil {
			return drop(ctx, e, "payment voucher invalid", err)
		}
		_, err = Post(ctx, tx, e.TenantID, "system:lenden", v)
		return err
	})
}

// resolvePaymentParty uses the party on the payment, else the party on the
// invoice's sales voucher, else "" (walk-in).
func (s *Service) resolvePaymentParty(ctx context.Context, tx pgx.Tx, tenantID string, p events.PaymentPayload) string {
	if p.PartyID != "" {
		var ok bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM parties WHERE tenant_id=$1 AND id::text=$2)`, tenantID, p.PartyID).Scan(&ok) == nil && ok {
			return p.PartyID
		}
	}
	if p.InvoiceID != "" {
		var pid *string
		_ = tx.QueryRow(ctx, `SELECT e.party_id::text FROM vouchers v JOIN voucher_entries e ON e.voucher_id=v.id
			WHERE v.tenant_id=$1 AND v.source=$2 AND v.source_id=$3 AND e.party_id IS NOT NULL LIMIT 1`,
			tenantID, posting.SrcInvoice, p.InvoiceID).Scan(&pid)
		if pid != nil {
			return *pid
		}
	}
	return ""
}

func (s *Service) onProduction(ctx context.Context, e events.Envelope) error {
	var p events.ProductionCompletedPayload
	if err := e.Decode(&p); err != nil {
		return drop(ctx, e, "malformed production payload", err)
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.CompletedAt); err == nil {
		date = t.In(IST).Format(time.DateOnly)
	}
	v, err := posting.Production(p, date)
	if err != nil {
		return drop(ctx, e, "production voucher invalid", err)
	}
	return s.once(ctx, e, func(tx pgx.Tx) error {
		_, err := Post(ctx, tx, e.TenantID, "system:nirmaan", v)
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
