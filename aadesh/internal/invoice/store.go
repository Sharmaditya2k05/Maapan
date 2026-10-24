package invoice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/gst"
)

const invoiceCols = `id, store_id, doc_type, number, fy, invoice_date, due_date, supplier_invoice_no, supplier_invoice_date,
	ref_invoice_id, party_id, party_name, party_phone, party_gstin, party_state, store_state, store_gstin, place_of_supply,
	inter_state, supply_category, reverse_charge, status, payment_status, taxable_paise, discount_paise, invoice_discount_paise,
	cgst_paise, sgst_paise, igst_paise, cess_paise, tcs_paise, round_off_paise, grand_total_paise, paid_paise, change_paise,
	credited_paise, irn, notes, client_uuid, created_by, created_at, cancelled_at, cancel_reason`

func scanInvoice(row pgx.Row) (*Invoice, error) {
	var inv Invoice
	var date time.Time
	var due, supDate *time.Time
	err := row.Scan(&inv.ID, &inv.StoreID, &inv.DocType, &inv.Number, &inv.FY, &date, &due, &inv.SupplierInvoiceNo, &supDate,
		&inv.RefInvoiceID, &inv.PartyID, &inv.PartyName, &inv.PartyPhone, &inv.PartyGSTIN, &inv.PartyState, &inv.StoreState,
		&inv.StoreGSTIN, &inv.PlaceOfSupply, &inv.InterState, &inv.SupplyCategory, &inv.ReverseCharge, &inv.Status,
		&inv.PaymentStatus, &inv.TaxablePaise, &inv.DiscountPaise, &inv.InvoiceDiscountPaise, &inv.CGSTPaise, &inv.SGSTPaise,
		&inv.IGSTPaise, &inv.CessPaise, &inv.TCSPaise, &inv.RoundOffPaise, &inv.GrandTotalPaise, &inv.PaidPaise, &inv.ChangePaise,
		&inv.CreditedPaise, &inv.IRN, &inv.Notes, &inv.ClientUUID, &inv.CreatedBy, &inv.CreatedAt, &inv.CancelledAt, &inv.CancelReason)
	if err != nil {
		return nil, err
	}
	inv.InvoiceDate = date.Format(time.DateOnly)
	if due != nil {
		s := due.Format(time.DateOnly)
		inv.DueDate = &s
	}
	if supDate != nil {
		s := supDate.Format(time.DateOnly)
		inv.SupplierInvoiceDate = &s
	}
	inv.BalancePaise = balance(inv.GrandTotalPaise, inv.CreditedPaise, inv.PaidPaise)
	inv.AmountInWords = gst.AmountInWords(inv.GrandTotalPaise)
	return &inv, nil
}

func balance(grand, credited, paid int64) int64 {
	b := grand - credited - paid
	if b < 0 {
		return 0
	}
	return b
}

func dateOrNil(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	return &t
}

