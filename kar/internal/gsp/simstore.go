package gsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/db"
)

// SimIRN is one document registered on the simulated IRP.
type SimIRN struct {
	IRN           string
	GSTIN         string
	DocType       string
	DocNo         string
	DocDate       string // dd/mm/yyyy
	FY            string
	AckNo         string
	AckDt         time.Time
	SignedInvoice string
	SignedQR      string
	Status        string // ACT | CNL
	CancelledAt   time.Time
	CancelReason  int
	CancelRemark  string
	EWBNo         string
}

// SimEWB is one e-way bill on the simulated NIC system.
type SimEWB struct {
	EWBNo     string
	GSTIN     string
	DocKey    string // gstin|docType|docNo — one active bill per document
	DocDate   string // dd/mm/yyyy
	IRN       string
	Status    string // ACT | CNL
	VehicleNo string
	TransMode string
	Distance  int
	ODC       bool
	EWBDate   time.Time
	ValidUpto time.Time // zero while Part-B is missing
	Cancelled time.Time
	Extended  int
	Raw       json.RawMessage
}

// SimStore persists simulator state so restarts are consistent.
type SimStore interface {
	GetIRN(ctx context.Context, irn string) (SimIRN, bool, error)
	// InsertIRN stores r unless the IRN exists; then it returns the existing row and false.
	InsertIRN(ctx context.Context, r SimIRN) (SimIRN, bool, error)
	UpdateIRN(ctx context.Context, r SimIRN) error
	GetEWB(ctx context.Context, no string) (SimEWB, bool, error)
	ActiveEWBForDoc(ctx context.Context, docKey string) (SimEWB, bool, error)
	// InsertEWB stores e; false when an active bill for the same DocKey exists.
	InsertEWB(ctx context.Context, e SimEWB) (bool, error)
	UpdateEWB(ctx context.Context, e SimEWB) error
	NextEWBSeq(ctx context.Context) (int64, error)
}

// MemStore is an in-memory SimStore (tests, and a fallback without a DB).
type MemStore struct {
	mu   sync.Mutex
	irns map[string]SimIRN
	ewbs map[string]SimEWB
	seq  int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{irns: map[string]SimIRN{}, ewbs: map[string]SimEWB{}}
}

func (m *MemStore) GetIRN(_ context.Context, irn string) (SimIRN, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.irns[irn]
	return r, ok, nil
}

func (m *MemStore) InsertIRN(_ context.Context, r SimIRN) (SimIRN, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.irns[r.IRN]; ok {
		return ex, false, nil
	}
	m.irns[r.IRN] = r
	return r, true, nil
}

func (m *MemStore) UpdateIRN(_ context.Context, r SimIRN) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.irns[r.IRN]; !ok {
		return errors.New("irn not found")
	}
	m.irns[r.IRN] = r
	return nil
}

func (m *MemStore) GetEWB(_ context.Context, no string) (SimEWB, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.ewbs[no]
	return e, ok, nil
}

func (m *MemStore) ActiveEWBForDoc(_ context.Context, key string) (SimEWB, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.ewbs {
		if e.DocKey == key && e.Status == IRNActive {
			return e, true, nil
		}
	}
	return SimEWB{}, false, nil
}

func (m *MemStore) InsertEWB(_ context.Context, e SimEWB) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.ewbs {
		if x.DocKey == e.DocKey && x.Status == IRNActive {
			return false, nil
		}
	}
	m.ewbs[e.EWBNo] = e
	return true, nil
}

func (m *MemStore) UpdateEWB(_ context.Context, e SimEWB) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ewbs[e.EWBNo] = e
	return nil
}

func (m *MemStore) NextEWBSeq(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return m.seq, nil
}

// PGStore keeps simulator state in kar's database (gsp_sim_* tables, 0002 migration).
type PGStore struct{ DB *pgxpool.Pool }

const simIRNCols = `irn, gstin, doc_type, doc_no, doc_date, fy, ack_no, ack_dt, signed_invoice, signed_qr, status,
	COALESCE(cancelled_at, 'epoch'::timestamptz), cancel_reason, cancel_remark, ewb_no`

