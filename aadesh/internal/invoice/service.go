package invoice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/aadesh/internal/clients"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Service implements invoice use cases.
type Service struct {
	DB    *pgxpool.Pool
	Dir   *clients.Directory
	Relay *events.Relay
	Log   *slog.Logger
	Now   func() time.Time
}

// Create validates, prices, numbers and stores an invoice, and queues its event.
// created=false means client_uuid matched an existing invoice (safe retry / offline re-sync).
func (s *Service) Create(ctx context.Context, p *auth.Principal, in CreateInput) (inv *Invoice, created bool, err error) {
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID == "" {
		return nil, false, httpx.Validation("store_id is required")
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return nil, false, httpx.Forbidden("no access to store")
	}

	// Fast path for retries: return the existing invoice before calling other services.
	if in.ClientUUID != "" {
		if existing, err := s.byClientUUID(ctx, p.TenantID, in.ClientUUID); err == nil {
			return existing, false, nil
		} else if !db.IsNotFound(err) {
			return nil, false, err
		}
	}

	store, err := s.Dir.Store(ctx, p.TenantID, storeID)
	if err != nil {
		return nil, false, err
	}

	party, err := s.resolveParty(ctx, in)
	if err != nil {
		return nil, false, err
	}
	pos := in.PlaceOfSupply
	if pos == "" {
		pos = party.StateCode
	}
	if pos == "" {
		pos = store.StateCode
	}

	now := s.Now()
	date := now.In(IST)
	if in.InvoiceDate != "" {
		date, _ = time.ParseInLocation(time.DateOnly, in.InvoiceDate, IST)
	}
	fy := FinancialYear(date)

	lines := make([]gst.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = l.Line
	}
	roundOff := in.RoundOff == nil || *in.RoundOff
	sum, err := gst.Compute(lines, store.StateCode, pos, roundOff)
	if err != nil {
		return nil, false, httpx.Validation("%s", err.Error())
	}

	var paid int64
	for _, t := range in.Tenders {
		if t.Mode != "credit" {
			paid += t.AmountPaise
		}
	}
	if paid > sum.GrandPaise {
		// Cash tendered above the bill is change returned, not revenue.
		paid = sum.GrandPaise
	}

	inv = &Invoice{
		StoreID: storeID, DocType: in.DocType, FY: fy, InvoiceDate: date.Format(time.DateOnly),
		PartyName: party.Name, StoreState: store.StateCode, PlaceOfSupply: pos, InterState: sum.InterState,
		Status: "posted", PaymentStatus: paymentStatus(paid, sum.GrandPaise),
		TaxablePaise: sum.TaxablePaise, DiscountPaise: sum.DiscountPaise, CGSTPaise: sum.CGSTPaise,
		SGSTPaise: sum.SGSTPaise, IGSTPaise: sum.IGSTPaise, CessPaise: sum.CessPaise,
		RoundOffPaise: sum.RoundOffPaise, GrandTotalPaise: sum.GrandPaise, PaidPaise: paid,
		AmountInWords: gst.AmountInWords(sum.GrandPaise), CreatedBy: p.UserID(), CreatedAt: now,
		PartyID: nilIfEmpty(party.ID), PartyPhone: nilIfEmpty(party.Phone), PartyGSTIN: nilIfEmpty(party.GSTIN),
		PartyState: nilIfEmpty(party.StateCode), SupplierInvoiceNo: nilIfEmpty(in.SupplierInvoiceNo),
		RefInvoiceID: nilIfEmpty(in.RefInvoiceID), Notes: nilIfEmpty(in.Notes), ClientUUID: nilIfEmpty(in.ClientUUID),
		Tenders: in.Tenders,
	}
	for i, l := range in.Lines {
		lt := sum.Lines[i]
		unit := l.Unit
		if unit == "" {
			unit = "PCS"
		}
		inv.Lines = append(inv.Lines, Line{
			LineNo: i + 1, ItemID: nilIfEmpty(l.ItemID), Name: strings.TrimSpace(l.Name), HSN: nilIfEmpty(l.HSN),
			Unit: unit, BatchNo: nilIfEmpty(l.BatchNo), Qty: l.Qty, UnitPricePaise: l.UnitPricePaise,
			DiscountPaise: l.DiscountPaise, TaxRateBP: l.TaxRateBP, CessRateBP: l.CessRateBP,
			PriceIncludesTax: l.PriceIncludesTax, TaxablePaise: lt.TaxablePaise, CGSTPaise: lt.CGSTPaise,
			SGSTPaise: lt.SGSTPaise, IGSTPaise: lt.IGSTPaise, CessPaise: lt.CessPaise, TotalPaise: lt.TotalPaise,
		})
	}

	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if IsNote(in.DocType) {
			if err := s.checkRef(ctx, tx, p.TenantID, in.RefInvoiceID, in.DocType); err != nil {
				return err
			}
		}
		seq, prefix, err := s.allocateNumber(ctx, tx, p.TenantID, storeID, in, fy)
		if err != nil {
			return err
		}
		inv.Number = fmt.Sprintf("%s/%s/%05d", prefix, fy, seq)
		if err := insertInvoice(ctx, tx, p.TenantID, seq, in, inv); err != nil {
			return err
		}
		evType := events.InvoiceCreated
		if IsNote(in.DocType) {
			evType = events.NoteCreated
		}
		ev, err := events.New(evType, "aadesh", p.TenantID, storeID, toPayload(inv))
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		if db.IsUniqueViolation(err) && in.ClientUUID != "" {
			// A concurrent sync of the same bill won the race; return that one.
			if existing, e2 := s.byClientUUID(ctx, p.TenantID, in.ClientUUID); e2 == nil {
				return existing, false, nil
			}
		}
		if db.IsUniqueViolation(err) {
			return nil, false, httpx.Conflict("invoice number already used")
		}
		return nil, false, err
	}
	s.kick()
	return inv, true, nil
}

