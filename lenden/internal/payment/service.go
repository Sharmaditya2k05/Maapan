package payment

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/lenden/internal/gateway"
	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
)

// Service implements lenden's use cases.
type Service struct {
	DB        *pgxpool.Pool
	Gateways  *gateway.Registry
	Ledger    Ledger
	Relay     *events.Relay
	Log       *slog.Logger
	PayeeName string // name shown in the customer's UPI app
	Now       func() time.Time
}

const requestCols = `id, store_id, invoice_id, party_id, party_phone, amount_paise, paid_paise, purpose, kind, gateway,
	gateway_ref, upi_qr_image_url, upi_intent, short_url, status, expires_at, created_at`

func scanRequest(row pgx.Row) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.StoreID, &r.InvoiceID, &r.PartyID, &r.PartyPhone, &r.AmountPaise, &r.PaidPaise,
		&r.Purpose, &r.Kind, &r.Gateway, &r.GatewayRef, &r.UPIQRImageURL, &r.UPIIntent, &r.ShortURL, &r.Status,
		&r.ExpiresAt, &r.CreatedAt)
	return &r, err
}

const paymentCols = `id, store_id, request_id, invoice_id, party_id, party_phone, amount_paise, refunded_paise, method,
	gateway, gateway_ref, status, notes, captured_at, created_by`

func scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.StoreID, &p.RequestID, &p.InvoiceID, &p.PartyID, &p.PartyPhone, &p.AmountPaise,
		&p.RefundedPaise, &p.Method, &p.Gateway, &p.GatewayRef, &p.Status, &p.Notes, &p.CapturedAt, &p.CreatedBy)
	return &p, err
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
	gw := s.Gateways.Default()
	if in.Gateway != "" {
		g, ok := s.Gateways.Get(in.Gateway)
		if !ok {
			return nil, httpx.Validation("gateway %q is not configured (available: %s)", in.Gateway, strings.Join(s.Gateways.Names(), ", "))
		}
		gw = g
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
func (s *Service) Manual(ctx context.Context, p *auth.Principal, in ManualInput) (*Payment, error) {
	storeID := in.StoreID
	if storeID == "" {
		storeID = p.StoreID
	}
	if p.Claims != nil && storeID != "" && !p.Claims.CanAccessStore(storeID) {
		return nil, httpx.Forbidden("no access to store")
	}
	at, err := in.receivedAt(s.Now())
	if err != nil {
		return nil, err
	}
	var out *Payment
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		out, err = scanPayment(tx.QueryRow(ctx, `INSERT INTO payments (tenant_id, store_id, invoice_id, party_id, party_phone,
				amount_paise, method, gateway, gateway_ref, notes, captured_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'manual',$8,$9,$10,$11) RETURNING `+paymentCols,
			p.TenantID, nilIfEmpty(storeID), nilIfEmpty(in.InvoiceID), nilIfEmpty(in.PartyID), nilIfEmpty(in.PartyPhone),
			in.AmountPaise, in.Method, nilIfEmpty(in.Reference), nilIfEmpty(in.Notes), at, p.UserID()))
		if err != nil {
			return err
		}
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
		return nil, err
	}
	s.kick()
	return out, nil
}

