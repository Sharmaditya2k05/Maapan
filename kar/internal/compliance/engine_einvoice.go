package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// e-invoice statuses (einvoices.status).
const (
	EInvDraft     = "draft"
	EInvPending   = "pending" // a transient failure; will be retried
	EInvGenerated = "generated"
	EInvCancelled = "cancelled"
	EInvFailed    = "failed"
)

// CancelWindow is how long after the acknowledgement an IRN may be cancelled.
const CancelWindow = 24 * time.Hour

// EInvoiceRecord is one invoice's e-invoice state.
type EInvoiceRecord struct {
	InvoiceID      string          `json:"invoice_id"`
	InvoiceNumber  string          `json:"invoice_number"`
	StoreID        string          `json:"store_id,omitempty"`
	GSTIN          string          `json:"gstin"`
	DocType        string          `json:"doc_type"`
	DocDate        string          `json:"doc_date,omitempty"`
	PartyGSTIN     string          `json:"party_gstin,omitempty"`
	PartyName      string          `json:"party_name,omitempty"`
	SupplyType     string          `json:"supply_type,omitempty"`
	TotalPaise     int64           `json:"total_paise"`
	Status         string          `json:"status"`
	IRN            string          `json:"irn,omitempty"`
	AckNo          string          `json:"ack_no,omitempty"`
	AckDate        *time.Time      `json:"ack_date,omitempty"`
	SignedQR       string          `json:"signed_qr,omitempty"`
	SignedInvoice  string          `json:"signed_invoice,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	Attempts       int             `json:"attempts"`
	Provider       string          `json:"provider,omitempty"`
	TriggeredBy    string          `json:"triggered_by"`
	CancelledAt    *time.Time      `json:"cancelled_at,omitempty"`
	CancelReason   *int            `json:"cancel_reason,omitempty"`
	CancelRemark   string          `json:"cancel_remark,omitempty"`
	CancelUntil    *time.Time      `json:"cancellable_until,omitempty"`
	CreatedBy      string          `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	QR             *gsp.QRData     `json:"qr,omitempty"` // decoded signed QR, for printing
	invoiceStoreID string
}

const einvCols = `invoice_id, invoice_number, COALESCE(store_id::text,''), gstin, doc_type, COALESCE(to_char(doc_date,'YYYY-MM-DD'),''),
	party_gstin, party_name, supply_type, total_paise, status, COALESCE(irn,''), COALESCE(ack_no,''), ack_date,
	COALESCE(signed_qr,''), COALESCE(signed_invoice,''), COALESCE(error_code,''), COALESCE(error_message,''), attempts,
	provider, triggered_by, cancelled_at, cancel_reason, COALESCE(cancel_remark,''), created_by, created_at, updated_at, payload`

func scanEInv(row pgx.Row) (EInvoiceRecord, error) {
	var r EInvoiceRecord
	err := row.Scan(&r.InvoiceID, &r.InvoiceNumber, &r.StoreID, &r.GSTIN, &r.DocType, &r.DocDate, &r.PartyGSTIN, &r.PartyName,
		&r.SupplyType, &r.TotalPaise, &r.Status, &r.IRN, &r.AckNo, &r.AckDate, &r.SignedQR, &r.SignedInvoice, &r.ErrorCode,
		&r.ErrorMessage, &r.Attempts, &r.Provider, &r.TriggeredBy, &r.CancelledAt, &r.CancelReason, &r.CancelRemark,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.Payload)
	if r.Status == EInvGenerated && r.AckDate != nil {
		u := r.AckDate.Add(CancelWindow)
		r.CancelUntil = &u
	}
	if r.SignedQR != "" {
		if q, err := gsp.ParseSignedQR(r.SignedQR); err == nil {
			r.QR = &q
		}
	}
	if string(r.Payload) == "{}" {
		r.Payload = nil
	}
	return r, err
}

