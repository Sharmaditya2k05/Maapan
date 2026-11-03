package messaging

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/money"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/templates"
)

// EventRoutes are soochna's Pub/Sub subscriptions (ARCHITECTURE.md §5).
func (s *Service) EventRoutes() events.Router {
	return events.Router{
		events.InvoiceCreated:  s.onEvent(s.invoiceMessages),
		events.PaymentCaptured: s.onEvent(s.paymentMessages),
		events.StockLow:        s.onEvent(stockLowMessage),
		events.BatchExpiring:   s.onEvent(batchExpiringMessage),
		events.ImportCompleted: s.onEvent(importMessage),
	}
}

// builder turns an event into zero or more messages.
type builder func(ctx context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error)

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
		ins, err := build(ctx, e, t)
		if err != nil {
			if isRetryable(err) {
				return err
			}
			s.Log.Warn("dropping undecodable event", "event_id", e.ID, "error", err.Error())
		}
		for _, in := range ins {
			// The idempotency key makes redelivery safe even if we crash before
			// marking the event processed. WhatsApp keeps the original key format.
			in.IdempotencyKey = "event:" + e.ID + ":" + in.Template
			if in.Channel != templates.WhatsApp {
				in.IdempotencyKey += ":" + in.Channel
			}
			if verr := in.Validate(); verr != nil {
				s.Log.Info("skipping notification", "event_id", e.ID, "channel", in.Channel, "reason", verr.Error())
				continue
			}
			if _, err := s.Send(ctx, e.TenantID, in); err != nil {
				return err
			}
		}
		return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			_, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
			return err
		})
	}
}

// retryable wraps a lookup failure that should make Pub/Sub redeliver.
type retryable struct{ error }

func isRetryable(err error) bool { _, ok := err.(retryable); return ok }

// partyEmail returns the party's e-mail from bahi ("" when unknown or bahi is not wired).
func (s *Service) partyEmail(ctx context.Context, tenantID, partyID string) (string, error) {
	if s.Bahi == nil || partyID == "" {
		return "", nil
	}
	p, err := s.Bahi.Party(ctx, tenantID, partyID)
	if err != nil {
		return "", retryable{err}
	}
	if p == nil {
		return "", nil
	}
	return strings.TrimSpace(p.Email), nil
}

func istDate(s string) string {
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d.Format("02 Jan 2006")
	}
	return s
}

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

// invoicePDF fills the INVOICE_PDF_URL pattern ("" when not configured).
func (s *Service) invoicePDF(tenantID string, p events.InvoicePayload) string {
	if s.InvoicePDFURL == "" || p.InvoiceID == "" {
		return ""
	}
	return strings.NewReplacer("{tenant_id}", tenantID, "{invoice_id}", p.InvoiceID,
		"{number}", strings.NewReplacer("/", "-", " ", "").Replace(p.Number)).Replace(s.InvoicePDFURL)
}

func pdfName(number string) string {
	n := strings.NewReplacer("/", "-", "\\", "-", " ", "", "\"", "").Replace(number)
	if n == "" {
		n = "invoice"
	}
	return n + ".pdf"
}

// invoiceMessages: a sale with a phone → WhatsApp bill (with the PDF when
// INVOICE_PDF_URL is set); a party with an e-mail in bahi → e-mail with the PDF.
func (s *Service) invoiceMessages(ctx context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error) {
	var p events.InvoicePayload
	if err := e.Decode(&p); err != nil {
		return nil, err
	}
	if p.DocType != "sale" {
		return nil, nil
	}
	name := p.PartyName
	if name == "" {
		name = "Customer"
	}
	params := map[string]string{
		"party_name": name, "business": t.BusinessName, "number": p.Number,
		"amount": money.Format(p.GrandTotalPaise), "date": istDate(p.InvoiceDate),
	}
	pdf := s.invoicePDF(e.TenantID, p)
	var out []SendInput
	if p.PartyPhone != "" {
		out = append(out, SendInput{Channel: templates.WhatsApp, To: p.PartyPhone, Template: templates.Invoice, Params: params,
			MediaURL: pdf, MediaFilename: ifSet(pdf, pdfName(p.Number))})
	}
	email, err := s.partyEmail(ctx, e.TenantID, p.PartyID)
	if err != nil {
		return nil, err
	}
	if email != "" {
		out = append(out, SendInput{Channel: templates.Email, To: email, Template: templates.Invoice, Params: params,
			MediaURL: pdf, MediaFilename: ifSet(pdf, pdfName(p.Number))})
	}
	return out, nil
}

func ifSet(cond, v string) string {
	if cond == "" {
		return ""
	}
	return v
}

func (s *Service) paymentMessages(ctx context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error) {
	var p events.PaymentPayload
	if err := e.Decode(&p); err != nil {
		return nil, err
	}
	ref := p.GatewayRef
	if ref == "" {
		ref = p.PaymentID
	}
	params := map[string]string{"party_name": "Customer", "business": t.BusinessName, "amount": money.Format(p.AmountPaise), "ref": ref}
	var out []SendInput
	if p.PartyPhone != "" {
		out = append(out, SendInput{Channel: templates.WhatsApp, To: p.PartyPhone, Template: templates.PaymentReceipt, Params: params})
	}
	email, err := s.partyEmail(ctx, e.TenantID, p.PartyID)
	if err != nil {
		return nil, err
	}
	if email != "" {
		out = append(out, SendInput{Channel: templates.Email, To: email, Template: templates.PaymentReceipt, Params: params})
	}
	return out, nil
}

func stockLowMessage(_ context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error) {
	var p events.StockLowPayload
	if err := e.Decode(&p); err != nil {
		return nil, err
	}
	if t.OwnerPhone == "" {
		return nil, nil
	}
	return []SendInput{{Channel: templates.WhatsApp, To: t.OwnerPhone, Template: templates.StockLow, Params: map[string]string{
		"item": p.ItemName, "on_hand": fmtQty(p.OnHand), "reorder_level": fmtQty(p.ReorderLevel), "business": t.BusinessName,
	}}}, nil
}

func batchExpiringMessage(_ context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error) {
	var p events.BatchExpiringPayload
	if err := e.Decode(&p); err != nil {
		return nil, err
	}
	if t.OwnerPhone == "" {
		return nil, nil
	}
	return []SendInput{{Channel: templates.WhatsApp, To: t.OwnerPhone, Template: templates.BatchExpiring, Params: map[string]string{
		"item": p.ItemName, "batch": p.BatchNo, "expiry": istDate(p.ExpiryDate), "days": strconv.Itoa(p.DaysLeft),
		"qty": fmtQty(p.Qty), "business": t.BusinessName,
	}}}, nil
}

func importMessage(_ context.Context, e events.Envelope, t clients.Tenant) ([]SendInput, error) {
	var p events.ImportCompletedPayload
	if err := e.Decode(&p); err != nil {
		return nil, err
	}
	to := p.NotifyPhone
	if to == "" {
		to = t.OwnerPhone
	}
	if to == "" {
		return nil, nil
	}
	return []SendInput{{Channel: templates.WhatsApp, To: to, Template: templates.ImportDone, Params: map[string]string{
		"kind": p.Kind, "imported": strconv.Itoa(p.Imported), "total": strconv.Itoa(p.Total),
		"failed": fmt.Sprint(p.Failed),
	}}}, nil
}