func (s *Service) kick() {
	if s.Relay != nil {
		s.Relay.Kick()
	}
}

func (s *Service) resolveParty(ctx context.Context, in CreateInput) (clients.Party, error) {
	if in.PartyID != "" {
		return s.Dir.Party(ctx, in.PartyID)
	}
	if in.Party != nil && strings.TrimSpace(in.Party.Name) != "" {
		state := in.Party.StateCode
		if state == "" && in.Party.GSTIN != "" {
			state = gst.StateCode(in.Party.GSTIN)
		}
		return clients.Party{Name: strings.TrimSpace(in.Party.Name), Phone: in.Party.Phone,
			GSTIN: gst.Normalize(in.Party.GSTIN), StateCode: state}, nil
	}
	return clients.Party{Name: "Cash Sale"}, nil
}

func (s *Service) checkRef(ctx context.Context, tx pgx.Tx, tenantID, refID, docType string) error {
	var refType, status string
	err := tx.QueryRow(ctx, `SELECT doc_type, status FROM invoices WHERE tenant_id=$1 AND id=$2`, tenantID, refID).Scan(&refType, &status)
	if db.IsNotFound(err) {
		return httpx.Validation("ref_invoice_id not found")
	}
	if err != nil {
		return err
	}
	want := DocSale
	if docType == DocPurchaseReturn {
		want = DocPurchase
	}
	if refType != want || status != "posted" {
		return httpx.Validation("ref_invoice_id must be a posted %s invoice", want)
	}
	return nil
}

