package jobwork

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/nirmaan/internal/clients"
	"github.com/maapan/nirmaan/internal/numbering"
	"github.com/maapan/nirmaan/internal/units"
)

var ist = time.FixedZone("IST", 5*3600+1800)

// Service implements job work.
type Service struct {
	DB        *pgxpool.Pool
	Inventory clients.Inventory
	Relay     *events.Relay
	Log       *slog.Logger
	Now       func() time.Time
}

func (s *Service) today() string {
	n := time.Now()
	if s.Now != nil {
		n = s.Now()
	}
	return n.In(ist).Format(time.DateOnly)
}

func canStore(p *auth.Principal, storeID string) bool {
	return p.Claims == nil || p.Claims.CanAccessStore(storeID)
}

// ---------- job workers ----------

// Worker is a job worker (a party who processes our goods).
type Worker struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	GSTIN     *string   `json:"gstin,omitempty"`
	StateCode string    `json:"state_code"`
	Address   *string   `json:"address,omitempty"`
	Phone     *string   `json:"phone,omitempty"`
	PartyID   *string   `json:"party_id,omitempty"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkerInput is the body for creating/updating a job worker.
type WorkerInput struct {
	Name      string `json:"name"`
	GSTIN     string `json:"gstin,omitempty"`
	StateCode string `json:"state_code,omitempty"` // required when there is no GSTIN
	Address   string `json:"address,omitempty"`
	Phone     string `json:"phone,omitempty"`
	PartyID   string `json:"party_id,omitempty"`
	Active    *bool  `json:"active,omitempty"`
}

// Validate checks the body and derives the state from the GSTIN.
func (in *WorkerInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return httpx.Validation("name is required")
	}
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("%s", err.Error())
		}
		if in.StateCode != "" && in.StateCode != gst.StateCode(in.GSTIN) {
			return httpx.Validation("state_code does not match the GSTIN")
		}
		in.StateCode = gst.StateCode(in.GSTIN)
	}
	if !gst.ValidState(in.StateCode) {
		return httpx.Validation("a valid 2-digit state_code (or gstin) is required")
	}
	return nil
}

const workerCols = `id, name, gstin, state_code, address, phone, party_id, active, created_at`

func scanWorker(row pgx.Row) (*Worker, error) {
	var w Worker
	err := row.Scan(&w.ID, &w.Name, &w.GSTIN, &w.StateCode, &w.Address, &w.Phone, &w.PartyID, &w.Active, &w.CreatedAt)
	return &w, err
}

// CreateWorker adds a job worker.
func (s *Service) CreateWorker(ctx context.Context, tenantID string, in WorkerInput) (*Worker, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	w, err := scanWorker(s.DB.QueryRow(ctx, `INSERT INTO job_workers (tenant_id, name, gstin, state_code, address, phone, party_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+workerCols, tenantID, in.Name, nilIfEmpty(in.GSTIN), in.StateCode,
		nilIfEmpty(in.Address), nilIfEmpty(in.Phone), nilIfEmpty(in.PartyID)))
	if db.IsUniqueViolation(err) {
		return nil, httpx.Conflict("a job worker named %q exists", in.Name)
	}
	return w, err
}

// UpdateWorker replaces a job worker's details.
func (s *Service) UpdateWorker(ctx context.Context, tenantID, id string, in WorkerInput) (*Worker, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	active := in.Active == nil || *in.Active
	w, err := scanWorker(s.DB.QueryRow(ctx, `UPDATE job_workers SET name=$3, gstin=$4, state_code=$5, address=$6, phone=$7,
		party_id=$8, active=$9, updated_at=now() WHERE tenant_id=$1 AND id=$2 RETURNING `+workerCols,
		tenantID, id, in.Name, nilIfEmpty(in.GSTIN), in.StateCode, nilIfEmpty(in.Address), nilIfEmpty(in.Phone),
		nilIfEmpty(in.PartyID), active))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("job worker not found")
	}
	if db.IsUniqueViolation(err) {
		return nil, httpx.Conflict("a job worker named %q exists", in.Name)
	}
	return w, err
}