// Refund refunds part or all of a payment. Manual payments and providers that
// refund instantly are processed now; otherwise the refund.processed webhook finishes it.
func (s *Service) Refund(ctx context.Context, p *auth.Principal, paymentID string, in RefundInput) (*Payment, error) {
	pay, err := s.GetPayment(ctx, p.TenantID, paymentID)
	if err != nil {
		return nil, err
	}
	if p.Claims != nil && pay.StoreID != nil && !p.Claims.CanAccessStore(*pay.StoreID) {
		return nil, httpx.NotFound("payment not found")
	}
	var pending int64
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(sum(amount_paise),0) FROM refunds
		WHERE tenant_id=$1 AND payment_id=$2 AND status='pending'`, p.TenantID, paymentID).Scan(&pending); err != nil {
		return nil, err
	}
	refundable := pay.AmountPaise - pay.RefundedPaise - pending
	amount := in.AmountPaise
	if amount == 0 {
		amount = refundable
	}
	if amount <= 0 || amount > refundable {
		return nil, httpx.Validation("refundable amount is %d paise", refundable)
	}

	var refundID string
	if err := s.DB.QueryRow(ctx, `INSERT INTO refunds (tenant_id, payment_id, amount_paise, reason, created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, p.TenantID, paymentID, amount, in.Reason, p.UserID()).Scan(&refundID); err != nil {
		return nil, err
	}

	processed := pay.Gateway == "manual"
	if !processed {
		gw, ok := s.Gateways.Get(pay.Gateway)
		if !ok {
			_, _ = s.DB.Exec(ctx, `UPDATE refunds SET status='failed' WHERE id=$1`, refundID)
			return nil, httpx.Unavailable("payment was taken through %s, which is no longer configured", pay.Gateway)
		}
		rf, err := gw.Refund(ctx, deref(pay.GatewayRef), amount, refundID)
		if err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE refunds SET status='failed' WHERE id=$1`, refundID)
			s.Log.Error("gateway refund failed", "payment_id", paymentID, "error", err.Error())
			return nil, httpx.Upstream("refund failed at the payment gateway")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE refunds SET gateway_ref=$2 WHERE id=$1`, refundID, rf.RefundRef); err != nil {
			return nil, err
		}
		processed = rf.Status == "processed"
	}
	if processed {
		if err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return applyRefund(ctx, tx, refundID) }); err != nil {
			return nil, err
		}
		s.kick()
	}
	return s.GetPayment(ctx, p.TenantID, paymentID)
}

// ReconcileReport summarises a reconciliation run.
type ReconcileReport struct {
	Checked  int `json:"checked"`
	Captured int `json:"captured"`
	Expired  int `json:"expired"`
	Errors   int `json:"errors"`
}

// Reconcile asks the provider about open requests from the last 3 days and
// records payments whose webhook never arrived; expires stale requests.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport
	rows, err := s.DB.Query(ctx, `SELECT id, kind, gateway, gateway_ref, expires_at FROM payment_requests
		WHERE status='created' AND gateway = ANY($1) AND gateway_ref IS NOT NULL AND created_at > now() - interval '3 days'
		ORDER BY created_at LIMIT 500`, s.Gateways.Names())
	if err != nil {
		return rep, err
	}
	type open struct {
		id, kind, gw, ref string
		expires           time.Time
	}
	var list []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.kind, &o.gw, &o.ref, &o.expires); err != nil {
			rows.Close()
			return rep, err
		}
		list = append(list, o)
	}
	rows.Close()

	now := s.Now()
	for _, o := range list {
		rep.Checked++
		gw, _ := s.Gateways.Get(o.gw) // the query only returns configured gateways
		var st gateway.RemoteStatus
		if o.kind == "upi_qr" {
			st, err = gw.FetchQR(ctx, o.ref)
		} else {
			st, err = gw.FetchLink(ctx, o.ref)
		}
		if err != nil {
			rep.Errors++
			s.Log.Warn("reconcile fetch failed", "request_id", o.id, "error", err.Error())
			continue
		}
		paid := false
		for _, rp := range st.Payments {
			if rp.Status != "captured" && rp.Status != "refunded" {
				continue
			}
			res, err := s.Ledger.Capture(ctx, "", nil, Capture{RequestID: o.id, Gateway: gw.Name(),
				PaymentRef: rp.PaymentRef, AmountPaise: rp.AmountPaise, Method: rp.Method, CapturedAt: rp.CapturedAt})
			if err != nil {
				rep.Errors++
				continue
			}
			paid = true
			if res == Applied {
				rep.Captured++
			}
		}
		if !paid && (st.Closed || now.After(o.expires.Add(5*time.Minute))) {
			tag, err := s.DB.Exec(ctx, `UPDATE payment_requests SET status='expired', updated_at=now() WHERE id=$1 AND status='created'`, o.id)
			if err == nil && tag.RowsAffected() > 0 {
				rep.Expired++
			}
		}
	}
	return rep, nil
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
	if req.Status != "created" || req.GatewayRef == nil {
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