// EInvoice returns the stored e-invoice of an invoice.
func (e *Engine) EInvoice(ctx context.Context, tenantID, invoiceID string) (EInvoiceRecord, bool, error) {
	r, err := scanEInv(e.DB.QueryRow(ctx, `SELECT `+einvCols+` FROM einvoices WHERE tenant_id=$1 AND invoice_id=$2`, tenantID, invoiceID))
	if db.IsNotFound(err) {
		return r, false, nil
	}
	return r, err == nil, err
}

// EInvoiceFilter narrows ListEInvoices.
type EInvoiceFilter struct {
	Status, From, To, GSTIN string
	StoreIDs                []string // nil = all stores
	Limit, Offset           int
}

// ListEInvoices lists e-invoices for the GST centre (newest documents first).
func (e *Engine) ListEInvoices(ctx context.Context, tenantID string, f EInvoiceFilter) ([]EInvoiceRecord, int, error) {
	where := []string{"tenant_id=$1", "status <> 'draft'"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.From != "" {
		add("doc_date >= ?::date", f.From)
	}
	if f.To != "" {
		add("doc_date <= ?::date", f.To)
	}
	if f.GSTIN != "" {
		add("gstin = ?", strings.ToUpper(f.GSTIN))
	}
	if f.StoreIDs != nil {
		add("store_id::text = ANY(?)", f.StoreIDs)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM einvoices WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := e.DB.Query(ctx, `SELECT `+einvCols+` FROM einvoices WHERE `+w+
		fmt.Sprintf(` ORDER BY doc_date DESC NULLS LAST, invoice_number DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []EInvoiceRecord{}
	for rows.Next() {
		r, err := scanEInv(rows)
		if err != nil {
			return nil, 0, err
		}
		r.Payload, r.SignedInvoice = nil, ""
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ErrNotApplicable is returned when an invoice need not (or cannot) be e-invoiced.
var ErrNotApplicable = errors.New("e-invoice not applicable")

// NotApplicableError explains why.
type NotApplicableError struct{ Reason string }

func (n *NotApplicableError) Error() string   { return "e-invoice not applicable: " + n.Reason }
func (n *NotApplicableError) Is(t error) bool { return t == ErrNotApplicable }

// GenerateRequest is one generate attempt.
type GenerateRequest struct {
	TenantID string
	Invoice  clients.Invoice
	By       string // user id or "system"
	Trigger  string // manual | event
	EventID  string // marks processed_events in the same transaction
	EventTyp string
	Export   *EInvExport
}

// BuildFor builds the IRP payload for an invoice (also used by the download endpoint).
func (e *Engine) BuildFor(ctx context.Context, tenantID string, inv clients.Invoice, export *EInvExport) (EInvoice, Seller, error) {
	s, err := e.seller(ctx, tenantID, inv.StoreID)
	if err != nil {
		return EInvoice{}, s, err
	}
	buyer := e.Counterparty(ctx, inv)
	opts := EInvoiceOptions{Export: export}
	if inv.RefInvoiceID != "" {
		o, err := e.Dir.Invoice(ctx, inv.RefInvoiceID)
		if err != nil {
			return EInvoice{}, s, fmt.Errorf("original invoice %s: %w", inv.RefInvoiceID, err)
		}
		opts.Original = &o
	}
	p, err := BuildEInvoiceWith(inv, s.Party, buyer, opts)
	if err != nil {
		return EInvoice{}, s, httpx.Validation("%s", err.Error())
	}
	return p, s, nil
}

// GenerateEInvoice registers an invoice on the IRP and records the outcome.
// It is idempotent: an invoice that already has an IRN returns it. Outcomes
// generated / failed publish kar.einvoice.generated through the outbox.
// Transient failures (GSP down) leave the row pending and return a 503-class
// error until MaxAttempts, then the row fails for good.
func (e *Engine) GenerateEInvoice(ctx context.Context, req GenerateRequest) (EInvoiceRecord, error) {
	inv := req.Invoice
	if req.Trigger == "" {
		req.Trigger = "manual"
	}
	cur, exists, err := e.EInvoice(ctx, req.TenantID, inv.ID)
	if err != nil {
		return cur, err
	}
	if exists {
		switch cur.Status {
		case EInvGenerated:
			return cur, e.markOnly(ctx, req)
		case EInvCancelled:
			return cur, httpx.Conflict("the IRN of %s was cancelled; the IRP does not accept the same document number again — issue a new invoice", inv.Number)
		case EInvFailed:
			if req.Trigger == "event" {
				return cur, e.markOnly(ctx, req) // a redelivered event never retries a failed document
			}
		}
	}

	payload, seller, err := e.BuildFor(ctx, req.TenantID, inv, req.Export)
	if err != nil && !isValidation(err) {
		return e.transientFailure(ctx, req, seller, nil, err)
	}
	if applicable, reason := EInvoiceApplicable(seller.Applicable, inv); !applicable {
		if req.EventID != "" {
			if merr := e.markOnly(ctx, req); merr != nil {
				return EInvoiceRecord{}, merr
			}
		}
		return EInvoiceRecord{}, &NotApplicableError{Reason: reason}
	}
	if err != nil {
		return e.finish(ctx, req, seller, nil, nil, err)
	}
	creds, err := e.credentials(ctx, req.TenantID, seller.GSTIN)
	if err != nil {
		return e.transientFailure(ctx, req, seller, &payload, err)
	}
	res, gerr := e.GSP.GenerateIRN(ctx, creds, payload)
	if gerr != nil {
		ge, _ := gsp.AsError(gerr)
		switch {
		case ge != nil && ge.Kind == gsp.KindDuplicate && ge.Existing != nil:
			res, gerr = *ge.Existing, nil
		case ge != nil && ge.Kind == gsp.KindDuplicate:
			res, gerr = e.GSP.GetIRNByDocument(ctx, creds, gsp.DocumentRef{DocType: payload.DocDtls.Typ,
				DocNo: payload.DocDtls.No, DocDate: payload.DocDtls.Dt})
		}
	}
	if gerr != nil && transient(gerr) {
		return e.transientFailure(ctx, req, seller, &payload, gerr)
	}
	if gerr == nil && res.Status == gsp.IRNCancelled {
		gerr = &gsp.Error{Kind: gsp.KindNotAllowed, Code: gsp.CodeIRNNotActive,
			Message: "this document number was registered and cancelled on the IRP; issue a new invoice"}
	}
	return e.finish(ctx, req, seller, &payload, &res, gerr)
}

func isValidation(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Status == 422
}

// markOnly records an event as processed without other changes.
func (e *Engine) markOnly(ctx context.Context, req GenerateRequest) error {
	if req.EventID == "" {
		return nil
	}
	return db.WithTx(ctx, e.DB, func(tx pgx.Tx) error {
		_, err := db.MarkProcessed(ctx, tx, req.EventID, req.EventTyp)
		return err
	})
}

// upsertEInv writes the row; a generated row is never overwritten by a failure.
func upsertEInv(ctx context.Context, tx pgx.Tx, tenantID string, r EInvoiceRecord, payload []byte) error {
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	_, err := tx.Exec(ctx, `INSERT INTO einvoices (tenant_id, invoice_id, invoice_number, payload, created_by, store_id, gstin,
			doc_type, doc_date, party_gstin, party_name, supply_type, total_paise, status, irn, ack_no, ack_date, signed_invoice,
			signed_qr, error_code, error_message, attempts, provider, triggered_by, updated_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,NULLIF($9,'')::date,$10,$11,$12,$13,$14,NULLIF($15,''),NULLIF($16,''),$17,
			NULLIF($18,''),NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),$22,$23,$24,now())
		ON CONFLICT (tenant_id, invoice_id) DO UPDATE SET invoice_number=EXCLUDED.invoice_number,
			payload=CASE WHEN EXCLUDED.payload = '{}'::jsonb THEN einvoices.payload ELSE EXCLUDED.payload END,
			store_id=EXCLUDED.store_id, gstin=EXCLUDED.gstin, doc_type=EXCLUDED.doc_type, doc_date=EXCLUDED.doc_date,
			party_gstin=EXCLUDED.party_gstin, party_name=EXCLUDED.party_name, supply_type=EXCLUDED.supply_type,
			total_paise=EXCLUDED.total_paise, status=EXCLUDED.status, irn=EXCLUDED.irn, ack_no=EXCLUDED.ack_no,
			ack_date=EXCLUDED.ack_date, signed_invoice=EXCLUDED.signed_invoice, signed_qr=EXCLUDED.signed_qr,
			error_code=EXCLUDED.error_code, error_message=EXCLUDED.error_message, attempts=EXCLUDED.attempts,
			provider=EXCLUDED.provider, triggered_by=EXCLUDED.triggered_by, updated_at=now()
		WHERE einvoices.status NOT IN ('generated','cancelled')`,
		tenantID, r.InvoiceID, r.InvoiceNumber, payload, r.CreatedBy, r.StoreID, r.GSTIN, r.DocType, r.DocDate, r.PartyGSTIN,
		r.PartyName, r.SupplyType, r.TotalPaise, r.Status, r.IRN, r.AckNo, r.AckDate, r.SignedInvoice, r.SignedQR, r.ErrorCode,
		r.ErrorMessage, r.Attempts, r.Provider, r.TriggeredBy)
	return err
}

func (e *Engine) baseRecord(req GenerateRequest, seller Seller, payload *EInvoice) EInvoiceRecord {
	inv := req.Invoice
	r := EInvoiceRecord{InvoiceID: inv.ID, InvoiceNumber: inv.Number, StoreID: inv.StoreID, GSTIN: seller.GSTIN,
		DocType: IRPDocType(inv.DocType), DocDate: inv.InvoiceDate, PartyGSTIN: inv.PartyGSTIN, PartyName: inv.PartyName,
		TotalPaise: inv.GrandTotalPaise, Provider: e.GSP.Name(), TriggeredBy: req.Trigger, CreatedBy: req.By}
	if r.GSTIN == "" {
		r.GSTIN = strings.ToUpper(inv.StoreGSTIN)
	}
	if st, err := SupplyType(inv.SupplyCategory); err == nil {
		r.SupplyType = st
	}
	if payload != nil {
		r.SupplyType = payload.TranDtls.SupTyp
	}
	return r
}

// transientFailure counts an attempt; past MaxAttempts it becomes a permanent failure.
func (e *Engine) transientFailure(ctx context.Context, req GenerateRequest, seller Seller, payload *EInvoice, cause error) (EInvoiceRecord, error) {
	cur, _, err := e.EInvoice(ctx, req.TenantID, req.Invoice.ID)
	if err != nil {
		return cur, err
	}
	if req.Trigger == "manual" && cur.Status == EInvFailed {
		cur.Attempts = 0 // a person retrying a failed document gets a fresh budget
	}
	attempts := cur.Attempts + 1
	if attempts >= e.maxAttempts() {
		e.log().Error("e-invoice failed after retries", "invoice_id", req.Invoice.ID, "attempts", attempts, "error", cause.Error())
		r, ferr := e.finish(ctx, req, seller, payload, nil, cause, attempts)
		if ferr != nil && !transient(ferr) {
			return r, ferr
		}
		return r, gspHTTPError(cause)
	}
	r := e.baseRecord(req, seller, payload)
	r.Status, r.Attempts = EInvPending, attempts
	r.ErrorCode, r.ErrorMessage = errorCode(cause)
	var pb []byte
	if payload != nil {
		pb, _ = json.Marshal(payload)
	}
	if err := db.WithTx(ctx, e.DB, func(tx pgx.Tx) error { return upsertEInv(ctx, tx, req.TenantID, r, pb) }); err != nil {
		return r, err
	}
	e.log().Warn("e-invoice attempt failed, will retry", "invoice_id", req.Invoice.ID, "attempts", attempts, "error", cause.Error())
	if he := gspHTTPError(cause); he != cause {
		return r, he
	}
	return r, httpx.Unavailable("e-invoice could not be generated now (%s); it will be retried", r.ErrorMessage)
}

// finish stores a generated or failed outcome, publishes the event and marks
// the triggering event processed — all in one transaction.
func (e *Engine) finish(ctx context.Context, req GenerateRequest, seller Seller, payload *EInvoice, res *gsp.IRNResult, cause error, attempts ...int) (EInvoiceRecord, error) {
	r := e.baseRecord(req, seller, payload)
	if len(attempts) > 0 {
		r.Attempts = attempts[0]
	}
	ev := events.EInvoicePayload{InvoiceID: r.InvoiceID, Number: r.InvoiceNumber, GSTIN: r.GSTIN, GeneratedVia: r.Provider}
	if cause == nil && res != nil {
		r.Status, r.IRN, r.AckNo, r.AckDate = EInvGenerated, res.IRN, res.AckNo, timePtr(res.AckDt)
		r.SignedQR, r.SignedInvoice = res.SignedQR, res.SignedInvoice
		ev.Status, ev.IRN, ev.AckNo, ev.AckDate, ev.SignedQR = EInvGenerated, res.IRN, res.AckNo, rfc3339(r.AckDate), res.SignedQR
	} else {
		r.Status = EInvFailed
		r.ErrorCode, r.ErrorMessage = errorCode(cause)
		ev.Status, ev.Error, ev.ErrorCode = EInvFailed, r.ErrorMessage, r.ErrorCode
	}
	var pb []byte
	if payload != nil {
		pb, _ = json.Marshal(payload)
	}
	err := db.WithTx(ctx, e.DB, func(tx pgx.Tx) error {
		if req.EventID != "" {
			fresh, err := db.MarkProcessed(ctx, tx, req.EventID, req.EventTyp)
			if err != nil || !fresh {
				return err
			}
		}
		if err := upsertEInv(ctx, tx, req.TenantID, r, pb); err != nil {
			return err
		}
		return enqueue(ctx, tx, events.EInvoiceGenerated, req.TenantID, r.StoreID, ev)
	})
	if err != nil {
		return r, err
	}
	e.kick()
	stored, _, err := e.EInvoice(ctx, req.TenantID, r.InvoiceID)
	if err != nil {
		return r, err
	}
	if r.Status == EInvFailed {
		e.log().Warn("e-invoice failed", "invoice_id", r.InvoiceID, "code", r.ErrorCode, "error", r.ErrorMessage)
		if he := gspHTTPError(cause); he != cause {
			return stored, he
		}
		return stored, cause
	}
	return stored, nil
}

// CancelEInvoice cancels a generated IRN within 24 hours of its acknowledgement.
func (e *Engine) CancelEInvoice(ctx context.Context, tenantID, invoiceID string, reason int, remark, by string) (EInvoiceRecord, error) {
	if _, ok := gsp.IRNCancelReasons[reason]; !ok {
		return EInvoiceRecord{}, httpx.Validation("reason_code must be 1 (duplicate), 2 (data entry mistake), 3 (order cancelled) or 4 (others)")
	}
	remark = strings.TrimSpace(remark)
	if remark == "" || len([]rune(remark)) > 100 {
		return EInvoiceRecord{}, httpx.Validation("remark is required (at most 100 characters)")
	}
	cur, ok, err := e.EInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return cur, err
	}
	if !ok || cur.Status != EInvGenerated {
		return cur, httpx.Conflict("this invoice has no active IRN to cancel")
	}
	if cur.AckDate != nil && e.now().Sub(*cur.AckDate) > CancelWindow {
		return cur, httpx.Conflict("an IRN can be cancelled only within 24 hours of acknowledgement (acknowledged %s); issue a credit note instead",
			cur.AckDate.In(gsp.IST).Format("02 Jan 2006 15:04"))
	}
	var activeEWB string
	if err := e.DB.QueryRow(ctx, `SELECT COALESCE(ewb_no,'') FROM eway_bills WHERE tenant_id=$1 AND invoice_id=$2 AND status='generated'`,
		tenantID, invoiceID).Scan(&activeEWB); err != nil && !db.IsNotFound(err) {
		return cur, err
	}
	if activeEWB != "" {
		return cur, httpx.Conflict("e-way bill %s is active for this invoice; cancel it before the IRN", activeEWB)
	}
	creds, err := e.credentials(ctx, tenantID, cur.GSTIN)
	if err != nil {
		return cur, err
	}
	res, err := e.GSP.CancelIRN(ctx, creds, gsp.CancelIRNRequest{IRN: cur.IRN, ReasonCode: reason, Remark: remark})
	if err != nil {
		return cur, gspHTTPError(err)
	}
	at := res.CancelDate
	if at.IsZero() {
		at = e.now()
	}
	err = db.WithTx(ctx, e.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE einvoices SET status='cancelled', cancelled_at=$3, cancel_reason=$4, cancel_remark=$5,
			updated_at=now() WHERE tenant_id=$1 AND invoice_id=$2`, tenantID, invoiceID, at, reason, remark); err != nil {
			return err
		}
		return enqueue(ctx, tx, events.EInvoiceGenerated, tenantID, cur.StoreID, events.EInvoicePayload{
			InvoiceID: invoiceID, Number: cur.InvoiceNumber, GSTIN: cur.GSTIN, Status: EInvCancelled, IRN: cur.IRN,
			AckNo: cur.AckNo, AckDate: rfc3339(cur.AckDate), CancelledAt: at.UTC().Format(time.RFC3339), GeneratedVia: e.GSP.Name()})
	})
	if err != nil {
		return cur, err
	}
	e.kick()
	e.log().Info("irn cancelled", "invoice_id", invoiceID, "by", by, "reason", reason)
	out, _, err := e.EInvoice(ctx, tenantID, invoiceID)
	return out, err
}