// GetWorker loads one job worker.
func (s *Service) GetWorker(ctx context.Context, tenantID, id string) (*Worker, error) {
	w, err := scanWorker(s.DB.QueryRow(ctx, `SELECT `+workerCols+` FROM job_workers WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("job worker not found")
	}
	return w, err
}

// ListWorkers lists job workers.
func (s *Service) ListWorkers(ctx context.Context, tenantID, search string, page httpx.Page) ([]Worker, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+workerCols+` FROM job_workers WHERE tenant_id=$1
		AND ($2 = '' OR name ILIKE '%' || $2 || '%' OR gstin = upper($2)) ORDER BY lower(name) LIMIT $3 OFFSET $4`,
		tenantID, search, page.Limit, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ---------- challans ----------

// ChallanLineInput is one line of goods sent.
type ChallanLineInput struct {
	ItemID            string  `json:"item_id"`
	ItemName          string  `json:"item_name,omitempty"` // default: bhandar name
	HSN               string  `json:"hsn,omitempty"`       // default: bhandar HSN
	Qty               float64 `json:"qty"`
	Unit              string  `json:"unit,omitempty"`                // default: the item's stock unit
	TaxableValuePaise *int64  `json:"taxable_value_paise,omitempty"` // default: qty × bhandar average cost
	TaxRateBP         *int    `json:"tax_rate_bp,omitempty"`         // default: bhandar rate
	GoodsType         string  `json:"goods_type,omitempty"`          // inputs (default) | capital_goods
	ReturnExempt      bool    `json:"return_exempt,omitempty"`       // moulds, dies, jigs, fixtures, tools
}

// ChallanInput is the body of POST /v1/job-work/challans.
type ChallanInput struct {
	JobWorkerID        string             `json:"job_worker_id"`
	StoreID            string             `json:"store_id,omitempty"`
	ChallanDate        string             `json:"challan_date,omitempty"` // default today
	PrincipalGSTIN     string             `json:"principal_gstin,omitempty"`
	PrincipalState     string             `json:"principal_state,omitempty"` // when there is no GSTIN
	NatureOfProcessing string             `json:"nature_of_processing"`
	ProductionRunID    string             `json:"production_run_id,omitempty"`
	EWayBillNo         string             `json:"eway_bill_no,omitempty"`
	VehicleNo          string             `json:"vehicle_no,omitempty"`
	Notes              string             `json:"notes,omitempty"`
	Lines              []ChallanLineInput `json:"lines"`
}

// Validate checks the body.
func (in *ChallanInput) Validate() error {
	if in.JobWorkerID == "" || strings.TrimSpace(in.NatureOfProcessing) == "" {
		return httpx.Validation("job_worker_id and nature_of_processing are required")
	}
	if in.ChallanDate != "" {
		if _, err := time.Parse(time.DateOnly, in.ChallanDate); err != nil {
			return httpx.Validation("challan_date must be YYYY-MM-DD")
		}
	}
	in.PrincipalGSTIN = gst.Normalize(in.PrincipalGSTIN)
	if in.PrincipalGSTIN != "" {
		if err := gst.ValidateGSTIN(in.PrincipalGSTIN); err != nil {
			return httpx.Validation("principal_gstin: %s", err.Error())
		}
		in.PrincipalState = gst.StateCode(in.PrincipalGSTIN)
	}
	if !gst.ValidState(in.PrincipalState) {
		return httpx.Validation("principal_gstin (or principal_state) is required: it decides IGST vs CGST/SGST and the e-way bill rule")
	}
	if len(in.Lines) == 0 || len(in.Lines) > 200 {
		return httpx.Validation("a challan needs 1 to 200 lines")
	}
	for i := range in.Lines {
		l := &in.Lines[i]
		if l.GoodsType == "" {
			l.GoodsType = GoodsInputs
		}
		switch {
		case l.ItemID == "" || l.Qty <= 0:
			return httpx.Validation("line %d needs item_id and a positive qty", i+1)
		case l.GoodsType != GoodsInputs && l.GoodsType != GoodsCapital:
			return httpx.Validation("line %d goods_type must be inputs or capital_goods", i+1)
		case l.TaxableValuePaise != nil && *l.TaxableValuePaise < 0:
			return httpx.Validation("line %d taxable_value_paise cannot be negative", i+1)
		case l.TaxRateBP != nil && (*l.TaxRateBP < 0 || *l.TaxRateBP > 4000):
			return httpx.Validation("line %d tax_rate_bp is out of range", i+1)
		}
	}
	return nil
}

// Challan is a job-work delivery challan.
type Challan struct {
	ID                 string        `json:"id"`
	ChallanNo          string        `json:"challan_no"`
	ChallanDate        string        `json:"challan_date"`
	StoreID            string        `json:"store_id"`
	JobWorker          Worker        `json:"job_worker"`
	PrincipalGSTIN     *string       `json:"principal_gstin,omitempty"`
	PrincipalState     *string       `json:"principal_state,omitempty"`
	NatureOfProcessing string        `json:"nature_of_processing"`
	ProductionRunID    *string       `json:"production_run_id,omitempty"`
	EWayBillNo         *string       `json:"eway_bill_no,omitempty"`
	VehicleNo          *string       `json:"vehicle_no,omitempty"`
	Status             string        `json:"status"`
	TotalValuePaise    int64         `json:"total_value_paise"`
	InterState         bool          `json:"inter_state"`
	Notes              *string       `json:"notes,omitempty"`
	CreatedBy          string        `json:"created_by"`
	CreatedAt          time.Time     `json:"created_at"`
	Lines              []ChallanLine `json:"lines,omitempty"`
	Receipts           []ReceiptSum  `json:"receipts,omitempty"`
	Warnings           []string      `json:"warnings,omitempty"`
}

// ChallanLine is a stored challan line with what has come back.
type ChallanLine struct {
	LineNo            int     `json:"line_no"`
	ItemID            string  `json:"item_id"`
	ItemName          string  `json:"item_name"`
	HSN               *string `json:"hsn,omitempty"`
	Unit              string  `json:"unit"`
	Qty               float64 `json:"qty"`
	Factor            float64 `json:"factor"`
	TaxableValuePaise int64   `json:"taxable_value_paise"`
	TaxRateBP         int     `json:"tax_rate_bp"`
	GoodsType         string  `json:"goods_type"`
	ReturnExempt      bool    `json:"return_exempt"`
	DueDate           *string `json:"due_date,omitempty"`
	Processed         float64 `json:"processed"`
	Returned          float64 `json:"returned"`
	Waste             float64 `json:"waste"`
	Pending           float64 `json:"pending"`
	DueStatus         string  `json:"due_status"`
	DaysLeft          *int    `json:"days_left,omitempty"`
}

// ReceiptSum is a receipt in the challan view.
type ReceiptSum struct {
	ID                     string  `json:"id"`
	ReceiptNo              string  `json:"receipt_no"`
	ReceiptDate            string  `json:"receipt_date"`
	JWChallanNo            *string `json:"jw_challan_no,omitempty"`
	ProcessingChargesPaise int64   `json:"processing_charges_paise"`
	PostingID              *string `json:"posting_id,omitempty"`
}

// CreateChallan records goods sent to a job worker.
func (s *Service) CreateChallan(ctx context.Context, p *auth.Principal, in ChallanInput) (*Challan, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	store := in.StoreID
	if store == "" {
		store = p.StoreID
	}
	if store == "" || !canStore(p, store) {
		return nil, httpx.Forbidden("store_id missing or not accessible")
	}
	w, err := s.GetWorker(ctx, p.TenantID, in.JobWorkerID)
	if err != nil {
		return nil, err
	}
	if !w.Active {
		return nil, httpx.Validation("job worker %s is inactive", w.Name)
	}
	date := in.ChallanDate
	if date == "" {
		date = s.today()
	}
	cd, _ := time.Parse(time.DateOnly, date)

	// defaults from bhandar: name, HSN, rate, stock unit, average-cost value
	ids := make([]string, 0, len(in.Lines))
	for _, l := range in.Lines {
		ids = append(ids, l.ItemID)
	}
	items := map[string]clients.Item{}
	prices := map[string]clients.Price{}
	if s.Inventory != nil {
		if prices, items, err = clients.ResolvePrices(ctx, s.Inventory, ids, store, clients.BasisAverage); err != nil {
			return nil, err
		}
	}
	type row struct {
		ChallanLineInput
		factor float64
		value  int64
		rate   int
		due    string
	}
	rows := make([]row, 0, len(in.Lines))
	var total int64
	for i, l := range in.Lines {
		it := items[l.ItemID]
		if strings.TrimSpace(l.ItemName) == "" {
			l.ItemName = it.Name
		}
		if strings.TrimSpace(l.ItemName) == "" {
			return nil, httpx.Validation("line %d: item_name is required (item not found in bhandar)", i+1)
		}
		if l.HSN == "" {
			l.HSN = it.HSN
		}
		if l.Unit == "" {
			l.Unit = it.Unit
		}
		l.Unit = units.Norm(l.Unit)
		if l.Unit == "" {
			l.Unit = "PCS"
		}
		f := 1.0
		if _, ok := items[l.ItemID]; ok {
			if f, err = units.Factor(l.Unit, it.Units()); err != nil {
				return nil, httpx.Validation("line %d %s: %s", i+1, l.ItemName, err.Error())
			}
		}
		r := row{ChallanLineInput: l, factor: f, rate: it.TaxRateBP}
		if l.TaxRateBP != nil {
			r.rate = *l.TaxRateBP
		}
		switch {
		case l.TaxableValuePaise != nil:
			r.value = *l.TaxableValuePaise
		case prices[l.ItemID].Paise > 0:
			r.value = units.Extend(l.Qty*f, prices[l.ItemID].Paise)
		default:
			return nil, httpx.Validation("line %d %s: no cost in bhandar; give taxable_value_paise (the challan must state the value)", i+1, l.ItemName)
		}
		r.due = DueDate(cd, l.GoodsType, l.ReturnExempt)
		total += r.value
		rows = append(rows, r)
	}

	var id string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		no, err := numbering.Next(ctx, tx, p.TenantID, numbering.JobWorkChallan, cd)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO job_work_challans (tenant_id, store_id, challan_no, challan_date, job_worker_id,
			principal_gstin, principal_state, nature_of_processing, production_run_id, eway_bill_no, vehicle_no, total_value_paise,
			notes, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
			p.TenantID, store, no, date, w.ID, nilIfEmpty(in.PrincipalGSTIN), in.PrincipalState, strings.TrimSpace(in.NatureOfProcessing),
			nilIfEmpty(in.ProductionRunID), nilIfEmpty(in.EWayBillNo), nilIfEmpty(in.VehicleNo), total, nilIfEmpty(in.Notes),
			p.UserID()).Scan(&id); err != nil {
			return err
		}
		for i, r := range rows {
			if _, err := tx.Exec(ctx, `INSERT INTO job_work_challan_lines (challan_id, line_no, item_id, item_name, hsn, unit, qty,
				factor, taxable_value_paise, tax_rate_bp, goods_type, return_exempt, due_date)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::date)`,
				id, i+1, r.ItemID, strings.TrimSpace(r.ItemName), nilIfEmpty(r.HSN), r.Unit, units.Round3(r.Qty), r.factor,
				r.value, r.rate, r.GoodsType, r.ReturnExempt, nilIfEmpty(r.due)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetChallan(ctx, p, id)
}

const challanCols = `c.id, c.challan_no, to_char(c.challan_date,'YYYY-MM-DD'), c.store_id, c.principal_gstin, c.principal_state,
	c.nature_of_processing, c.production_run_id, c.eway_bill_no, c.vehicle_no, c.status, c.total_value_paise, c.notes,
	c.created_by, c.created_at, w.id, w.name, w.gstin, w.state_code, w.address, w.phone, w.party_id, w.active, w.created_at`

func scanChallan(row pgx.Row) (*Challan, error) {
	var c Challan
	w := &c.JobWorker
	err := row.Scan(&c.ID, &c.ChallanNo, &c.ChallanDate, &c.StoreID, &c.PrincipalGSTIN, &c.PrincipalState, &c.NatureOfProcessing,
		&c.ProductionRunID, &c.EWayBillNo, &c.VehicleNo, &c.Status, &c.TotalValuePaise, &c.Notes, &c.CreatedBy, &c.CreatedAt,
		&w.ID, &w.Name, &w.GSTIN, &w.StateCode, &w.Address, &w.Phone, &w.PartyID, &w.Active, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.InterState = c.PrincipalState != nil && *c.PrincipalState != w.StateCode
	return &c, nil
}

func loadChallanLines(ctx context.Context, q db.Querier, challanID, asOf string) ([]ChallanLine, error) {
	rows, err := q.Query(ctx, `SELECT l.line_no, l.item_id, l.item_name, l.hsn, l.unit, l.qty::float8, l.factor::float8,
		l.taxable_value_paise, l.tax_rate_bp, l.goods_type, l.return_exempt, to_char(l.due_date,'YYYY-MM-DD'),
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='processed'),0)::float8,
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='returned'),0)::float8,
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='waste'),0)::float8
		FROM job_work_challan_lines l
		LEFT JOIN job_work_receipts r ON r.challan_id = l.challan_id
		LEFT JOIN job_work_receipt_lines rl ON rl.receipt_id = r.id AND rl.challan_line_no = l.line_no
		WHERE l.challan_id=$1 GROUP BY l.challan_id, l.line_no ORDER BY l.line_no`, challanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChallanLine
	for rows.Next() {
		var l ChallanLine
		if err := rows.Scan(&l.LineNo, &l.ItemID, &l.ItemName, &l.HSN, &l.Unit, &l.Qty, &l.Factor, &l.TaxableValuePaise,
			&l.TaxRateBP, &l.GoodsType, &l.ReturnExempt, &l.DueDate, &l.Processed, &l.Returned, &l.Waste); err != nil {
			return nil, err
		}
		pl := PendingLine{Sent: l.Qty, Processed: l.Processed, Returned: l.Returned, Waste: l.Waste, DueDate: deref(l.DueDate)}
		pl.Settle(l.TaxableValuePaise, asOf)
		l.Pending, l.DueStatus, l.DaysLeft = pl.Pending, pl.Status, pl.DaysLeft
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetChallan loads a challan with lines, receipts and GST warnings.
func (s *Service) GetChallan(ctx context.Context, p *auth.Principal, id string) (*Challan, error) {
	c, err := scanChallan(s.DB.QueryRow(ctx, `SELECT `+challanCols+` FROM job_work_challans c JOIN job_workers w ON w.id=c.job_worker_id
		WHERE c.tenant_id=$1 AND c.id=$2`, p.TenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("challan not found")
	}
	if err != nil {
		return nil, err
	}
	if !canStore(p, c.StoreID) {
		return nil, httpx.NotFound("challan not found")
	}
	if c.Lines, err = loadChallanLines(ctx, s.DB, id, s.today()); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, receipt_no, to_char(receipt_date,'YYYY-MM-DD'), jw_challan_no, processing_charges_paise, posting_id
		FROM job_work_receipts WHERE challan_id=$1 ORDER BY receipt_date, receipt_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ReceiptSum
		if err := rows.Scan(&r.ID, &r.ReceiptNo, &r.ReceiptDate, &r.JWChallanNo, &r.ProcessingChargesPaise, &r.PostingID); err != nil {
			return nil, err
		}
		c.Receipts = append(c.Receipts, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if c.Status != StatusCancelled {
		cd, _ := time.Parse(time.DateOnly, c.ChallanDate)
		f := ChallanFacts{ChallanDate: cd, PrincipalState: deref(c.PrincipalState), JobWorkerState: c.JobWorker.StateCode,
			JobWorkerGSTIN: deref(c.JobWorker.GSTIN), TotalValuePaise: c.TotalValuePaise, EWayBillNo: deref(c.EWayBillNo)}
		for _, l := range c.Lines {
			f.Lines = append(f.Lines, FactLine{l.GoodsType, l.ReturnExempt})
			if l.DueStatus == DueOverdue {
				c.Warnings = append(c.Warnings, fmt.Sprintf("Line %d (%s): %s pending past the %s limit — deemed supplied on %s; raise a tax invoice and pay GST with interest.",
					l.LineNo, l.ItemName, qtyStr(l.Pending), deref(l.DueDate), c.ChallanDate))
			} else if l.DueStatus == DueSoon {
				c.Warnings = append(c.Warnings, fmt.Sprintf("Line %d (%s): %s pending, due back by %s.", l.LineNo, l.ItemName, qtyStr(l.Pending), deref(l.DueDate)))
			}
		}
		c.Warnings = append(c.Warnings, Warnings(f)...)
	}
	return c, nil
}

// ListChallans lists challans.
func (s *Service) ListChallans(ctx context.Context, p *auth.Principal, workerID, status, from, to string, page httpx.Page) ([]Challan, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+challanCols+` FROM job_work_challans c JOIN job_workers w ON w.id=c.job_worker_id
		WHERE c.tenant_id=$1 AND ($2 = '' OR c.job_worker_id::text=$2) AND ($3 = '' OR c.status=$3)
		AND ($4 = '' OR c.challan_date >= $4::date) AND ($5 = '' OR c.challan_date <= $5::date)
		ORDER BY c.challan_date DESC, c.challan_no DESC LIMIT $6 OFFSET $7`, p.TenantID, workerID, status, from, to, page.Limit, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Challan{}
	for rows.Next() {
		c, err := scanChallan(rows)
		if err != nil {
			return nil, err
		}
		if canStore(p, c.StoreID) {
			out = append(out, *c)
		}
	}
	return out, rows.Err()
}

// CancelChallan cancels a challan nothing has come back against.
func (s *Service) CancelChallan(ctx context.Context, p *auth.Principal, id string) (*Challan, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status, store string
		err := tx.QueryRow(ctx, `SELECT status, store_id FROM job_work_challans WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&status, &store)
		if db.IsNotFound(err) || (err == nil && !canStore(p, store)) {
			return httpx.NotFound("challan not found")
		}
		if err != nil {
			return err
		}
		if status != StatusOpen {
			return httpx.Conflict("only an open challan with no receipts can be cancelled (it is %s)", status)
		}
		_, err = tx.Exec(ctx, `UPDATE job_work_challans SET status='cancelled', cancelled_at=now() WHERE id=$1`, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetChallan(ctx, p, id)
}

