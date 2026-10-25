package invoice

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
)

// e-Invoice states (kar.einvoice.generated).
const (
	EInvoiceGenerated = "generated"
	EInvoiceCancelled = "cancelled"
	EInvoiceFailed    = "failed"
)

// e-Way bill states (kar.eway.generated).
var ewayLive = map[string]bool{"generated": true, "updated": true, "extended": true}

// eventTime parses the timestamps kar forwards: RFC3339 as agreed, plus the
// IRP / e-way portal formats ("2006-01-02 15:04:05", "02/01/2006 03:04:05 PM",
// both IST) in case a GSP driver passes them through unchanged.
func eventTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "02/01/2006 03:04:05 PM", "02/01/2006 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, IST); err == nil {
			return &t
		}
	}
	return nil
}

// lockForEvent marks the event processed and locks its invoice. ok=false means
// there is nothing to do (duplicate delivery, unknown or mismatched invoice).
func (s *Service) lockForEvent(ctx context.Context, tx pgx.Tx, e events.Envelope, invoiceID, number string, dest ...any) (bool, error) {
	if _, err := uuid.Parse(invoiceID); err != nil {
		s.Log.Warn("event for a malformed invoice id; ignored", "type", e.Type, "invoice_id", invoiceID)
		return false, nil
	}
	fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
	if err != nil || !fresh {
		return false, err
	}
	var gotNumber string
	err = tx.QueryRow(ctx, `SELECT number, einvoice_status, irn, ewb_status, ewb_no FROM invoices WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		e.TenantID, invoiceID).Scan(append([]any{&gotNumber}, dest...)...)
	if db.IsNotFound(err) {
		// kar works from aadesh's own events, so the invoice exists unless the
		// tenant is wrong: retrying would not help.
		s.Log.Warn("event for an unknown invoice; ignored", "type", e.Type, "invoice_id", invoiceID, "tenant_id", e.TenantID)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if number != "" && number != gotNumber {
		s.Log.Warn("event number does not match the invoice; ignored", "type", e.Type, "invoice_id", invoiceID, "event_number", number, "invoice_number", gotNumber)
		return false, nil
	}
	return true, nil
}

// ApplyEInvoice stores what the IRP returned for an invoice (idempotent on the
// event id): IRN, acknowledgement and signed QR when generated, the
// cancellation, or the IRP's error. A late "generated" for an IRN that was
// already cancelled is ignored; a failure (e.g. of a cancel attempt) never
// clears a live IRN — it is only recorded as the last error.
func (s *Service) ApplyEInvoice(ctx context.Context, e events.Envelope) error {
	var pl events.EInvoicePayload
	if err := e.Decode(&pl); err != nil || pl.InvoiceID == "" {
		return nil // malformed: nothing to retry
	}
	switch pl.Status {
	case EInvoiceGenerated, EInvoiceCancelled, EInvoiceFailed:
	default:
		s.Log.Warn("unknown e-invoice status; ignored", "status", pl.Status, "invoice_id", pl.InvoiceID)
		return nil
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status, irn, ewbStatus, ewbNo *string
		ok, err := s.lockForEvent(ctx, tx, e, pl.InvoiceID, pl.Number, &status, &irn, &ewbStatus, &ewbNo)
		if err != nil || !ok {
			return err
		}
		cur := deref(status)
		switch pl.Status {
		case EInvoiceGenerated:
			if pl.IRN == "" {
				s.Log.Warn("generated e-invoice without an IRN; ignored", "invoice_id", pl.InvoiceID)
				return nil
			}
			if cur == EInvoiceCancelled && deref(irn) == pl.IRN {
				return nil // out-of-order delivery: this IRN is already cancelled
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET irn=$3, ack_no=$4, ack_date=$5, signed_qr=$6, einvoice_status='generated',
				einvoice_error=NULL, einvoice_error_code=NULL, irn_cancelled_at=NULL WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, pl.IRN, nilIfEmpty(pl.AckNo), eventTime(pl.AckDate), nilIfEmpty(pl.SignedQR))
		case EInvoiceCancelled:
			at := eventTime(pl.CancelledAt)
			if at == nil {
				now := e.OccurredAt
				if now.IsZero() {
					now = s.Now()
				}
				at = &now
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET einvoice_status='cancelled', irn=COALESCE($3, irn), irn_cancelled_at=$4,
				einvoice_error=NULL, einvoice_error_code=NULL WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, nilIfEmpty(pl.IRN), at)
		case EInvoiceFailed:
			newStatus := EInvoiceFailed
			if cur == EInvoiceGenerated || cur == EInvoiceCancelled {
				newStatus = cur // keep the live (or cancelled) IRN; just record the error
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET einvoice_status=$3, einvoice_error=$4, einvoice_error_code=$5 WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, newStatus, nilIfEmpty(pl.Error), nilIfEmpty(pl.ErrorCode))
		}
		return err
	})
}

// ApplyEWay stores an e-way bill generated, updated (Part-B), extended,
// cancelled or failed for an invoice (idempotent on the event id). A
// cancellation of an older e-way bill number does not touch a newer one, and a
// failure never clears a live e-way bill.
func (s *Service) ApplyEWay(ctx context.Context, e events.Envelope) error {
	var pl events.EWayPayload
	if err := e.Decode(&pl); err != nil || pl.InvoiceID == "" {
		return nil
	}
	if !ewayLive[pl.Status] && pl.Status != "cancelled" && pl.Status != "failed" {
		s.Log.Warn("unknown e-way status; ignored", "status", pl.Status, "invoice_id", pl.InvoiceID)
		return nil
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status, irn, ewbStatus, ewbNo *string
		ok, err := s.lockForEvent(ctx, tx, e, pl.InvoiceID, pl.Number, &status, &irn, &ewbStatus, &ewbNo)
		if err != nil || !ok {
			return err
		}
		cur, curNo := deref(ewbStatus), deref(ewbNo)
		switch {
		case ewayLive[pl.Status]:
			if pl.EWBNo == "" {
				s.Log.Warn("e-way event without a bill number; ignored", "invoice_id", pl.InvoiceID)
				return nil
			}
			if cur == "cancelled" && curNo == pl.EWBNo {
				return nil // out-of-order delivery: this bill is already cancelled
			}
			var km *int
			if pl.DistanceKM > 0 {
				km = &pl.DistanceKM
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET ewb_no=$3,
				ewb_date=CASE WHEN ewb_no IS DISTINCT FROM $3 THEN $4 ELSE COALESCE($4, ewb_date) END,
				ewb_valid_upto=COALESCE($5, ewb_valid_upto), ewb_status=$6, ewb_error=NULL,
				ewb_vehicle_no=COALESCE($7, ewb_vehicle_no), ewb_distance_km=COALESCE($8, ewb_distance_km)
				WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, pl.EWBNo, eventTime(pl.EWBDate), eventTime(pl.ValidUpto), pl.Status,
				nilIfEmpty(pl.VehicleNo), km)
		case pl.Status == "cancelled":
			if pl.EWBNo != "" && curNo != "" && pl.EWBNo != curNo {
				return nil // an older bill; the current one stays
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET ewb_no=COALESCE(ewb_no, $3), ewb_status='cancelled', ewb_error=NULL WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, nilIfEmpty(pl.EWBNo))
		default: // failed
			newStatus := "failed"
			if ewayLive[cur] || cur == "cancelled" {
				newStatus = cur
			}
			_, err = tx.Exec(ctx, `UPDATE invoices SET ewb_status=$3, ewb_error=$4 WHERE tenant_id=$1 AND id=$2`,
				e.TenantID, pl.InvoiceID, newStatus, nilIfEmpty(pl.Error))
		}
		return err
	})
}