// allocateNumber returns the sequence number for the invoice. Online bills take
// the next number; offline bills bring a number from a block reserved for their device.
func (s *Service) allocateNumber(ctx context.Context, tx pgx.Tx, tenantID, storeID string, in CreateInput, fy string) (int64, string, error) {
	if in.Number > 0 {
		var prefix string
		err := tx.QueryRow(ctx, `
			SELECT ns.prefix FROM number_blocks b
			JOIN number_series ns USING (tenant_id, store_id, doc_type, fy)
			WHERE b.tenant_id=$1 AND b.store_id=$2 AND b.doc_type=$3 AND b.fy=$4 AND b.device_id=$5
			  AND $6 BETWEEN b.start_no AND b.end_no LIMIT 1`,
			tenantID, storeID, in.DocType, fy, in.DeviceID, in.Number).Scan(&prefix)
		if db.IsNotFound(err) {
			return 0, "", httpx.Validation("number %d is not in a block reserved for device %s", in.Number, in.DeviceID)
		}
		return in.Number, prefix, err
	}
	var next int64
	var prefix string
	err := tx.QueryRow(ctx, `
		INSERT INTO number_series (tenant_id, store_id, doc_type, fy, prefix, next_no)
		VALUES ($1,$2,$3,$4,$5,2)
		ON CONFLICT (tenant_id, store_id, doc_type, fy) DO UPDATE SET next_no = number_series.next_no + 1
		RETURNING next_no - 1, prefix`,
		tenantID, storeID, in.DocType, fy, defaultPrefix[in.DocType]).Scan(&next, &prefix)
	return next, prefix, err
}

// Block is a reserved range of numbers for an offline device.
type Block struct {
	DocType  string `json:"doc_type"`
	FY       string `json:"fy"`
	Prefix   string `json:"prefix"`
	DeviceID string `json:"device_id"`
	StartNo  int64  `json:"start_no"`
	EndNo    int64  `json:"end_no"`
}

// ReserveBlock hands a device `size` consecutive numbers for offline billing.
func (s *Service) ReserveBlock(ctx context.Context, p *auth.Principal, storeID, docType, deviceID string, size int64) (*Block, error) {
	if storeID == "" {
		storeID = p.StoreID
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	if _, ok := defaultPrefix[docType]; !ok || deviceID == "" || size < 1 || size > 1000 {
		return nil, httpx.Validation("need doc_type, device_id and size between 1 and 1000")
	}
	fy := FinancialYear(s.Now().In(IST))
	b := &Block{DocType: docType, FY: fy, DeviceID: deviceID}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO number_series (tenant_id, store_id, doc_type, fy, prefix, next_no)
			VALUES ($1,$2,$3,$4,$5,$6+1)
			ON CONFLICT (tenant_id, store_id, doc_type, fy) DO UPDATE SET next_no = number_series.next_no + $6
			RETURNING next_no - $6, prefix`,
			p.TenantID, storeID, docType, fy, defaultPrefix[docType], size).Scan(&b.StartNo, &b.Prefix)
		if err != nil {
			return err
		}
		b.EndNo = b.StartNo + size - 1
		_, err = tx.Exec(ctx, `INSERT INTO number_blocks (tenant_id, store_id, doc_type, fy, device_id, start_no, end_no)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, storeID, docType, fy, deviceID, b.StartNo, b.EndNo)
		return err
	})
	return b, err
}

// Cancel marks a posted invoice cancelled and queues the reversal event.
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, id, reason string) (*Invoice, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, httpx.Validation("reason is required")
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invoices SET status='cancelled', cancelled_at=now(), cancel_reason=$3
			WHERE tenant_id=$1 AND id=$2 AND status='posted'`, p.TenantID, id, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("invoice not found or already cancelled")
		}
		inv, err := getInvoice(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		ev, err := events.New(events.InvoiceCancelled, "aadesh", p.TenantID, inv.StoreID, toPayload(inv))
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.kick()
	return s.Get(ctx, p.TenantID, id)
}

// Get loads one invoice with lines and tenders.
func (s *Service) Get(ctx context.Context, tenantID, id string) (*Invoice, error) {
	inv, err := getInvoice(ctx, s.DB, tenantID, id)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("invoice not found")
	}
	return inv, err
}

func (s *Service) byClientUUID(ctx context.Context, tenantID, clientUUID string) (*Invoice, error) {
	var id string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM invoices WHERE tenant_id=$1 AND client_uuid=$2`, tenantID, clientUUID).Scan(&id); err != nil {
		return nil, err
	}
	return getInvoice(ctx, s.DB, tenantID, id)
}

