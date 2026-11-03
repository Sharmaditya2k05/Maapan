package ledger

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// BankTxn is a deposit/withdrawal/charge/interest on a bank account.
type BankTxn struct {
	ID            string  `json:"id"`
	BankAccountID string  `json:"bank_account_id"`
	Kind          string  `json:"kind"`
	AmountPaise   int64   `json:"amount_paise"`
	Date          string  `json:"date"`
	Reference     string  `json:"reference,omitempty"`
	VoucherID     string  `json:"voucher_id"`
	ReconciledAt  *string `json:"reconciled_at,omitempty"`
}

type bankTxnInput struct {
	BankAccountID    string `json:"bank_account_id"`
	Kind             string `json:"kind"` // deposit | withdrawal | charges | interest
	AmountPaise      int64  `json:"amount_paise"`
	Date             string `json:"date"`
	Reference        string `json:"reference"`
	CounterAccountID string `json:"counter_account_id"` // default: Cash (deposit/withdrawal), Bank Charges, Other Income
}

func (in *bankTxnInput) Validate() error {
	switch in.Kind {
	case "deposit", "withdrawal", "charges", "interest":
	default:
		return httpx.Validation("kind must be deposit, withdrawal, charges or interest")
	}
	if in.BankAccountID == "" || in.AmountPaise <= 0 {
		return httpx.Validation("bank_account_id and a positive amount_paise are required")
	}
	if in.Date != "" {
		if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
			return httpx.Validation("date must be YYYY-MM-DD")
		}
	}
	return nil
}

func (s *Service) requireBank(ctx context.Context, q db.Querier, tenantID, id string) error {
	var ok bool
	err := q.QueryRow(ctx, `SELECT is_bank AND NOT is_group FROM accounts WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&ok)
	if err != nil || !ok {
		return httpx.Validation("bank_account_id must be a bank account")
	}
	return nil
}

func (s *Service) handleCreateBankTxn(w http.ResponseWriter, r *http.Request) error {
	var in bankTxnInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.BankAccountID); err != nil {
		return err
	}
	date := orDefault(in.Date, s.today())
	bank := posting.Entry{AccountID: in.BankAccountID}
	counter := posting.Entry{AccountID: in.CounterAccountID}
	if in.CounterAccountID == "" {
		switch in.Kind {
		case "deposit", "withdrawal":
			counter = posting.Entry{AccountCode: posting.Cash}
		case "charges":
			counter = posting.Entry{AccountCode: posting.BankCharges}
		case "interest":
			counter = posting.Entry{AccountCode: posting.OtherIncome}
		}
	}
	vtype := posting.VContra
	switch in.Kind {
	case "deposit", "interest":
		bank.Debit, counter.Credit = in.AmountPaise, in.AmountPaise
	default:
		bank.Credit, counter.Debit = in.AmountPaise, in.AmountPaise
	}
	if in.Kind == "charges" || in.Kind == "interest" {
		vtype = posting.VJournal
	}
	v := posting.Voucher{Type: vtype, Date: date, Source: posting.SrcBank, SourceRef: in.Reference,
		Narration: "Bank " + in.Kind + " " + in.Reference, Entries: []posting.Entry{bank, counter}}
	var t BankTxn
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		posted, err := Post(r.Context(), tx, p.TenantID, p.UserID(), v)
		if err != nil {
			return err
		}
		t.VoucherID = posted.ID
		return tx.QueryRow(r.Context(), `INSERT INTO bank_transactions (tenant_id, bank_account_id, kind, amount_paise, txn_date, reference, voucher_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, p.TenantID, in.BankAccountID, in.Kind, in.AmountPaise, date,
			nilIfEmpty(in.Reference), posted.ID).Scan(&t.ID)
	})
	if err != nil {
		return err
	}
	t.BankAccountID, t.Kind, t.AmountPaise, t.Date, t.Reference = in.BankAccountID, in.Kind, in.AmountPaise, date, in.Reference
	return httpx.Created(w, t)
}

func (s *Service) handleListBankTxns(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	args := []any{p.TenantID, from, to}
	cond := "tenant_id=$1 AND txn_date BETWEEN $2 AND $3"
	if id := httpx.Query(r, "bank_account_id"); id != "" {
		args = append(args, id)
		cond += fmt.Sprintf(" AND bank_account_id=$%d", len(args))
	}
	switch httpx.Query(r, "reconciled") {
	case "true":
		cond += " AND reconciled_at IS NOT NULL"
	case "false":
		cond += " AND reconciled_at IS NULL"
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id, bank_account_id, kind, amount_paise, txn_date, COALESCE(reference,''),
		voucher_id, reconciled_at FROM bank_transactions WHERE `+cond+` ORDER BY txn_date, created_at LIMIT 2000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []BankTxn{}
	for rows.Next() {
		var t BankTxn
		var d time.Time
		var rec *time.Time
		if err := rows.Scan(&t.ID, &t.BankAccountID, &t.Kind, &t.AmountPaise, &d, &t.Reference, &t.VoucherID, &rec); err != nil {
			return err
		}
		t.Date = d.Format(time.DateOnly)
		if rec != nil {
			s := rec.Format(time.DateOnly)
			t.ReconciledAt = &s
		}
		items = append(items, t)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

type reconcileInput struct {
	ClearedOn string `json:"cleared_on"`
}

func (s *Service) handleReconcileBankTxn(w http.ResponseWriter, r *http.Request) error {
	var in reconcileInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, in.ClearedOn); err != nil {
		return httpx.Validation("cleared_on must be YYYY-MM-DD")
	}
	p := auth.FromContext(r.Context())
	tag, err := s.DB.Exec(r.Context(), `UPDATE bank_transactions SET reconciled_at=$3 WHERE tenant_id=$1 AND id=$2`,
		p.TenantID, r.PathValue("id"), in.ClearedOn)
	if isBadUUID(err) || (err == nil && tag.RowsAffected() == 0) {
		return httpx.NotFound("bank transaction not found")
	}
	if err != nil {
		return err
	}
	return httpx.NoContent(w)
}