// ---------- receipts ----------

// ReceiptLineInput is one line received back.
type ReceiptLineInput struct {
	ChallanLineNo int     `json:"challan_line_no"`
	Kind          string  `json:"kind"`                // processed | returned | waste
	ItemID        string  `json:"item_id,omitempty"`   // processed: the item received (default: the item sent)
	ItemName      string  `json:"item_name,omitempty"` // default: bhandar name
	Qty           float64 `json:"qty"`                 // received qty (processed: in `unit`; returned/waste: in the challan unit)
	Unit          string  `json:"unit,omitempty"`      // processed only; default the item's stock unit
	InputQty      float64 `json:"input_qty,omitempty"` // processed: challan-line qty used up (default qty when same item & unit)
}

// ReceiptInput is the body of POST /v1/job-work/challans/{id}/receipts.
type ReceiptInput struct {
	ReceiptDate            string             `json:"receipt_date,omitempty"` // default today
	JWChallanNo            string             `json:"jw_challan_no,omitempty"`
	JWChallanDate          string             `json:"jw_challan_date,omitempty"`
	ProcessingChargesPaise int64              `json:"processing_charges_paise,omitempty"` // taxable value of the job worker's bill
	Notes                  string             `json:"notes,omitempty"`
	Lines                  []ReceiptLineInput `json:"lines"`
}

