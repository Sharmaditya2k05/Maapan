package ledger

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// Cheque is a post-dated / received / issued cheque.
type Cheque struct {
	ID            string  `json:"id"`
	Direction     string  `json:"direction"`
	PartyID       string  `json:"party_id"`
	PartyName     string  `json:"party_name"`
	BankAccountID string  `json:"bank_account_id"`
	ChequeNo      string  `json:"cheque_no"`
	ChequeDate    string  `json:"cheque_date"`
	DraweeBank    string  `json:"drawee_bank,omitempty"`
	AmountPaise   int64   `json:"amount_paise"`
	Status        string  `json:"status"`
	VoucherID     *string `json:"voucher_id,omitempty"`
	StatusDate    *string `json:"status_date,omitempty"`
	Notes         string  `json:"notes,omitempty"`
}

type chequeInput struct {
	Direction     string `json:"direction"` // received | issued
	PartyID       string `json:"party_id"`
	BankAccountID string `json:"bank_account_id"`
	ChequeNo      string `json:"cheque_no"`
	ChequeDate    string `json:"cheque_date"`
	DraweeBank    string `json:"drawee_bank"`
	AmountPaise   int64  `json:"amount_paise"`
	Notes         string `json:"notes"`
}

func (in *chequeInput) Validate() error {
	if in.Direction != "received" && in.Direction != "issued" {
		return httpx.Validation("direction must be received or issued")
	}
	if in.PartyID == "" || in.BankAccountID == "" || strings.TrimSpace(in.ChequeNo) == "" || in.AmountPaise <= 0 {
		return httpx.Validation("party_id, bank_account_id, cheque_no and a positive amount_paise are required")
	}
	if _, err := time.Parse(time.DateOnly, in.ChequeDate); err != nil {
		return httpx.Validation("cheque_date must be YYYY-MM-DD")
	}
	return nil
}

const chequeCols = `c.id, c.direction, c.party_id, p.name, c.bank_account_id, c.cheque_no, c.cheque_date,
	COALESCE(c.drawee_bank,''), c.amount_paise, c.status, c.voucher_id, c.status_date, COALESCE(c.notes,'')`

func scanCheque(row pgx.Row) (*Cheque, error) {
	var c Cheque
	var cd time.Time
	var sd *time.Time
	if err := row.Scan(&c.ID, &c.Direction, &c.PartyID, &c.PartyName, &c.BankAccountID, &c.ChequeNo, &cd, &c.DraweeBank,
		&c.AmountPaise, &c.Status, &c.VoucherID, &sd, &c.Notes); err != nil {
		return nil, err
	}
	c.ChequeDate = cd.Format(time.DateOnly)
	if sd != nil {
		s := sd.Format(time.DateOnly)
		c.StatusDate = &s
	}
	return &c, nil
}

func (s *Service) getCheque(ctx context.Context, q db.Querier, tenantID, id string) (*Cheque, error) {
	c, err := scanCheque(q.QueryRow(ctx, `SELECT `+chequeCols+` FROM cheques c JOIN parties p ON p.id=c.party_id
		WHERE c.tenant_id=$1 AND c.id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("cheque not found")
	}
	return c, err
}

func (s *Service) handleCreateCheque(w http.ResponseWriter, r *http.Request) error {
	var in chequeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.BankAccountID); err != nil {
		return err
	}
	if _, err := s.getParty(r.Context(), s.DB, p.TenantID, in.PartyID); err != nil {
		return err
	}
	var id string
	err := s.DB.QueryRow(r.Context(), `INSERT INTO cheques (tenant_id, direction, party_id, bank_account_id, cheque_no,
		cheque_date, drawee_bank, amount_paise, status, notes, status_date) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$2,$9,$10) RETURNING id`,
		p.TenantID, in.Direction, in.PartyID, in.BankAccountID, strings.TrimSpace(in.ChequeNo), in.ChequeDate,
		nilIfEmpty(in.DraweeBank), in.AmountPaise, nilIfEmpty(in.Notes), s.today()).Scan(&id)
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("cheque %s already recorded", in.ChequeNo)
	}
	if err != nil {
		return err
	}
	c, err := s.getCheque(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.Created(w, c)
}

func (s *Service) handleListCheques(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	args := []any{p.TenantID}
	cond := "c.tenant_id=$1"
	if st := httpx.Query(r, "status"); st != "" {
		args = append(args, st)
		cond += fmt.Sprintf(" AND c.status=$%d", len(args))
	}
	if d := httpx.Query(r, "direction"); d != "" {
		args = append(args, d)
		cond += fmt.Sprintf(" AND c.direction=$%d", len(args))
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+chequeCols+` FROM cheques c JOIN parties p ON p.id=c.party_id
		WHERE `+cond+` ORDER BY c.cheque_date LIMIT 2000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Cheque{}
	for rows.Next() {
		c, err := scanCheque(rows)
		if err != nil {
			return err
		}
		items = append(items, *c)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// chequeTransitions lists allowed status moves per direction.
var chequeTransitions = map[string]map[string][]string{
	"received": {"received": {"deposited", "cancelled"}, "deposited": {"cleared", "bounced"}},
	"issued":   {"issued": {"cleared", "bounced", "cancelled"}},
}

type chequeStatusInput struct {
	Status string `json:"status"`
	Date   string `json:"date"`
	Notes  string `json:"notes"`
}

// handleChequeStatus moves a cheque through its lifecycle. Money is posted only
// when it clears: received → Dr Bank / Cr Party (receipt); issued → Dr Party / Cr Bank (payment).
func (s *Service) handleChequeStatus(w http.ResponseWriter, r *http.Request) error {
	var in chequeStatusInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	date := orDefault(in.Date, s.today())
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return httpx.Validation("date must be YYYY-MM-DD")
	}
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM cheques WHERE tenant_id=$1 AND id::text=$2 FOR UPDATE`, p.TenantID, id); err != nil {
			return err
		}
		c, err := s.getCheque(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		allowed := false
		for _, next := range chequeTransitions[c.Direction][c.Status] {
			allowed = allowed || next == in.Status
		}
		if !allowed {
			return httpx.Conflict("cannot move a %s cheque from %s to %s", c.Direction, c.Status, in.Status)
		}
		var voucherID *string
		if in.Status == "cleared" {
			bank := posting.Entry{AccountID: c.BankAccountID}
			party := posting.Entry{PartyID: c.PartyID}
			vtype := posting.VReceipt
			if c.Direction == "received" {
				bank.Debit, party.Credit = c.AmountPaise, c.AmountPaise
			} else {
				bank.Credit, party.Debit, vtype = c.AmountPaise, c.AmountPaise, posting.VPayment
			}
			posted, err := Post(r.Context(), tx, p.TenantID, p.UserID(), posting.Voucher{Type: vtype, Date: date,
				Source: posting.SrcCheque, SourceID: c.ID, SourceRef: "Cheque " + c.ChequeNo,
				Narration: fmt.Sprintf("Cheque %s %s — %s", c.ChequeNo, c.Direction, c.PartyName),
				Entries:   []posting.Entry{bank, party}})
			if err != nil {
				return err
			}
			voucherID = &posted.ID
		}
		_, err = tx.Exec(r.Context(), `UPDATE cheques SET status=$3, status_date=$4, voucher_id=COALESCE($5,voucher_id),
			reconciled_at = CASE WHEN $3='cleared' THEN $4::date ELSE reconciled_at END,
			notes=COALESCE($6,notes) WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, id, in.Status, date, voucherID, nilIfEmpty(in.Notes))
		return err
	})
	if err != nil {
		return err
	}
	c, err := s.getCheque(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, c)
}
