package messaging

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/money"
	"github.com/taraju/soochna/internal/clients"
	"github.com/taraju/soochna/internal/templates"
)

// EventRoutes are soochna's Pub/Sub subscriptions (ARCHITECTURE.md §5).
func (s *Service) EventRoutes() events.Router {
	return events.Router{
		events.InvoiceCreated:  s.onEvent(invoiceMessage),
		events.PaymentCaptured: s.onEvent(paymentMessage),
		events.StockLow:        s.onEvent(stockLowMessage),
		events.BatchExpiring:   s.onEvent(batchExpiringMessage),
		events.ImportCompleted: s.onEvent(importMessage),
	}
}

// builder turns an event into zero or one message (ok=false: nothing to send).
type builder func(e events.Envelope, t clients.Tenant) (in SendInput, ok bool, err error)

func (s *Service) onEvent(build builder) events.HandlerFunc {
	return func(ctx context.Context, e events.Envelope) error {
		var seen bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, e.ID).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil
		}
		t, err := s.Tenants.Get(ctx, e.TenantID)
		if err != nil {
			return err // pehchaan unavailable: let Pub/Sub retry
		}
		in, ok, err := build(e, t)
		if err != nil {
			s.Log.Warn("dropping undecodable event", "event_id", e.ID, "error", err.Error())
		} else if ok {
			// The idempotency key makes redelivery safe even if we crash before
			// marking the event processed.
			in.IdempotencyKey = "event:" + e.ID + ":" + in.Template
			if verr := in.Validate(); verr != nil {
				s.Log.Info("skipping notification", "event_id", e.ID, "reason", verr.Error())
			} else if _, err := s.Send(ctx, e.TenantID, in); err != nil {
				return err
			}
		}
		return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			_, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
			return err
		})
	}
}

func istDate(s string) string {
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d.Format("02 Jan 2006")
	}
	return s
}

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

func invoiceMessage(e events.Envelope, t clients.Tenant) (SendInput, bool, error) {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return SendInput{}, false, err
	}
	if p.DocType != "sale" || p.PartyPhone == "" {
		return SendInput{}, false, nil
	}
	name := p.PartyName
	if name == "" {
		name = "Customer"
	}
	return SendInput{Channel: "whatsapp", To: p.PartyPhone, Template: templates.Invoice, Params: map[string]string{
		"party_name": name, "business": t.BusinessName, "number": p.Number,
		"amount": money.Format(p.GrandTotalPaise), "date": istDate(p.InvoiceDate),
	}}, true, nil
}

func paymentMessage(e events.Envelope, t clients.Tenant) (SendInput, bool, error) {
	var p events.PaymentPayload
	if err := e.Decode(&p); err != nil {
		return SendInput{}, false, err
	}
	if p.PartyPhone == "" {
		return SendInput{}, false, nil
	}
	ref := p.GatewayRef
	if ref == "" {
		ref = p.PaymentID
	}
	return SendInput{Channel: "whatsapp", To: p.PartyPhone, Template: templates.PaymentReceipt, Params: map[string]string{
		"party_name": "Customer", "business": t.BusinessName, "amount": money.Format(p.AmountPaise), "ref": ref,
	}}, true, nil
}

func stockLowMessage(e events.Envelope, t clients.Tenant) (SendInput, bool, error) {
	var p events.StockLowPayload
	if err := e.Decode(&p); err != nil {
		return SendInput{}, false, err
	}
	if t.OwnerPhone == "" {
		return SendInput{}, false, nil
	}
	return SendInput{Channel: "whatsapp", To: t.OwnerPhone, Template: templates.StockLow, Params: map[string]string{
		"item": p.ItemName, "on_hand": fmtQty(p.OnHand), "reorder_level": fmtQty(p.ReorderLevel), "business": t.BusinessName,
	}}, true, nil
}

func batchExpiringMessage(e events.Envelope, t clients.Tenant) (SendInput, bool, error) {
	var p events.BatchExpiringPayload
	if err := e.Decode(&p); err != nil {
		return SendInput{}, false, err
	}
	if t.OwnerPhone == "" {
		return SendInput{}, false, nil
	}
	return SendInput{Channel: "whatsapp", To: t.OwnerPhone, Template: templates.BatchExpiring, Params: map[string]string{
		"item": p.ItemName, "batch": p.BatchNo, "expiry": istDate(p.ExpiryDate), "days": strconv.Itoa(p.DaysLeft),
		"qty": fmtQty(p.Qty), "business": t.BusinessName,
	}}, true, nil
}

func importMessage(e events.Envelope, t clients.Tenant) (SendInput, bool, error) {
	var p events.ImportCompletedPayload
	if err := e.Decode(&p); err != nil {
		return SendInput{}, false, err
	}
	to := p.NotifyPhone
	if to == "" {
		to = t.OwnerPhone
	}
	if to == "" {
		return SendInput{}, false, nil
	}
	return SendInput{Channel: "whatsapp", To: to, Template: templates.ImportDone, Params: map[string]string{
		"kind": p.Kind, "imported": strconv.Itoa(p.Imported), "total": strconv.Itoa(p.Total),
		"failed": fmt.Sprint(p.Failed),
	}}, true, nil
}
