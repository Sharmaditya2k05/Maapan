package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/lenden/internal/creds"
	"github.com/maapan/lenden/internal/gateway"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// Service implements lenden's use cases.
type Service struct {
	DB        *pgxpool.Pool
	Gateways  *gateway.Registry
	Ledger    Ledger
	Relay     *events.Relay
	Log       *slog.Logger
	Creds     *creds.Store // per-tenant gateway keys (nil = platform keys only)
	PayeeName string       // name shown in the customer's UPI app
	Now       func() time.Time
}

// Reconciliation tuning.
const (
	reconcileWindow     = 7 * 24 * time.Hour // how far back open requests and refunds are checked
	expiryGrace         = 5 * time.Minute    // slack after expires_at before a request is expired
	refundSettleAfter   = 2 * time.Minute    // leave the webhook a chance before asking the gateway
	refundGiveUpUnknown = time.Hour          // an 'unknown' refund the gateway has no record of after this is failed
)

const requestCols = `id, store_id, invoice_id, party_id, party_phone, amount_paise, paid_paise, purpose, kind, gateway,
	gateway_ref, upi_qr_image_url, upi_intent, short_url, status, expires_at, created_at`

func scanRequest(row pgx.Row) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.StoreID, &r.InvoiceID, &r.PartyID, &r.PartyPhone, &r.AmountPaise, &r.PaidPaise,
		&r.Purpose, &r.Kind, &r.Gateway, &r.GatewayRef, &r.UPIQRImageURL, &r.UPIIntent, &r.ShortURL, &r.Status,
		&r.ExpiresAt, &r.CreatedAt)
	return &r, err
}

const paymentCols = `id, store_id, request_id, invoice_id, party_id, party_phone, amount_paise, refunded_paise, short_paise,
	excess_paise, method, gateway, gateway_ref, status, notes, idempotency_key, captured_at, created_by`

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.StoreID, &p.RequestID, &p.InvoiceID, &p.PartyID, &p.PartyPhone, &p.AmountPaise,
		&p.RefundedPaise, &p.ShortPaise, &p.ExcessPaise, &p.Method, &p.Gateway, &p.GatewayRef, &p.Status, &p.Notes,
		&p.IdempotencyKey, &p.CapturedAt, &p.CreatedBy)
	return &p, err
}

const refundCols = `id, payment_id, amount_paise, gateway_ref, status, source, reason, last_error, created_at, processed_at`

func scanRefund(row pgx.Row) (*RefundView, error) {
	var r RefundView
	err := row.Scan(&r.ID, &r.PaymentID, &r.AmountPaise, &r.GatewayRef, &r.Status, &r.Source, &r.Reason, &r.LastError,
		&r.CreatedAt, &r.ProcessedAt)
	return &r, err
}

// gatewayFor resolves the driver for a tenant (its own keys first, then the platform's).
func (s *Service) gatewayFor(ctx context.Context, tenantID, name string) (gateway.Gateway, error) {
	g, err := s.Gateways.For(ctx, tenantID, name)
	if errors.Is(err, gateway.ErrNotConfigured) {
		return nil, httpx.Unavailable("payment gateway %s is not configured", name)
	}
	return g, err
}