// autoDocTypes are the documents generated automatically from aadesh events.
// Purchase returns (DBN) are only generated on request: whether the buyer's
// debit note must be e-invoiced depends on how the return is documented.
var autoDocTypes = map[string]bool{"sale": true, "sale_return": true}

// OnInvoiceEvent handles aadesh.invoice.created / aadesh.note.created:
// generate the IRN when the invoice is e-invoice applicable. Idempotent via
// processed_events; permanent errors are stored as failed (never retried);
// transient ones are retried by the broker up to MaxAttempts.
func (e *Engine) OnInvoiceEvent(ctx context.Context, env events.Envelope) error {
	var p events.InvoicePayload
	if err := env.Decode(&p); err != nil || p.InvoiceID == "" || env.TenantID == "" {
		return nil // malformed: never succeeds, ack it
	}
	var done bool
	if err := e.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, env.ID).Scan(&done); err != nil {
		return err
	}
	req := GenerateRequest{TenantID: env.TenantID, Invoice: invoiceFromEvent(p), By: "system", Trigger: "event",
		EventID: env.ID, EventTyp: env.Type}
	if done {
		return nil
	}
	if !autoDocTypes[p.DocType] || p.BillOfSupply || p.IRN != "" {
		return e.markOnly(ctx, req)
	}
	_, err := e.GenerateEInvoice(ctx, req)
	if err == nil || errors.Is(err, ErrNotApplicable) {
		return nil
	}
	cur, ok, lerr := e.EInvoice(ctx, env.TenantID, p.InvoiceID)
	if lerr == nil && ok && (cur.Status == EInvFailed || cur.Status == EInvGenerated) {
		return nil // stored as failed; do not retry forever
	}
	if transient(err) {
		return err
	}
	return nil
}