func scanSimIRN(row pgx.Row) (SimIRN, error) {
	var r SimIRN
	err := row.Scan(&r.IRN, &r.GSTIN, &r.DocType, &r.DocNo, &r.DocDate, &r.FY, &r.AckNo, &r.AckDt, &r.SignedInvoice,
		&r.SignedQR, &r.Status, &r.CancelledAt, &r.CancelReason, &r.CancelRemark, &r.EWBNo)
	if r.CancelledAt.Unix() == 0 {
		r.CancelledAt = time.Time{}
	}
	return r, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (p *PGStore) GetIRN(ctx context.Context, irn string) (SimIRN, bool, error) {
	r, err := scanSimIRN(p.DB.QueryRow(ctx, `SELECT `+simIRNCols+` FROM gsp_sim_irns WHERE irn=$1`, irn))
	if db.IsNotFound(err) {
		return SimIRN{}, false, nil
	}
	return r, err == nil, err
}

func (p *PGStore) InsertIRN(ctx context.Context, r SimIRN) (SimIRN, bool, error) {
	tag, err := p.DB.Exec(ctx, `INSERT INTO gsp_sim_irns (irn, gstin, doc_type, doc_no, doc_date, fy, ack_no, ack_dt,
			signed_invoice, signed_qr, status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (irn) DO NOTHING`,
		r.IRN, r.GSTIN, r.DocType, r.DocNo, r.DocDate, r.FY, r.AckNo, r.AckDt, r.SignedInvoice, r.SignedQR, r.Status)
	if err != nil {
		return SimIRN{}, false, err
	}
	if tag.RowsAffected() == 1 {
		return r, true, nil
	}
	ex, _, err := p.GetIRN(ctx, r.IRN)
	return ex, false, err
}

func (p *PGStore) UpdateIRN(ctx context.Context, r SimIRN) error {
	_, err := p.DB.Exec(ctx, `UPDATE gsp_sim_irns SET status=$2, cancelled_at=$3, cancel_reason=$4, cancel_remark=$5, ewb_no=$6
		WHERE irn=$1`, r.IRN, r.Status, nullTime(r.CancelledAt), r.CancelReason, r.CancelRemark, r.EWBNo)
	return err
}

const simEWBCols = `ewb_no, gstin, doc_key, doc_date, irn, status, vehicle_no, trans_mode, distance_km, odc, ewb_date,
	COALESCE(valid_upto, 'epoch'::timestamptz), COALESCE(cancelled_at, 'epoch'::timestamptz), extended, raw`

func scanSimEWB(row pgx.Row) (SimEWB, error) {
	var e SimEWB
	err := row.Scan(&e.EWBNo, &e.GSTIN, &e.DocKey, &e.DocDate, &e.IRN, &e.Status, &e.VehicleNo, &e.TransMode, &e.Distance,
		&e.ODC, &e.EWBDate, &e.ValidUpto, &e.Cancelled, &e.Extended, &e.Raw)
	if e.ValidUpto.Unix() == 0 {
		e.ValidUpto = time.Time{}
	}
	if e.Cancelled.Unix() == 0 {
		e.Cancelled = time.Time{}
	}
	return e, err
}

func (p *PGStore) GetEWB(ctx context.Context, no string) (SimEWB, bool, error) {
	e, err := scanSimEWB(p.DB.QueryRow(ctx, `SELECT `+simEWBCols+` FROM gsp_sim_ewbs WHERE ewb_no=$1`, no))
	if db.IsNotFound(err) {
		return SimEWB{}, false, nil
	}
	return e, err == nil, err
}

func (p *PGStore) ActiveEWBForDoc(ctx context.Context, key string) (SimEWB, bool, error) {
	e, err := scanSimEWB(p.DB.QueryRow(ctx, `SELECT `+simEWBCols+` FROM gsp_sim_ewbs WHERE doc_key=$1 AND status='ACT'`, key))
	if db.IsNotFound(err) {
		return SimEWB{}, false, nil
	}
	return e, err == nil, err
}

func (p *PGStore) InsertEWB(ctx context.Context, e SimEWB) (bool, error) {
	raw := e.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	_, err := p.DB.Exec(ctx, `INSERT INTO gsp_sim_ewbs (ewb_no, gstin, doc_key, doc_date, irn, status, vehicle_no, trans_mode,
			distance_km, odc, ewb_date, valid_upto, extended, raw) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		e.EWBNo, e.GSTIN, e.DocKey, e.DocDate, e.IRN, e.Status, e.VehicleNo, e.TransMode, e.Distance, e.ODC, e.EWBDate,
		nullTime(e.ValidUpto), e.Extended, raw)
	if db.IsUniqueViolation(err) {
		return false, nil
	}
	return err == nil, err
}

func (p *PGStore) UpdateEWB(ctx context.Context, e SimEWB) error {
	_, err := p.DB.Exec(ctx, `UPDATE gsp_sim_ewbs SET status=$2, vehicle_no=$3, trans_mode=$4, distance_km=$5, valid_upto=$6,
		cancelled_at=$7, extended=$8 WHERE ewb_no=$1`,
		e.EWBNo, e.Status, e.VehicleNo, e.TransMode, e.Distance, nullTime(e.ValidUpto), nullTime(e.Cancelled), e.Extended)
	return err
}

func (p *PGStore) NextEWBSeq(ctx context.Context) (int64, error) {
	var n int64
	err := p.DB.QueryRow(ctx, `SELECT nextval('gsp_sim_ewb_seq')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("ewb sequence: %w", err)
	}
	return n, nil
}