// CreateRequest stores a payment request and creates the QR/link at the provider.
func (s *Service) CreateRequest(ctx context.Context, p *auth.Principal, in RequestInput) (*Request, error) {
	if p.TenantID == "" {
		return nil, httpx.BadRequest("tenant is required")
	}
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if storeID != "" && !isUUID(storeID) {
		return nil, httpx.Validation("store_id must be a UUID")
	}
	if p.Claims != nil && storeID != "" && !p.Claims.CanAccessStore(storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	gwName := s.Gateways.DefaultName()
	if in.Gateway != "" {
		gwName = in.Gateway
	}
	gw, err := s.Gateways.For(ctx, p.TenantID, gwName)
	if errors.Is(err, gateway.ErrNotConfigured) {
		names, _ := s.Gateways.NamesFor(ctx, p.TenantID)
		return nil, httpx.Validation("gateway %q is not configured (available: %s)", gwName, strings.Join(names, ", "))
	}
	if err != nil {
		return nil, err
	}
	now := s.Now()
	expires := in.Expiry(now)

	req, err := scanRequest(s.DB.QueryRow(ctx, `INSERT INTO payment_requests
		(tenant_id, store_id, invoice_id, party_id, party_phone, amount_paise, purpose, kind, gateway, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+requestCols,
		p.TenantID, nilIfEmpty(storeID), nilIfEmpty(in.InvoiceID), nilIfEmpty(in.PartyID), nilIfEmpty(in.PartyPhone),
		in.AmountPaise, in.Purpose, in.Kind, gw.Name(), expires, p.UserID()))
	if err != nil {
		return nil, err
	}

	desc := "Payment to " + s.PayeeName
	if in.InvoiceID != "" {
		desc = "Invoice payment " + shortID(in.InvoiceID)
	}
	var created gateway.Created
	if in.Kind == "upi_qr" {
		created, err = gw.CreateQR(ctx, gateway.QRRequest{RequestID: req.ID, TenantID: p.TenantID,
			AmountPaise: in.AmountPaise, Name: s.PayeeName, Phone: in.PartyPhone, Description: desc, CloseBy: expires})
	} else {
		created, err = gw.CreateLink(ctx, gateway.LinkRequest{RequestID: req.ID, TenantID: p.TenantID,
			AmountPaise: in.AmountPaise, Description: desc, Phone: in.PartyPhone, ExpireBy: expires})
	}
	if err != nil {
		s.Log.Error("gateway create failed", "gateway", gw.Name(), "request_id", req.ID, "error", err.Error())
		_, _ = s.DB.Exec(ctx, `UPDATE payment_requests SET status='failed', last_error=$2, updated_at=now() WHERE id=$1`,
			req.ID, truncate(err.Error(), 500))
		return nil, httpx.Upstream("payment gateway error, please retry or take cash")
	}
	return scanRequest(s.DB.QueryRow(ctx, `UPDATE payment_requests SET gateway_ref=$2, upi_qr_image_url=$3, upi_intent=$4,
		short_url=$5, updated_at=now() WHERE id=$1 RETURNING `+requestCols,
		req.ID, created.GatewayRef, nilIfEmpty(created.ImageURL), nilIfEmpty(created.UPIIntent), nilIfEmpty(created.ShortURL)))
}

// GetRequest loads a payment request (the POS polls this while the QR is shown).
func (s *Service) GetRequest(ctx context.Context, tenantID, id string) (*Request, error) {
	if !isUUID(id) {
		return nil, httpx.NotFound("payment request not found")
	}
	r, err := scanRequest(s.DB.QueryRow(ctx, `SELECT `+requestCols+` FROM payment_requests WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("payment request not found")
	}
	return r, err
}

// GetPayment loads one payment.
func (s *Service) GetPayment(ctx context.Context, tenantID, id string) (*Payment, error) {
	if !isUUID(id) {
		return nil, httpx.NotFound("payment not found")
	}
	p, err := scanPayment(s.DB.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("payment not found")
	}
	return p, err
}

// GetRefund loads one refund of a tenant.
func (s *Service) GetRefund(ctx context.Context, tenantID, id string) (*RefundView, error) {
	r, err := scanRefund(s.DB.QueryRow(ctx, `SELECT `+refundCols+` FROM refunds WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("refund not found")
	}
	return r, err
}

// ListPayments returns payments matching f.
func (s *Service) ListPayments(ctx context.Context, tenantID string, f Filter) ([]Payment, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	for col, v := range map[string]string{"store_id": f.StoreID, "invoice_id": f.InvoiceID, "party_id": f.PartyID} {
		if v != "" {
			if !isUUID(v) {
				return nil, 0, httpx.Validation("%s must be a UUID", col)
			}
			add(col+" = $%d", v)
		}
	}
	if f.Method != "" {
		add("method = $%d", f.Method)
	}
	if f.Gateway != "" {
		add("gateway = $%d", f.Gateway)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.From != "" {
		add("captured_at >= ($%d::date AT TIME ZONE 'Asia/Kolkata')", f.From)
	}
	if f.To != "" {
		add("captured_at < (($%d::date + 1) AT TIME ZONE 'Asia/Kolkata')", f.To)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM payments WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM payments WHERE %s ORDER BY captured_at DESC LIMIT $%d OFFSET $%d`,
		paymentCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}

// Manual records a cash/cheque/bank receipt and publishes lenden.payment.captured.
// The idempotency key is required; a replay returns the first receipt with
// created=false, so a double-submitted cheque never books twice.
func (s *Service) Manual(ctx context.Context, p *auth.Principal, in ManualInput) (out *Payment, created bool, err error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" {
		return nil, false, httpx.Validation("idempotency_key is required (body field or Idempotency-Key header)")
	}
	if len(key) > MaxIdempotencyKey {
		return nil, false, httpx.Validation("idempotency_key must be at most %d characters", MaxIdempotencyKey)
	}
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if p.Claims != nil && storeID != "" && !p.Claims.CanAccessStore(storeID) {
		return nil, false, httpx.Forbidden("no access to store")
	}
	at, err := in.receivedAt(s.Now())
	if err != nil {
		return nil, false, err
	}
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		out, err = scanPayment(tx.QueryRow(ctx, `INSERT INTO payments (tenant_id, store_id, invoice_id, party_id, party_phone,
				amount_paise, method, gateway, gateway_ref, notes, idempotency_key, captured_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'manual',$8,$9,$10,$11,$12)
			ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
			RETURNING `+paymentCols,
			p.TenantID, nilIfEmpty(storeID), nilIfEmpty(in.InvoiceID), nilIfEmpty(in.PartyID), nilIfEmpty(in.PartyPhone),
			in.AmountPaise, in.Method, nilIfEmpty(in.Reference), nilIfEmpty(in.Notes), key, at, p.UserID()))
		if db.IsNotFound(err) {
			created = false
			return nil // replay: the first receipt stands
		}
		if err != nil {
			return err
		}
		created = true
		ev, err := events.New(events.PaymentCaptured, "lenden", p.TenantID, storeID, events.PaymentPayload{
			PaymentID: out.ID, InvoiceID: in.InvoiceID, PartyID: in.PartyID, PartyPhone: in.PartyPhone,
			AmountPaise: in.AmountPaise, Method: in.Method, Gateway: "manual", GatewayRef: in.Reference,
			CapturedAt: at.Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		s.kick()
		return out, true, nil
	}
	out, err = scanPayment(s.DB.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE tenant_id=$1 AND idempotency_key=$2`,
		p.TenantID, key))
	if err != nil {
		return nil, false, err
	}
	if out.AmountPaise != in.AmountPaise || out.Method != in.Method {
		return nil, false, httpx.Conflict("idempotency_key %q was already used for a different receipt (payment %s)", key, out.ID)
	}
	return out, false, nil
}

// Refund refunds part or all of a payment.
//
// The payment row is locked while the refundable amount (amount − every refund
// not in status failed) is recomputed and the pending refund row is written, so
// two concurrent refunds cannot both pass; the gateway is called only after
// that commit, leaving a trace if the process dies mid-way. A definitive 4xx
// rejection marks the refund failed; a timeout, transport error or 5xx leaves
// it unknown (the amount stays reserved) until the refund.processed webhook
// or reconciliation settles it.
func (s *Service) Refund(ctx context.Context, p *auth.Principal, paymentID string, in RefundInput) (*RefundResponse, error) {
	if !isUUID(paymentID) {
		return nil, httpx.NotFound("payment not found")
	}
	var (
		pay      *Payment
		refundID string
		amount   int64
	)
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		pay, err = scanPayment(tx.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
			p.TenantID, paymentID))
		if db.IsNotFound(err) {
			return httpx.NotFound("payment not found")
		}
		if err != nil {
			return err
		}
		if p.Claims != nil && pay.StoreID != nil && !p.Claims.CanAccessStore(*pay.StoreID) {
			return httpx.NotFound("payment not found")
		}
		var reserved int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_paise),0) FROM refunds
			WHERE tenant_id=$1 AND payment_id=$2 AND status <> 'failed'`, p.TenantID, paymentID).Scan(&reserved); err != nil {
			return err
		}
		refundable := pay.AmountPaise - reserved
		amount = in.AmountPaise
		if amount == 0 {
			amount = refundable
		}
		if amount <= 0 || amount > refundable {
			return httpx.Validation("refundable amount is %d paise", max(refundable, 0)).
				WithDetails(map[string]any{"refundable_paise": max(refundable, 0), "requested_paise": in.AmountPaise})
		}
		return tx.QueryRow(ctx, `INSERT INTO refunds (tenant_id, payment_id, amount_paise, reason, created_by)
			VALUES ($1,$2,$3,$4,$5) RETURNING id`, p.TenantID, paymentID, amount, in.Reason, p.UserID()).Scan(&refundID)
	})
	if err != nil {
		return nil, err
	}

	processed := pay.Gateway == "manual"
	if !processed {
		gw, err := s.Gateways.For(ctx, p.TenantID, pay.Gateway)
		if err != nil {
			s.markRefund(ctx, refundID, "failed", "gateway not configured: "+err.Error())
			if errors.Is(err, gateway.ErrNotConfigured) {
				return nil, httpx.Unavailable("payment was taken through %s, which is no longer configured", pay.Gateway)
			}
			return nil, err
		}
		rf, err := gw.Refund(ctx, deref(pay.GatewayRef), amount, refundID)
		switch {
		case err != nil && gateway.IsDefinitive(err):
			s.markRefund(ctx, refundID, "failed", err.Error())
			s.Log.Warn("gateway rejected refund", "payment_id", paymentID, "refund_id", refundID, "error", err.Error())
			return nil, httpx.Upstream("refund rejected by the payment gateway: %s", err.Error())
		case err != nil:
			s.markRefund(ctx, refundID, "unknown", err.Error())
			s.Log.Error("gateway refund outcome unknown; awaiting webhook/reconcile", "payment_id", paymentID, "refund_id", refundID, "error", err.Error())
			return s.refundResponse(ctx, p.TenantID, paymentID, refundID)
		}
		if _, err := s.DB.Exec(ctx, `UPDATE refunds SET gateway_ref=COALESCE($2, gateway_ref), updated_at=now() WHERE id=$1`,
			refundID, nilIfEmpty(rf.RefundRef)); err != nil {
			return nil, err
		}
		if rf.Status == "failed" {
			s.markRefund(ctx, refundID, "failed", "gateway reported the refund as failed")
			return nil, httpx.Upstream("refund failed at the payment gateway")
		}
		processed = rf.Status == "processed"
	}
	if processed {
		if err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return applyRefund(ctx, tx, refundID) }); err != nil {
			return nil, err
		}
		s.kick()
	}
	return s.refundResponse(ctx, p.TenantID, paymentID, refundID)
}

