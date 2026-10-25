package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/kar/internal/clients"
	"github.com/maapan/kar/internal/gsp"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// e-way statuses (eway_bills.status).
const (
	EWBDraft     = "draft"
	EWBGenerated = "generated"
	EWBCancelled = "cancelled"
	EWBFailed    = "failed"
)

// EWayRecord is one invoice's e-way bill state.
type EWayRecord struct {
	InvoiceID      string          `json:"invoice_id"`
	InvoiceNumber  string          `json:"invoice_number"`
	StoreID        string          `json:"store_id,omitempty"`
	GSTIN          string          `json:"gstin"`
	DocDate        string          `json:"doc_date,omitempty"`
	IRN            string          `json:"irn,omitempty"`
	Status         string          `json:"status"`
	EWBNo          string          `json:"ewb_no,omitempty"`
	EWBDate        *time.Time      `json:"ewb_date,omitempty"`
	ValidUpto      *time.Time      `json:"valid_upto,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	VehicleNo      string          `json:"vehicle_no,omitempty"`
	VehicleType    string          `json:"vehicle_type,omitempty"`
	DistanceKM     int             `json:"distance_km"`
	TransporterID  string          `json:"transporter_id,omitempty"`
	Extensions     int             `json:"extensions"`
	BelowThreshold bool            `json:"below_threshold"`
	Warnings       json.RawMessage `json:"warnings,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	Provider       string          `json:"provider,omitempty"`
	CancelledAt    *time.Time      `json:"cancelled_at,omitempty"`
	CancelReason   *int            `json:"cancel_reason,omitempty"`
	CancelRemark   string          `json:"cancel_remark,omitempty"`
	History        json.RawMessage `json:"history,omitempty"`
	CreatedBy      string          `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

const ewbCols = `invoice_id, invoice_number, COALESCE(store_id::text,''), gstin, COALESCE(to_char(doc_date,'YYYY-MM-DD'),''),
	COALESCE(irn,''), status, COALESCE(ewb_no,''), ewb_date, valid_upto, mode, vehicle_no, vehicle_type, distance_km,
	transporter_id, extensions, below_threshold, warnings, COALESCE(error_code,''), COALESCE(error_message,''), provider,
	cancelled_at, cancel_reason, COALESCE(cancel_remark,''), history, created_by, created_at, updated_at, payload`

func scanEWB(row pgx.Row) (EWayRecord, error) {
	var r EWayRecord
	err := row.Scan(&r.InvoiceID, &r.InvoiceNumber, &r.StoreID, &r.GSTIN, &r.DocDate, &r.IRN, &r.Status, &r.EWBNo, &r.EWBDate,
		&r.ValidUpto, &r.Mode, &r.VehicleNo, &r.VehicleType, &r.DistanceKM, &r.TransporterID, &r.Extensions, &r.BelowThreshold,
		&r.Warnings, &r.ErrorCode, &r.ErrorMessage, &r.Provider, &r.CancelledAt, &r.CancelReason, &r.CancelRemark, &r.History,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.Payload)
	return r, err
}

// EWay returns the stored e-way bill of an invoice.
func (e *Engine) EWay(ctx context.Context, tenantID, invoiceID string) (EWayRecord, bool, error) {
	r, err := scanEWB(e.DB.QueryRow(ctx, `SELECT `+ewbCols+` FROM eway_bills WHERE tenant_id=$1 AND invoice_id=$2`, tenantID, invoiceID))
	if db.IsNotFound(err) {
		return r, false, nil
	}
	return r, err == nil, err
}

// ListEWays lists e-way bills (newest first).
func (e *Engine) ListEWays(ctx context.Context, tenantID string, f EInvoiceFilter) ([]EWayRecord, int, error) {
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
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM eway_bills WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := e.DB.Query(ctx, `SELECT `+ewbCols+` FROM eway_bills WHERE `+w+
		fmt.Sprintf(` ORDER BY COALESCE(ewb_date, created_at) DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []EWayRecord{}
	for rows.Next() {
		r, err := scanEWB(rows)
		if err != nil {
			return nil, 0, err
		}
		r.Payload, r.History = nil, nil
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// BuildEWayFor builds the NIC JSON for an invoice (download endpoint and generate).
func (e *Engine) BuildEWayFor(ctx context.Context, tenantID string, inv clients.Invoice, t Transport) (EWayFile, []string, bool, Seller, error) {
	s, err := e.seller(ctx, tenantID, inv.StoreID)
	if err != nil {
		return EWayFile{}, nil, false, s, err
	}
	_, threshold, err := e.taxSettings(ctx, tenantID)
	if err != nil {
		return EWayFile{}, nil, false, s, err
	}
	f, warnings, below, err := BuildEWay(inv, s.Party, e.Counterparty(ctx, inv), t, threshold)
	if err != nil {
		return f, nil, false, s, httpx.Validation("%s", err.Error())
	}
	if warnings == nil {
		warnings = []string{}
	}
	return f, warnings, below, s, nil
}

func historyEntry(action string, at time.Time, by string, extra map[string]any) []byte {
	m := map[string]any{"action": action, "at": at.UTC().Format(time.RFC3339), "by": by}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal([]any{m})
	return b
}

func (e *Engine) publishEWay(ctx context.Context, tx pgx.Tx, tenantID string, r EWayRecord, status, errMsg string) error {
	return enqueue(ctx, tx, events.EWayGenerated, tenantID, r.StoreID, events.EWayPayload{
		InvoiceID: r.InvoiceID, Number: r.InvoiceNumber, Status: status, EWBNo: r.EWBNo, EWBDate: rfc3339(r.EWBDate),
		ValidUpto: rfc3339(r.ValidUpto), VehicleNo: r.VehicleNo, DistanceKM: r.DistanceKM, Error: errMsg})
}

// GenerateEWay generates the e-way bill of an invoice: from its IRN when the
// invoice was e-invoiced, otherwise from the standalone NIC payload.
func (e *Engine) GenerateEWay(ctx context.Context, tenantID string, inv clients.Invoice, t Transport, by string) (EWayRecord, error) {
	t.Normalize()
	cur, exists, err := e.EWay(ctx, tenantID, inv.ID)
	if err != nil {
		return cur, err
	}
	if exists && cur.Status == EWBGenerated {
		return cur, httpx.Conflict("e-way bill %s is already active for this invoice; update Part-B, extend or cancel it", cur.EWBNo)
	}
	if err := CheckDocAge(inv.InvoiceDate, e.now()); err != nil {
		return cur, httpx.Validation("%s", err.Error())
	}
	file, warnings, below, seller, err := e.BuildEWayFor(ctx, tenantID, inv, t)
	if err != nil {
		return cur, err
	}
	bill := file.BillLists[0]
	irn := ""
	if inv.DocType == "sale" {
		if ei, ok, err := e.EInvoice(ctx, tenantID, inv.ID); err == nil && ok && ei.Status == EInvGenerated {
			irn = ei.IRN
		}
	}
	req := gsp.EWBRequest{Bill: bill}
	if irn != "" {
		r := &gsp.EWBByIRN{IRN: irn, Distance: t.TransDistance, TransMode: bill.TransMode, TransID: bill.TransporterID,
			TransName: bill.TransporterName, TransDocNo: bill.TransDocNo, TransDocDt: bill.TransDocDate, VehNo: bill.VehicleNo}
		if bill.TransMode == "1" {
			r.VehType = bill.VehicleType
		}
		if d := t.DispatchFrom; d != nil {
			a1, a2 := splitAddress(d.Address1)
			r.DispDtls = &gsp.Address{Name: Truncate(d.Name, 100), Addr1: a1, Addr2: a2, Loc: Truncate(d.Place, 50), Pin: d.Pincode, Stcd: d.StateCode}
		}
		if s := t.ShipTo; s != nil {
			a1, a2 := splitAddress(s.Address1)
			r.ExpShipDtls = &gsp.Address{Addr1: a1, Addr2: a2, Loc: Truncate(s.Place, 50), Pin: s.Pincode, Stcd: s.StateCode}
		}
		req = gsp.EWBRequest{ByIRN: r}
	}
	creds, err := e.credentials(ctx, tenantID, seller.GSTIN)
	if err != nil {
		return cur, err
	}
	res, gerr := e.GSP.GenerateEWB(ctx, creds, req)
	now := e.now()
	payload, _ := json.Marshal(file)
	warn, _ := json.Marshal(warnings)
	r := EWayRecord{InvoiceID: inv.ID, InvoiceNumber: inv.Number, StoreID: inv.StoreID, GSTIN: seller.GSTIN, DocDate: inv.InvoiceDate,
		IRN: irn, Mode: bill.TransMode, VehicleNo: bill.VehicleNo, VehicleType: bill.VehicleType, DistanceKM: t.TransDistance,
		TransporterID: bill.TransporterID, BelowThreshold: below, Provider: e.GSP.Name(), CreatedBy: by}
	if gerr != nil && transient(gerr) {
		return cur, gspHTTPError(gerr) // nothing changed on the portal side; the user retries
	}
	hist := historyEntry("generated", now, by, map[string]any{"ewb_no": res.EWBNo, "vehicle_no": bill.VehicleNo})
	if gerr == nil {
		r.Status, r.EWBNo, r.EWBDate = EWBGenerated, res.EWBNo, timePtr(res.EWBDate)
		vu := res.ValidUpto
		if vu.IsZero() && bill.TransMode != "" && (bill.VehicleNo != "" || bill.TransDocNo != "") {
			vu = gsp.ValidUpto(res.EWBDate, gsp.ValidityDays(t.TransDistance, t.ODC()))
		}
		r.ValidUpto = timePtr(vu)
	} else {
		r.Status = EWBFailed
		r.ErrorCode, r.ErrorMessage = errorCode(gerr)
		hist = historyEntry("failed", now, by, map[string]any{"error": r.ErrorMessage})
	}
	err = db.WithTx(ctx, e.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO eway_bills (tenant_id, invoice_id, invoice_number, payload, below_threshold, warnings,
				created_by, store_id, gstin, doc_date, irn, status, ewb_no, ewb_date, valid_upto, mode, vehicle_no, vehicle_type,
				distance_km, transporter_id, error_code, error_message, provider, history, extensions, cancelled_at, cancel_reason,
				cancel_remark, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid,$9,NULLIF($10,'')::date,NULLIF($11,''),$12,NULLIF($13,''),$14,$15,$16,$17,
				$18,$19,$20,NULLIF($21,''),NULLIF($22,''),$23,$24,0,NULL,NULL,NULL,now())
			ON CONFLICT (tenant_id, invoice_id) DO UPDATE SET invoice_number=EXCLUDED.invoice_number, payload=EXCLUDED.payload,
				below_threshold=EXCLUDED.below_threshold, warnings=EXCLUDED.warnings, created_by=EXCLUDED.created_by,
				store_id=EXCLUDED.store_id, gstin=EXCLUDED.gstin, doc_date=EXCLUDED.doc_date, irn=EXCLUDED.irn, status=EXCLUDED.status,
				ewb_no=EXCLUDED.ewb_no, ewb_date=EXCLUDED.ewb_date, valid_upto=EXCLUDED.valid_upto, mode=EXCLUDED.mode,
				vehicle_no=EXCLUDED.vehicle_no, vehicle_type=EXCLUDED.vehicle_type, distance_km=EXCLUDED.distance_km,
				transporter_id=EXCLUDED.transporter_id, error_code=EXCLUDED.error_code, error_message=EXCLUDED.error_message,
				provider=EXCLUDED.provider, history=eway_bills.history || EXCLUDED.history, extensions=0, cancelled_at=NULL,
				cancel_reason=NULL, cancel_remark=NULL, updated_at=now()
			WHERE eway_bills.status <> 'generated'`,
			tenantID, inv.ID, inv.Number, payload, below, warn, by, inv.StoreID, seller.GSTIN, inv.InvoiceDate, irn, r.Status,
			r.EWBNo, r.EWBDate, r.ValidUpto, r.Mode, r.VehicleNo, r.VehicleType, r.DistanceKM, r.TransporterID, r.ErrorCode,
			r.ErrorMessage, r.Provider, hist); err != nil {
			return err
		}
		return e.publishEWay(ctx, tx, tenantID, r, r.Status, r.ErrorMessage)
	})
	if err != nil {
		return r, err
	}
	e.kick()
	stored, _, err := e.EWay(ctx, tenantID, inv.ID)
	if err != nil {
		return r, err
	}
	if gerr != nil {
		return stored, gspHTTPError(gerr)
	}
	return stored, nil
}

func (e *Engine) activeEWay(ctx context.Context, tenantID, invoiceID string) (EWayRecord, gsp.Credentials, error) {
	cur, ok, err := e.EWay(ctx, tenantID, invoiceID)
	if err != nil {
		return cur, gsp.Credentials{}, err
	}
	if !ok || cur.Status != EWBGenerated {
		return cur, gsp.Credentials{}, httpx.Conflict("this invoice has no active e-way bill")
	}
	c, err := e.credentials(ctx, tenantID, cur.GSTIN)
	return cur, c, err
}

// PartBInput is the body of PUT /v1/eway-bills/{invoice_id}/part-b.
type PartBInput struct {
	Mode         string `json:"mode"` // 1-4 or road/rail/air/ship
	VehicleNo    string `json:"vehicle_no"`
	VehicleType  string `json:"vehicle_type"`
	TransDocNo   string `json:"trans_doc_no"`
	TransDocDate string `json:"trans_doc_date"` // YYYY-MM-DD
	FromPlace    string `json:"from_place"`
	FromState    string `json:"from_state"`
	ReasonCode   string `json:"reason_code"` // 1 breakdown, 2 transhipment, 3 others, 4 first time
	ReasonRemark string `json:"reason_remark"`
}

func nicDateOrEmpty(iso string) (string, error) {
	if iso == "" {
		return "", nil
	}
	return NICDate(iso)
}

// UpdatePartB changes the vehicle / transport document.
func (e *Engine) UpdatePartB(ctx context.Context, tenantID, invoiceID string, in PartBInput, by string) (EWayRecord, error) {
	cur, creds, err := e.activeEWay(ctx, tenantID, invoiceID)
	if err != nil {
		return cur, err
	}
	t := Transport{TransMode: in.Mode, VehicleNo: in.VehicleNo, VehicleType: in.VehicleType, TransDocNo: in.TransDocNo}
	t.Normalize()
	mode := t.ModeCode()
	if mode == "" {
		return cur, httpx.Validation("mode must be 1 (road), 2 (rail), 3 (air) or 4 (ship)")
	}
	if mode == "1" && !gsp.ValidVehicle(t.VehicleNo) {
		return cur, httpx.Validation("vehicle_no %q is not a valid registration number", in.VehicleNo)
	}
	docDate, err := nicDateOrEmpty(in.TransDocDate)
	if err != nil {
		return cur, httpx.Validation("trans_doc_date must be YYYY-MM-DD")
	}
	if in.ReasonCode == "" {
		in.ReasonCode = "4"
	}
	if strings.TrimSpace(in.ReasonRemark) == "" {
		in.ReasonRemark = gsp.EWBCancelReasons[4]
	}
	res, err := e.GSP.UpdatePartB(ctx, creds, gsp.PartBRequest{EWBNo: cur.EWBNo, VehicleNo: t.VehicleNo, FromPlace: Truncate(in.FromPlace, 50),
		FromState: stateInt(in.FromState), ReasonCode: in.ReasonCode, ReasonRem: Truncate(in.ReasonRemark, 50), TransDocNo: t.TransDocNo,
		TransDocDate: docDate, TransMode: mode, VehicleType: t.VehicleType})
	if err != nil {
		return cur, gspHTTPError(err)
	}
	now := e.now()
	vu := cur.ValidUpto
	if !res.ValidUpto.IsZero() {
		vu = &res.ValidUpto
	} else if vu == nil && cur.EWBDate != nil {
		v := gsp.ValidUpto(now, gsp.ValidityDays(cur.DistanceKM, t.ODC()))
		vu = &v
	}
	cur.Mode, cur.VehicleNo, cur.ValidUpto = mode, t.VehicleNo, vu
	return e.saveUpdate(ctx, tenantID, cur, "updated", historyEntry("part_b", now, by, map[string]any{"vehicle_no": t.VehicleNo, "mode": mode}),
		`mode=$3, vehicle_no=$4, valid_upto=$5`, mode, t.VehicleNo, vu)
}

// ExtendInput is the body of POST /v1/eway-bills/{invoice_id}/extend.
type ExtendInput struct {
	FromPlace           string `json:"from_place"`
	FromState           string `json:"from_state"`
	FromPincode         int    `json:"from_pincode"`
	RemainingDistanceKM int    `json:"remaining_distance_km"`
	ConsignmentStatus   string `json:"consignment_status"` // M in movement | T in transit
	TransitType         string `json:"transit_type"`       // R road, W warehouse, O others (when T)
	ReasonCode          int    `json:"reason_code"`        // 1 natural calamity, 2 law and order, 4 transhipment, 5 accident, 99 others
	Remark              string `json:"remark"`
	Mode                string `json:"mode"`
	VehicleNo           string `json:"vehicle_no"`
	TransDocNo          string `json:"trans_doc_no"`
	TransDocDate        string `json:"trans_doc_date"`
}

// ExtendEWay extends validity (8 hours either side of expiry).
func (e *Engine) ExtendEWay(ctx context.Context, tenantID, invoiceID string, in ExtendInput, by string) (EWayRecord, error) {
	cur, creds, err := e.activeEWay(ctx, tenantID, invoiceID)
	if err != nil {
		return cur, err
	}
	now := e.now()
	if cur.ValidUpto != nil && !gsp.CanExtend(*cur.ValidUpto, now) {
		return cur, httpx.Conflict("validity can be extended only from 8 hours before to 8 hours after it ends (%s)",
			cur.ValidUpto.In(gsp.IST).Format("02 Jan 2006 15:04"))
	}
	if in.RemainingDistanceKM < 1 || in.RemainingDistanceKM > gsp.MaxDistance {
		return cur, httpx.Validation("remaining_distance_km must be 1-%d", gsp.MaxDistance)
	}
	t := Transport{TransMode: in.Mode, VehicleNo: in.VehicleNo, TransDocNo: in.TransDocNo}
	t.Normalize()
	if in.ConsignmentStatus == "" {
		in.ConsignmentStatus = "M"
	}
	mode := t.ModeCode()
	if in.ConsignmentStatus == "M" && mode == "" {
		mode = cur.Mode
		if t.VehicleNo == "" {
			t.VehicleNo = cur.VehicleNo
		}
	}
	docDate, err := nicDateOrEmpty(in.TransDocDate)
	if err != nil {
		return cur, httpx.Validation("trans_doc_date must be YYYY-MM-DD")
	}
	res, err := e.GSP.ExtendValidity(ctx, creds, gsp.ExtendRequest{EWBNo: cur.EWBNo, VehicleNo: t.VehicleNo, FromPlace: Truncate(in.FromPlace, 50),
		FromState: stateInt(in.FromState), FromPincode: in.FromPincode, RemainingDistance: in.RemainingDistanceKM, TransDocNo: t.TransDocNo,
		TransDocDate: docDate, TransMode: mode, ExtnRsnCode: in.ReasonCode, ExtnRemarks: Truncate(in.Remark, 50),
		ConsignmentStatus: in.ConsignmentStatus, TransitType: in.TransitType, ODC: cur.VehicleType == "O"})
	if err != nil {
		return cur, gspHTTPError(err)
	}
	vu := res.ValidUpto
	if vu.IsZero() {
		vu = gsp.ValidUpto(now, gsp.ValidityDays(in.RemainingDistanceKM, cur.VehicleType == "O"))
	}
	return e.saveUpdate(ctx, tenantID, cur, "extended", historyEntry("extended", now, by,
		map[string]any{"valid_upto": vu.UTC().Format(time.RFC3339), "remaining_km": in.RemainingDistanceKM, "reason": in.ReasonCode}),
		`valid_upto=$3, extensions=extensions+1, vehicle_no=COALESCE(NULLIF($4,''), vehicle_no)`, vu, t.VehicleNo)
}

// CancelEWay cancels within 24 hours of generation.
func (e *Engine) CancelEWay(ctx context.Context, tenantID, invoiceID string, reason int, remark, by string) (EWayRecord, error) {
	if _, ok := gsp.EWBCancelReasons[reason]; !ok {
		return EWayRecord{}, httpx.Validation("reason_code must be 1 (duplicate), 2 (order cancelled), 3 (data entry mistake) or 4 (others)")
	}
	remark = strings.TrimSpace(remark)
	if remark == "" || len([]rune(remark)) > 50 {
		return EWayRecord{}, httpx.Validation("remark is required (at most 50 characters)")
	}
	cur, creds, err := e.activeEWay(ctx, tenantID, invoiceID)
	if err != nil {
		return cur, err
	}
	now := e.now()
	if cur.EWBDate != nil && now.Sub(*cur.EWBDate) > 24*time.Hour {
		return cur, httpx.Conflict("an e-way bill can be cancelled only within 24 hours of generation (generated %s)",
			cur.EWBDate.In(gsp.IST).Format("02 Jan 2006 15:04"))
	}
	at, err := e.GSP.CancelEWB(ctx, creds, gsp.CancelEWBRequest{EWBNo: cur.EWBNo, ReasonCode: reason, Remark: remark})
	if err != nil {
		return cur, gspHTTPError(err)
	}
	if at.IsZero() {
		at = now
	}
	return e.saveUpdate(ctx, tenantID, cur, "cancelled", historyEntry("cancelled", now, by, map[string]any{"reason": reason, "remark": remark}),
		`status='cancelled', cancelled_at=$3, cancel_reason=$4, cancel_remark=$5`, at, reason, remark)
}

// saveUpdate applies set (params from $3) and publishes kar.eway.generated with status.
func (e *Engine) saveUpdate(ctx context.Context, tenantID string, cur EWayRecord, status string, hist []byte, set string, args ...any) (EWayRecord, error) {
	params := append([]any{tenantID, cur.InvoiceID}, args...)
	params = append(params, hist)
	q := fmt.Sprintf(`UPDATE eway_bills SET %s, history = history || $%d::jsonb, updated_at=now() WHERE tenant_id=$1 AND invoice_id=$2`, set, len(params))
	err := db.WithTx(ctx, e.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, q, params...); err != nil {
			return err
		}
		r, err := scanEWB(tx.QueryRow(ctx, `SELECT `+ewbCols+` FROM eway_bills WHERE tenant_id=$1 AND invoice_id=$2`, tenantID, cur.InvoiceID))
		if err != nil {
			return err
		}
		return e.publishEWay(ctx, tx, tenantID, r, status, "")
	})
	if err != nil {
		return cur, err
	}
	e.kick()
	out, _, err := e.EWay(ctx, tenantID, cur.InvoiceID)
	return out, err
}