func insertInvoice(ctx context.Context, tx pgx.Tx, tenantID string, seq int64, in CreateInput, inv *Invoice) error {
	err := tx.QueryRow(ctx, `INSERT INTO invoices (
		tenant_id, store_id, doc_type, number, seq_no, fy, invoice_date, due_date, supplier_invoice_no, supplier_invoice_date,
		ref_invoice_id, party_id, party_name, party_phone, party_gstin, party_state, store_state, store_gstin, place_of_supply,
		inter_state, supply_category, reverse_charge, payment_status, taxable_paise, discount_paise, invoice_discount_paise,
		cgst_paise, sgst_paise, igst_paise, cess_paise, tcs_paise, round_off_paise, grand_total_paise, paid_paise, change_paise,
		notes, client_uuid, device_id, created_by, client_created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,
		        $31,$32,$33,$34,$35,$36,$37,$38,$39,$40)
		RETURNING id, created_at`,
		tenantID, inv.StoreID, inv.DocType, inv.Number, seq, inv.FY, inv.InvoiceDate, dateOrNil(deref(inv.DueDate)),
		inv.SupplierInvoiceNo, dateOrNil(deref(inv.SupplierInvoiceDate)), inv.RefInvoiceID, inv.PartyID, inv.PartyName,
		inv.PartyPhone, inv.PartyGSTIN, inv.PartyState, inv.StoreState, inv.StoreGSTIN, inv.PlaceOfSupply, inv.InterState,
		inv.SupplyCategory, inv.ReverseCharge, inv.PaymentStatus, inv.TaxablePaise, inv.DiscountPaise, inv.InvoiceDiscountPaise,
		inv.CGSTPaise, inv.SGSTPaise, inv.IGSTPaise, inv.CessPaise, inv.TCSPaise, inv.RoundOffPaise, inv.GrandTotalPaise,
		inv.PaidPaise, inv.ChangePaise, inv.Notes, inv.ClientUUID, nilIfEmpty(in.DeviceID), inv.CreatedBy, in.ClientCreatedAt,
	).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, l := range inv.Lines {
		batch.Queue(`INSERT INTO invoice_lines (invoice_id, line_no, item_id, name, hsn, unit, batch_no, qty,
			unit_price_paise, discount_paise, discount_bp, tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category,
			price_includes_tax, taxable_paise, cgst_paise, sgst_paise, igst_paise, cess_paise, total_paise)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
			inv.ID, l.LineNo, l.ItemID, l.Name, l.HSN, l.Unit, l.BatchNo, l.Qty, l.UnitPricePaise, l.DiscountPaise, l.DiscountBP,
			l.TaxRateBP, l.CessRateBP, l.CessPerUnitPaise, l.TaxCategory, l.PriceIncludesTax, l.TaxablePaise, l.CGSTPaise,
			l.SGSTPaise, l.IGSTPaise, l.CessPaise, l.TotalPaise)
	}
	for _, t := range inv.Tenders {
		batch.Queue(`INSERT INTO invoice_tenders (invoice_id, mode, amount_paise, reference, payment_request_id) VALUES ($1,$2,$3,$4,$5)`,
			inv.ID, t.Mode, t.AmountPaise, nilIfEmpty(t.Reference), nilIfEmpty(t.PaymentRequestID))
	}
	return tx.SendBatch(ctx, batch).Close()
}

const lineCols = `line_no, item_id, name, hsn, unit, batch_no, qty::float8, unit_price_paise, discount_paise, discount_bp,
	tax_rate_bp, cess_rate_bp, cess_per_unit_paise, tax_category, price_includes_tax, taxable_paise, cgst_paise, sgst_paise,
	igst_paise, cess_paise, total_paise`

func scanLine(rows pgx.Rows, l *Line) error {
	return rows.Scan(&l.LineNo, &l.ItemID, &l.Name, &l.HSN, &l.Unit, &l.BatchNo, &l.Qty, &l.UnitPricePaise, &l.DiscountPaise,
		&l.DiscountBP, &l.TaxRateBP, &l.CessRateBP, &l.CessPerUnitPaise, &l.TaxCategory, &l.PriceIncludesTax, &l.TaxablePaise,
		&l.CGSTPaise, &l.SGSTPaise, &l.IGSTPaise, &l.CessPaise, &l.TotalPaise)
}

func getInvoice(ctx context.Context, q db.Querier, tenantID, id string) (*Invoice, error) {
	inv, err := scanInvoice(q.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+lineCols+` FROM invoice_lines WHERE invoice_id=$1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Line
		if err := scanLine(rows, &l); err != nil {
			return nil, err
		}
		inv.Lines = append(inv.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := q.Query(ctx, `SELECT mode, amount_paise, COALESCE(reference,''), COALESCE(payment_request_id,'') FROM invoice_tenders WHERE invoice_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var t Tender
		if err := trows.Scan(&t.Mode, &t.AmountPaise, &t.Reference, &t.PaymentRequestID); err != nil {
			return nil, err
		}
		inv.Tenders = append(inv.Tenders, t)
	}
	return inv, trows.Err()
}

// List returns invoices (without lines) matching f.
func List(ctx context.Context, pool *pgxpool.Pool, tenantID string, f Filter) ([]Invoice, int, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.DocType != "" {
		add("doc_type = $%d", f.DocType)
	}
	if f.StoreID != "" {
		add("store_id = $%d", f.StoreID)
	}
	if f.PartyID != "" {
		add("party_id = $%d", f.PartyID)
	}
	if f.RefInvoiceID != "" {
		add("ref_invoice_id = $%d", f.RefInvoiceID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.PaymentStatus != "" {
		add("payment_status = $%d", f.PaymentStatus)
	}
	if f.From != "" {
		add("invoice_date >= $%d", f.From)
	}
	if f.To != "" {
		add("invoice_date <= $%d", f.To)
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(number ILIKE $%d OR party_name ILIKE $%d OR party_phone ILIKE $%d)", n, n, n))
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM invoices WHERE %s
		ORDER BY invoice_date DESC, seq_no DESC LIMIT $%d OFFSET $%d`, invoiceCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *inv)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if f.IncludeLines && len(out) > 0 {
		if err := attachLines(ctx, pool, out); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// attachLines loads lines and tenders for a page of invoices in two queries.
func attachLines(ctx context.Context, q db.Querier, invs []Invoice) error {
	ids := make([]string, len(invs))
	pos := make(map[string]int, len(invs))
	for i, inv := range invs {
		ids[i] = inv.ID
		pos[inv.ID] = i
	}
	rows, err := q.Query(ctx, `SELECT invoice_id, `+lineCols+` FROM invoice_lines WHERE invoice_id = ANY($1::uuid[]) ORDER BY invoice_id, line_no`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var l Line
		if err := rows.Scan(&id, &l.LineNo, &l.ItemID, &l.Name, &l.HSN, &l.Unit, &l.BatchNo, &l.Qty, &l.UnitPricePaise, &l.DiscountPaise,
			&l.DiscountBP, &l.TaxRateBP, &l.CessRateBP, &l.CessPerUnitPaise, &l.TaxCategory, &l.PriceIncludesTax, &l.TaxablePaise,
			&l.CGSTPaise, &l.SGSTPaise, &l.IGSTPaise, &l.CessPaise, &l.TotalPaise); err != nil {
			return err
		}
		invs[pos[id]].Lines = append(invs[pos[id]].Lines, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	trows, err := q.Query(ctx, `SELECT invoice_id, mode, amount_paise, COALESCE(reference,''), COALESCE(payment_request_id,'') FROM invoice_tenders
		WHERE invoice_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var id string
		var t Tender
		if err := trows.Scan(&id, &t.Mode, &t.AmountPaise, &t.Reference, &t.PaymentRequestID); err != nil {
			return err
		}
		invs[pos[id]].Tenders = append(invs[pos[id]].Tenders, t)
	}
	return trows.Err()
}