func (s *Service) markRefund(ctx context.Context, refundID, status, lastError string) {
	if _, err := s.DB.Exec(ctx, `UPDATE refunds SET status=$2, last_error=$3, updated_at=now() WHERE id=$1 AND status <> 'processed'`,
		refundID, status, truncate(lastError, 500)); err != nil {
		s.Log.Error("mark refund", "refund_id", refundID, "status", status, "error", err.Error())
	}
}

func (s *Service) refundResponse(ctx context.Context, tenantID, paymentID, refundID string) (*RefundResponse, error) {
	pay, err := s.GetPayment(ctx, tenantID, paymentID)
	if err != nil {
		return nil, err
	}
	rf, err := s.GetRefund(ctx, tenantID, refundID)
	if err != nil {
		return nil, err
	}
	return &RefundResponse{Payment: pay, Refund: *rf}, nil
}

// ReconcileReport summarises a reconciliation run.
type ReconcileReport struct {
	Checked        int `json:"checked"`         // open requests asked about at the gateway
	Captured       int `json:"captured"`        // payments booked whose webhook never arrived
	Refunded       int `json:"refunded"`        // refunds booked from the gateway's view of those requests
	Expired        int `json:"expired"`         // requests past expires_at
	RefundsChecked int `json:"refunds_checked"` // pending/unknown refunds asked about at the gateway
	RefundsSettled int `json:"refunds_settled"` // of those, now processed or failed
	Errors         int `json:"errors"`
}