// Validate checks the body.
func (in *ReceiptInput) Validate() error {
	for _, d := range []string{in.ReceiptDate, in.JWChallanDate} {
		if d != "" {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return httpx.Validation("dates must be YYYY-MM-DD")
			}
		}
	}
	if in.ProcessingChargesPaise < 0 {
		return httpx.Validation("processing_charges_paise cannot be negative")
	}
	if len(in.Lines) == 0 {
		return httpx.Validation("lines are required")
	}
	for i, l := range in.Lines {
		switch {
		case l.ChallanLineNo <= 0:
			return httpx.Validation("line %d needs challan_line_no", i+1)
		case l.Kind != KindProcessed && l.Kind != KindReturned && l.Kind != KindWaste:
			return httpx.Validation("line %d kind must be processed, returned or waste", i+1)
		case l.Qty < 0 || l.InputQty < 0:
			return httpx.Validation("line %d quantities cannot be negative", i+1)
		case l.Kind != KindProcessed && l.Qty <= 0:
			return httpx.Validation("line %d needs a positive qty", i+1)
		}
	}
	return nil
}

// ReceiveEvent is nirmaan.production.completed for a job-work receipt: the
// inputs used up (processed + waste) leave stock and the processed goods
// arrive at input value + processing charges. Quantities are in stock units.
type ReceiveEvent struct {
	events.ProductionCompletedPayload
	JobWork          bool   `json:"job_work"`
	ChallanNo        string `json:"challan_no"`
	ReceiptNo        string `json:"receipt_no"`
	JobWorkerID      string `json:"job_worker_id"`
	ProcessingPaise  int64  `json:"processing_charges_paise"`
	InputsValuePaise int64  `json:"inputs_value_paise"`
}