// ApplyPayment records a lenden payment against an invoice (idempotent on payment id).
func (s *Service) ApplyPayment(ctx context.Context, e events.Envelope, refund bool) error {
	var pl events.PaymentPayload
	if err := e.Decode(&pl); err != nil {
		return nil // malformed: nothing to retry
	}
	if pl.InvoiceID == "" {
		return nil // on-account payment, nothing to update here
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		delta := pl.AmountPaise
		if refund {
			// Each refund event (deduped above by event id) reduces the receipt; partial refunds add up.
			tag, err := tx.Exec(ctx, `UPDATE invoice_receipts SET refunded_paise = refunded_paise + $2
				WHERE payment_id=$1 AND refunded_paise + $2 <= amount_paise`, pl.PaymentID, pl.AmountPaise)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			delta = -delta
		} else {
			tag, err := tx.Exec(ctx, `INSERT INTO invoice_receipts (payment_id, invoice_id, amount_paise)
				SELECT $1, id, $3 FROM invoices WHERE tenant_id=$4 AND id=$2 ON CONFLICT DO NOTHING`,
				pl.PaymentID, pl.InvoiceID, pl.AmountPaise, e.TenantID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE invoices SET
				paid_paise = GREATEST(0, paid_paise + $3),
				payment_status = CASE WHEN paid_paise + $3 >= grand_total_paise THEN 'paid'
				                      WHEN paid_paise + $3 > 0 THEN 'partial' ELSE 'unpaid' END
			WHERE tenant_id=$1 AND id=$2`, e.TenantID, pl.InvoiceID, delta)
		return err
	})
}

func paymentStatus(paid, total int64) string {
	switch {
	case paid >= total:
		return "paid"
	case paid > 0:
		return "partial"
	}
	return "unpaid"
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toPayload(inv *Invoice) events.InvoicePayload {
	p := events.InvoicePayload{
		InvoiceID: inv.ID, StoreID: inv.StoreID, DocType: inv.DocType, Number: inv.Number,
		InvoiceDate: inv.InvoiceDate, RefInvoiceID: deref(inv.RefInvoiceID), PartyID: deref(inv.PartyID),
		PartyName: inv.PartyName, PartyPhone: deref(inv.PartyPhone), PartyGSTIN: deref(inv.PartyGSTIN),
		PlaceOfSupply: inv.PlaceOfSupply, InterState: inv.InterState, TaxablePaise: inv.TaxablePaise,
		CGSTPaise: inv.CGSTPaise, SGSTPaise: inv.SGSTPaise, IGSTPaise: inv.IGSTPaise, CessPaise: inv.CessPaise,
		RoundOffPaise: inv.RoundOffPaise, GrandTotalPaise: inv.GrandTotalPaise, CreatedBy: inv.CreatedBy,
	}
	for _, l := range inv.Lines {
		p.Lines = append(p.Lines, events.InvoiceLine{
			LineNo: l.LineNo, ItemID: deref(l.ItemID), Name: l.Name, HSN: deref(l.HSN), Unit: l.Unit,
			BatchNo: deref(l.BatchNo), Qty: l.Qty, UnitPricePaise: l.UnitPricePaise, DiscountPaise: l.DiscountPaise,
			PriceInclTax: l.PriceIncludesTax, TaxRateBP: l.TaxRateBP,
			TaxablePaise: l.TaxablePaise, CGSTPaise: l.CGSTPaise, SGSTPaise: l.SGSTPaise, IGSTPaise: l.IGSTPaise,
			CessPaise: l.CessPaise, TotalPaise: l.TotalPaise,
		})
	}
	for _, t := range inv.Tenders {
		p.Tenders = append(p.Tenders, events.Tender{Mode: t.Mode, AmountPaise: t.AmountPaise, Reference: t.Reference})
	}
	return p
}