// Reconcile asks the gateways about open requests from the last 7 days, books
// payments and refunds whose webhook never arrived, expires requests past their
// expires_at, and settles refunds whose outcome is still pending or unknown.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport
	if err := s.reconcileRequests(ctx, &rep); err != nil {
		return rep, err
	}
	if err := s.reconcileRefunds(ctx, &rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func (s *Service) reconcileRequests(ctx context.Context, rep *ReconcileReport) error {
	supported := s.Gateways.Supported()
	rows, err := s.DB.Query(ctx, `SELECT id, tenant_id, kind, gateway, gateway_ref, expires_at FROM payment_requests
		WHERE status IN ('created','partial') AND gateway = ANY($1) AND gateway_ref IS NOT NULL AND created_at > $2
		ORDER BY created_at LIMIT 500`, supported, s.Now().Add(-reconcileWindow))
	if err != nil {
		return err
	}
	type open struct {
		id, tenant, kind, gw, ref string
		expires                   time.Time
	}
	var list []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.tenant, &o.kind, &o.gw, &o.ref, &o.expires); err != nil {
			rows.Close()
			return err
		}
		list = append(list, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	now := s.Now()
	var unreachable []string // requests we could not ask the gateway about: never expire those blind
	for _, o := range list {
		rep.Checked++
		gw, err := s.Gateways.For(ctx, o.tenant, o.gw)
		if err != nil {
			rep.Errors++
			unreachable = append(unreachable, o.id)
			s.Log.Warn("reconcile: gateway unavailable for tenant", "request_id", o.id, "gateway", o.gw, "error", err.Error())
			continue
		}
		var st gateway.RemoteStatus
		if o.kind == "upi_qr" {
			st, err = gw.FetchQR(ctx, o.ref)
		} else {
			st, err = gw.FetchLink(ctx, o.ref)
		}
		if err != nil {
			rep.Errors++
			unreachable = append(unreachable, o.id)
			s.Log.Warn("reconcile fetch failed", "request_id", o.id, "error", err.Error())
			continue
		}
		for _, rp := range st.Payments {
			if rp.Status != "captured" && rp.Status != "refunded" {
				continue
			}
			res, err := s.Ledger.Capture(ctx, "", nil, Capture{RequestID: o.id, Gateway: gw.Name(),
				PaymentRef: rp.PaymentRef, AmountPaise: rp.AmountPaise, Method: rp.Method, CapturedAt: rp.CapturedAt})
			if err != nil {
				rep.Errors++
				s.Log.Warn("reconcile capture failed", "request_id", o.id, "payment_ref", rp.PaymentRef, "error", err.Error())
				continue
			}
			if res == Applied {
				rep.Captured++
			}
			// Refunds the gateway processed (dashboard refunds, or ours whose webhook was lost).
			for _, rr := range rp.Refunds {
				if rr.Status != "processed" {
					continue
				}
				res, err := s.Ledger.RefundProcessed(ctx, "", nil, RefundDone{Gateway: gw.Name(), RefundRef: rr.RefundRef,
					RefundID: rr.Receipt, PaymentRef: rp.PaymentRef, AmountPaise: rr.AmountPaise})
				if err != nil {
					rep.Errors++
					s.Log.Warn("reconcile refund failed", "payment_ref", rp.PaymentRef, "refund_ref", rr.RefundRef, "error", err.Error())
					continue
				}
				if res == Applied {
					rep.Refunded++
				}
			}
		}
		if st.Closed || now.After(o.expires.Add(expiryGrace)) {
			tag, err := s.DB.Exec(ctx, `UPDATE payment_requests SET status='expired', updated_at=now()
				WHERE id=$1 AND status IN ('created','partial')`, o.id)
			if err == nil && tag.RowsAffected() > 0 {
				rep.Expired++
			}
		}
	}
	// Everything else past its expiry — older than the window, never created at the
	// gateway, or on a gateway we no longer have — expires too. Requests whose gateway
	// could not be reached just now are left for the next run.
	if unreachable == nil {
		unreachable = []string{}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE payment_requests SET status='expired', updated_at=now()
		WHERE status IN ('created','partial') AND expires_at < $1 AND NOT (id::text = ANY($2))`,
		now.Add(-expiryGrace), unreachable)
	if err != nil {
		return err
	}
	rep.Expired += int(tag.RowsAffected())
	return nil
}

// reconcileRefunds settles refunds still pending or unknown by asking the
// gateway for the payment's refunds: matched by gateway refund id, then by
// receipt (our refund id).
func (s *Service) reconcileRefunds(ctx context.Context, rep *ReconcileReport) error {
	rows, err := s.DB.Query(ctx, `SELECT r.id, r.tenant_id, r.status, r.gateway_ref, r.created_at, p.gateway, p.gateway_ref
		FROM refunds r JOIN payments p ON p.id = r.payment_id
		WHERE r.status IN ('pending','unknown') AND p.gateway <> 'manual' AND p.gateway_ref IS NOT NULL
		  AND r.created_at < $1 AND r.created_at > $2
		ORDER BY r.created_at LIMIT 200`, s.Now().Add(-refundSettleAfter), s.Now().Add(-reconcileWindow))
	if err != nil {
		return err
	}
	type openRefund struct {
		id, tenant, status string
		ref                *string
		created            time.Time
		gw, payRef         string
	}
	var list []openRefund
	for rows.Next() {
		var o openRefund
		if err := rows.Scan(&o.id, &o.tenant, &o.status, &o.ref, &o.created, &o.gw, &o.payRef); err != nil {
			rows.Close()
			return err
		}
		list = append(list, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range list {
		rep.RefundsChecked++
		gw, err := s.Gateways.For(ctx, o.tenant, o.gw)
		if err != nil {
			rep.Errors++
			continue
		}
		remote, err := gw.FetchRefunds(ctx, o.payRef)
		if err != nil {
			rep.Errors++
			s.Log.Warn("reconcile: fetch refunds failed", "refund_id", o.id, "error", err.Error())
			continue
		}
		var match *gateway.RemoteRefund
		for i := range remote {
			if o.ref != nil && remote[i].RefundRef != "" && remote[i].RefundRef == *o.ref {
				match = &remote[i]
				break
			}
		}
		if match == nil {
			for i := range remote {
				if remote[i].Receipt == o.id {
					match = &remote[i]
					break
				}
			}
		}
		switch {
		case match == nil:
			if o.status == "unknown" && s.Now().Sub(o.created) > refundGiveUpUnknown {
				s.markRefund(ctx, o.id, "failed", "gateway has no record of this refund")
				s.Log.Warn("reconcile: unknown refund not found at gateway, marked failed", "refund_id", o.id, "payment_ref", o.payRef)
				rep.RefundsSettled++
			}
		case match.Status == "processed":
			res, err := s.Ledger.RefundProcessed(ctx, "", nil, RefundDone{Gateway: o.gw, RefundRef: match.RefundRef,
				RefundID: o.id, PaymentRef: o.payRef, AmountPaise: match.AmountPaise})
			if err != nil {
				rep.Errors++
				continue
			}
			if res == Applied {
				rep.RefundsSettled++
			}
		case match.Status == "failed":
			s.markRefund(ctx, o.id, "failed", "gateway reports the refund as failed")
			rep.RefundsSettled++
		default: // still pending at the gateway: record its id, wait
			if o.ref == nil && match.RefundRef != "" {
				_, _ = s.DB.Exec(ctx, `UPDATE refunds SET gateway_ref=$2, status='pending', updated_at=now() WHERE id=$1 AND status IN ('pending','unknown')`,
					o.id, match.RefundRef)
			}
		}
	}
	return nil
}

// ListSettlements returns a tenant's gateway payouts.
func (s *Service) ListSettlements(ctx context.Context, tenantID string, f SettlementFilter) ([]Settlement, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Gateway != "" {
		add("gateway = $%d", f.Gateway)
	}
	if f.From != "" {
		add("settled_at >= ($%d::date AT TIME ZONE 'Asia/Kolkata')", f.From)
	}
	if f.To != "" {
		add("settled_at < (($%d::date + 1) AT TIME ZONE 'Asia/Kolkata')", f.To)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM settlements WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`SELECT id, gateway, gateway_ref, utr, gross_paise, fee_paise, fee_tax_paise,
		refunds_paise, net_paise, settled_at, created_at FROM settlements WHERE %s ORDER BY settled_at DESC LIMIT $%d OFFSET $%d`,
		cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Settlement{}
	for rows.Next() {
		var st Settlement
		if err := rows.Scan(&st.ID, &st.Gateway, &st.GatewayRef, &st.UTR, &st.GrossPaise, &st.FeePaise, &st.FeeTaxPaise,
			&st.RefundsPaise, &st.NetPaise, &st.SettledAt, &st.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, st)
	}
	return out, total, rows.Err()
}

// PutGatewayCredentials stores a tenant's own keys for a gateway (MATURITY-PLAN D5).
func (s *Service) PutGatewayCredentials(ctx context.Context, p *auth.Principal, gw string, in GatewayCredentialInput) (*creds.Summary, error) {
	if s.Creds == nil {
		return nil, httpx.Unavailable("per-tenant gateway keys are not enabled")
	}
	if !s.Gateways.TenantConfigurable(gw) {
		return nil, httpx.Validation("gateway must be one of: %s", strings.Join(s.configurable(), ", "))
	}
	c := gateway.Credentials{TenantID: p.TenantID, Gateway: gw, KeyID: in.KeyID, KeySecret: in.KeySecret,
		WebhookSecret: in.WebhookSecret, Env: in.Env}
	switch gw {
	case "razorpay":
		if c.WebhookSecret == "" {
			return nil, httpx.Validation("razorpay needs webhook_secret (Dashboard → Webhooks)")
		}
		if c.Env == "" {
			c.Env = "live"
			if strings.HasPrefix(c.KeyID, "rzp_test_") {
				c.Env = "test"
			}
		}
		if c.Env != "test" && c.Env != "live" {
			return nil, httpx.Validation("razorpay env must be test or live")
		}
	case "cashfree":
		if c.Env == "" {
			c.Env = "production"
			if strings.HasPrefix(strings.ToUpper(c.KeyID), "TEST") {
				c.Env = "sandbox"
			}
		}
		if c.Env != "sandbox" && c.Env != "production" {
			return nil, httpx.Validation("cashfree env must be sandbox or production")
		}
	}
	if err := s.Creds.Put(ctx, c, p.UserID()); err != nil {
		return nil, err
	}
	s.Log.Info("gateway credentials stored", "tenant_id", p.TenantID, "gateway", gw, "env", c.Env, "key_id", creds.Mask(c.KeyID), "by", p.UserID())
	list, err := s.Creds.List(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Gateway == gw {
			return &list[i], nil
		}
	}
	return nil, httpx.Internal("credentials not found after save")
}

// DeleteGatewayCredentials removes a tenant's keys; the platform keys apply again.
func (s *Service) DeleteGatewayCredentials(ctx context.Context, p *auth.Principal, gw string) error {
	if s.Creds == nil {
		return httpx.Unavailable("per-tenant gateway keys are not enabled")
	}
	ok, err := s.Creds.Delete(ctx, p.TenantID, gw)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("no %s keys stored for this tenant", gw)
	}
	s.Log.Info("gateway credentials removed", "tenant_id", p.TenantID, "gateway", gw, "by", p.UserID())
	return nil
}

// ListGatewayCredentials returns the tenant's stored gateways, key ids masked.
func (s *Service) ListGatewayCredentials(ctx context.Context, tenantID string) ([]creds.Summary, error) {
	if s.Creds == nil {
		return []creds.Summary{}, nil
	}
	return s.Creds.List(ctx, tenantID)
}

func (s *Service) configurable() []string {
	var out []string
	for _, n := range s.Gateways.Supported() {
		if s.Gateways.TenantConfigurable(n) {
			out = append(out, n)
		}
	}
	return out
}

// Simulate pays a request through the mock gateway (local development only).
func (s *Service) Simulate(ctx context.Context, tenantID, requestID, method string) (*Request, error) {
	mock, ok := s.Gateways.Mock()
	if !ok {
		return nil, httpx.NotFound("simulation is only available when the mock gateway is configured")
	}
	req, err := s.GetRequest(ctx, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	if req.Gateway != "mock" {
		return nil, httpx.Conflict("request was created on %s, not the mock gateway", req.Gateway)
	}
	if (req.Status != "created" && req.Status != "partial") || req.GatewayRef == nil {
		return nil, httpx.Conflict("request is %s", req.Status)
	}
	if method == "" {
		method = "upi"
	}
	rp := mock.Pay(*req.GatewayRef, req.AmountPaise-req.PaidPaise, method)
	if _, err := s.Ledger.Capture(ctx, "mock:"+rp.PaymentRef, []byte(`{}`), Capture{RequestID: req.ID, Gateway: mock.Name(),
		PaymentRef: rp.PaymentRef, AmountPaise: rp.AmountPaise, Method: rp.Method, CapturedAt: rp.CapturedAt}); err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, tenantID, requestID)
}

func (s *Service) kick() {
	if s.Relay != nil {
		s.Relay.Kick()
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