// Receive records goods coming back from the job worker against a challan.
func (s *Service) Receive(ctx context.Context, p *auth.Principal, challanID string, in ReceiptInput) (*Challan, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	c, err := s.GetChallan(ctx, p, challanID)
	if err != nil {
		return nil, err
	}
	if c.Status == StatusCancelled || c.Status == StatusClosed {
		return nil, httpx.Conflict("challan %s is %s", c.ChallanNo, c.Status)
	}
	date := in.ReceiptDate
	if date == "" {
		date = s.today()
	}
	if date < c.ChallanDate {
		return nil, httpx.Validation("receipt_date is before the challan date")
	}
	byLine := map[int]ChallanLine{}
	for _, l := range c.Lines {
		byLine[l.LineNo] = l
	}

	// output items for processed lines: names and stock units from bhandar
	ids := []string{}
	for i := range in.Lines {
		if in.Lines[i].Kind == KindProcessed && in.Lines[i].ItemID != "" {
			ids = append(ids, in.Lines[i].ItemID)
		}
	}
	items := map[string]clients.Item{}
	if s.Inventory != nil && len(ids) > 0 {
		if items, err = s.Inventory.Lookup(ctx, ids); err != nil {
			return nil, err
		}
	}

	type rline struct {
		ReceiptLineInput
		unit       string
		baseQty    float64 // processed output in stock unit
		inputValue int64
	}
	settle := map[int]float64{}
	var rl []rline
	for i, l := range in.Lines {
		cl, ok := byLine[l.ChallanLineNo]
		if !ok {
			return nil, httpx.Validation("line %d: challan has no line %d", i+1, l.ChallanLineNo)
		}
		r := rline{ReceiptLineInput: l}
		switch l.Kind {
		case KindProcessed:
			if r.ItemID == "" {
				r.ItemID = cl.ItemID
			}
			it, known := items[r.ItemID]
			if r.ItemName == "" {
				r.ItemName = it.Name
			}
			if r.ItemID == cl.ItemID && r.ItemName == "" {
				r.ItemName = cl.ItemName
			}
			if strings.TrimSpace(r.ItemName) == "" {
				return nil, httpx.Validation("line %d: item_name is required (item not found in bhandar)", i+1)
			}
			r.unit = units.Norm(l.Unit)
			if r.unit == "" {
				r.unit = units.Norm(it.Unit)
			}
			if r.unit == "" && r.ItemID == cl.ItemID {
				r.unit = cl.Unit
			}
			if r.unit == "" {
				r.unit = "PCS"
			}
			if r.InputQty == 0 {
				if r.ItemID != cl.ItemID || r.unit != cl.Unit {
					return nil, httpx.Validation("line %d: input_qty is required when the processed goods differ from what was sent", i+1)
				}
				r.InputQty = l.Qty
			}
			if r.Qty <= 0 {
				return nil, httpx.Validation("line %d: processed qty must be positive", i+1)
			}
			f := 1.0
			switch {
			case known:
				if f, err = units.Factor(r.unit, it.Units()); err != nil {
					return nil, httpx.Validation("line %d %s: %s", i+1, r.ItemName, err.Error())
				}
			case r.ItemID == cl.ItemID && r.unit == cl.Unit:
				f = cl.Factor
			}
			r.baseQty = units.Round3(r.Qty * f)
		default: // returned / waste: the sent item, in the challan unit
			r.ItemID, r.ItemName, r.unit, r.InputQty = cl.ItemID, cl.ItemName, cl.Unit, l.Qty
		}
		r.InputQty = units.Round3(r.InputQty)
		settle[l.ChallanLineNo] += r.InputQty
		r.inputValue = units.MulDiv(cl.TaxableValuePaise, units.Milli(r.InputQty), units.Milli(cl.Qty))
		rl = append(rl, r)
	}
	for no, q := range settle {
		if cl := byLine[no]; units.Round3(q) > cl.Pending {
			return nil, httpx.Validation("challan line %d (%s): %s settled but only %s pending", no, cl.ItemName, qtyStr(q), qtyStr(cl.Pending))
		}
	}

	// value of processed goods = inputs used up (processed + waste) + processing charges,
	// spread over the processed lines by their input value
	var used int64
	var procIdx []int
	var weights []int64
	for i, r := range rl {
		if r.Kind == KindProcessed {
			procIdx = append(procIdx, i)
			weights = append(weights, r.inputValue+1) // +1: a zero-value input still gets a share
		}
		if r.Kind != KindReturned {
			used += r.inputValue
		}
	}
	shares := Allocate(used+in.ProcessingChargesPaise, weights)

	rd, _ := time.Parse(time.DateOnly, date)
	var receiptNo string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM job_work_challans WHERE id=$1 FOR UPDATE`, challanID).Scan(&status); err != nil {
			return err
		}
		if status != StatusOpen && status != StatusPartial {
			return httpx.Conflict("challan changed concurrently (now %s)", status)
		}
		// re-check pending under the lock
		fresh, err := loadChallanLines(ctx, tx, challanID, date)
		if err != nil {
			return err
		}
		for _, l := range fresh {
			if q := settle[l.LineNo]; q > 0 && units.Round3(q) > l.Pending {
				return httpx.Conflict("challan line %d changed concurrently", l.LineNo)
			}
		}
		if receiptNo, err = numbering.Next(ctx, tx, p.TenantID, numbering.JobWorkReceipt, rd); err != nil {
			return err
		}
		var rid string
		if err := tx.QueryRow(ctx, `INSERT INTO job_work_receipts (tenant_id, challan_id, receipt_no, receipt_date, jw_challan_no,
			jw_challan_date, processing_charges_paise, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8,$9) RETURNING id`,
			p.TenantID, challanID, receiptNo, date, nilIfEmpty(in.JWChallanNo), nilIfEmpty(in.JWChallanDate), in.ProcessingChargesPaise,
			nilIfEmpty(in.Notes), p.UserID()).Scan(&rid); err != nil {
			return err
		}
		pl := ReceiveEvent{
			ProductionCompletedPayload: events.ProductionCompletedPayload{ProductionRunID: rid, StoreID: c.StoreID,
				Consumed: []events.ProductionLine{}, Produced: []events.ProductionLine{}, TotalCostPaise: used + in.ProcessingChargesPaise,
				CompletedAt: rd.In(ist).Add(12 * time.Hour).UTC().Format(time.RFC3339)},
			JobWork: true, ChallanNo: c.ChallanNo, ReceiptNo: receiptNo, JobWorkerID: c.JobWorker.ID,
			ProcessingPaise: in.ProcessingChargesPaise, InputsValuePaise: used,
		}
		consumed := map[int]float64{} // challan line → input qty leaving stock
		for i, r := range rl {
			value := r.inputValue
			if r.Kind == KindProcessed {
				for k, idx := range procIdx {
					if idx == i {
						value = shares[k]
					}
				}
				pl.Produced = append(pl.Produced, events.ProductionLine{ItemID: r.ItemID, Qty: r.baseQty,
					UnitCostPaise: units.Rate(value, r.baseQty)})
			}
			if r.Kind != KindReturned {
				consumed[r.ChallanLineNo] += r.InputQty
			}
			if _, err := tx.Exec(ctx, `INSERT INTO job_work_receipt_lines (receipt_id, line_no, challan_line_no, kind, item_id,
				item_name, unit, qty, input_qty, value_paise) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				rid, i+1, r.ChallanLineNo, r.Kind, r.ItemID, strings.TrimSpace(r.ItemName), r.unit, units.Round3(r.Qty), r.InputQty, value); err != nil {
				return err
			}
		}
		for _, cl := range c.Lines {
			q := consumed[cl.LineNo]
			if q <= 0 {
				continue
			}
			base := units.Round3(q * cl.Factor)
			pl.Consumed = append(pl.Consumed, events.ProductionLine{ItemID: cl.ItemID, Qty: base,
				UnitCostPaise: units.Rate(cl.TaxableValuePaise, cl.Qty*cl.Factor)})
		}
		if len(pl.Consumed)+len(pl.Produced) > 0 {
			ev, err := events.New(events.ProductionCompleted, "nirmaan", p.TenantID, c.StoreID, pl)
			if err != nil {
				return err
			}
			if err := events.Enqueue(ctx, tx, ev); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE job_work_receipts SET posting_id=$2 WHERE id=$1`, rid, rid); err != nil {
				return err
			}
		}
		// close the challan when every line is settled
		after, err := loadChallanLines(ctx, tx, challanID, date)
		if err != nil {
			return err
		}
		newStatus := StatusClosed
		for _, l := range after {
			if l.Pending > 0 {
				newStatus = StatusPartial
			}
		}
		_, err = tx.Exec(ctx, `UPDATE job_work_challans SET status=$2 WHERE id=$1`, challanID, newStatus)
		return err
	})
	if err != nil {
		return nil, err
	}
	if s.Relay != nil {
		s.Relay.Kick()
	}
	return s.GetChallan(ctx, p, challanID)
}

// ---------- reports ----------

// Pending lists challan lines still with job workers, with GST return-limit status.
func (s *Service) Pending(ctx context.Context, p *auth.Principal, workerID, asOf string, includeSettled bool) ([]PendingLine, error) {
	if asOf == "" {
		asOf = s.today()
	} else if _, err := time.Parse(time.DateOnly, asOf); err != nil {
		return nil, httpx.Validation("as_of must be YYYY-MM-DD")
	}
	rows, err := s.DB.Query(ctx, `SELECT c.id, c.challan_no, to_char(c.challan_date,'YYYY-MM-DD'), c.store_id, w.id, w.name,
		l.line_no, l.item_id, l.item_name, l.unit, l.goods_type, l.qty::float8, l.taxable_value_paise, COALESCE(to_char(l.due_date,'YYYY-MM-DD'),''),
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='processed' AND r.receipt_date <= $3::date),0)::float8,
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='returned' AND r.receipt_date <= $3::date),0)::float8,
		COALESCE(sum(rl.input_qty) FILTER (WHERE rl.kind='waste' AND r.receipt_date <= $3::date),0)::float8
		FROM job_work_challans c JOIN job_workers w ON w.id=c.job_worker_id
		JOIN job_work_challan_lines l ON l.challan_id=c.id
		LEFT JOIN job_work_receipts r ON r.challan_id=c.id
		LEFT JOIN job_work_receipt_lines rl ON rl.receipt_id=r.id AND rl.challan_line_no=l.line_no
		WHERE c.tenant_id=$1 AND c.status <> 'cancelled' AND ($2 = '' OR c.job_worker_id::text=$2) AND c.challan_date <= $3::date
		GROUP BY c.id, w.id, l.challan_id, l.line_no
		ORDER BY w.name, c.challan_date, c.challan_no, l.line_no`, p.TenantID, workerID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingLine{}
	for rows.Next() {
		var pl PendingLine
		var store string
		var value int64
		if err := rows.Scan(&pl.ChallanID, &pl.ChallanNo, &pl.ChallanDate, &store, &pl.JobWorkerID, &pl.JobWorkerName, &pl.LineNo,
			&pl.ItemID, &pl.ItemName, &pl.Unit, &pl.GoodsType, &pl.Sent, &value, &pl.DueDate, &pl.Processed, &pl.Returned, &pl.Waste); err != nil {
			return nil, err
		}
		if !canStore(p, store) {
			continue
		}
		pl.Settle(value, asOf)
		if pl.Pending > 0 || includeSettled {
			out = append(out, pl)
		}
	}
	return out, rows.Err()
}

// ITC04 builds tables 4 and 5A for a period.
func (s *Service) ITC04(ctx context.Context, tenantID string, per Period) ([]Table4Row, []Table5ARow, error) {
	rows, err := s.DB.Query(ctx, `SELECT COALESCE(w.gstin,''), w.state_code, c.challan_no, to_char(c.challan_date,'YYYY-MM-DD'),
		COALESCE(c.principal_state,''), l.goods_type, l.item_name, COALESCE(l.hsn,''), l.unit, l.qty::float8, l.taxable_value_paise, l.tax_rate_bp
		FROM job_work_challans c JOIN job_workers w ON w.id=c.job_worker_id JOIN job_work_challan_lines l ON l.challan_id=c.id
		WHERE c.tenant_id=$1 AND c.status <> 'cancelled' AND c.challan_date BETWEEN $2::date AND $3::date
		ORDER BY c.challan_date, c.challan_no, l.line_no`, tenantID, per.From, per.To)
	if err != nil {
		return nil, nil, err
	}
	t4 := []Table4Row{}
	for rows.Next() {
		var r Table4Row
		var principal, unit string
		var rate int
		if err := rows.Scan(&r.JobWorkerGSTIN, &r.JobWorkerState, &r.ChallanNo, &r.ChallanDate, &principal, &r.GoodsType,
			&r.Description, &r.HSN, &unit, &r.Qty, &r.TaxablePaise, &rate); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.UQC = units.UQC(unit)
		r.IGSTRateBP, r.CGSTRateBP, r.SGSTRateBP = TaxSplit(rate, principal != "" && principal != r.JobWorkerState)
		t4 = append(t4, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = s.DB.Query(ctx, `SELECT COALESCE(w.gstin,''), w.state_code, c.challan_no, to_char(c.challan_date,'YYYY-MM-DD'),
		COALESCE(r.jw_challan_no,''), COALESCE(to_char(r.jw_challan_date,'YYYY-MM-DD'),''), c.nature_of_processing,
		rl.item_name, rl.unit, rl.qty::float8, r.receipt_no, to_char(r.receipt_date,'YYYY-MM-DD'), rl.challan_line_no, rl.kind, r.id
		FROM job_work_receipts r JOIN job_work_challans c ON c.id=r.challan_id JOIN job_workers w ON w.id=c.job_worker_id
		JOIN job_work_receipt_lines rl ON rl.receipt_id=r.id
		WHERE r.tenant_id=$1 AND r.receipt_date BETWEEN $2::date AND $3::date
		ORDER BY r.receipt_date, r.receipt_no, rl.line_no`, tenantID, per.From, per.To)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var recv []ReceivedLine
	for rows.Next() {
		var l ReceivedLine
		var unit string
		if err := rows.Scan(&l.JobWorkerGSTIN, &l.JobWorkerState, &l.OrigChallanNo, &l.OrigChallanDate, &l.JWChallanNo,
			&l.JWChallanDate, &l.NatureOfJobWork, &l.Description, &unit, &l.Qty, &l.ReceiptNo, &l.ReceiptDate,
			&l.ChallanLineNo, &l.Kind, &l.ReceiptID); err != nil {
			return nil, nil, err
		}
		l.UQC = units.UQC(unit)
		recv = append(recv, l)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	t5 := BuildTable5A(recv)
	if t5 == nil {
		t5 = []Table5ARow{}
	}
	return t4, t5, nil
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
