package ledger

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/httpx"
)

// StatementLine is one posting with the running balance (debit − credit).
type StatementLine struct {
	Date          string `json:"date"`
	VoucherID     string `json:"voucher_id"`
	VoucherNumber string `json:"voucher_number"`
	VoucherType   string `json:"voucher_type"`
	Narration     string `json:"narration"`
	Reference     string `json:"reference,omitempty"`
	DebitPaise    int64  `json:"debit_paise"`
	CreditPaise   int64  `json:"credit_paise"`
	BalancePaise  int64  `json:"balance_paise"`
}

// Statement is a ledger for one or more accounts over a period.
type Statement struct {
	AccountID    string          `json:"account_id,omitempty"`
	AccountName  string          `json:"account_name"`
	From         string          `json:"from"`
	To           string          `json:"to"`
	OpeningPaise int64           `json:"opening_balance_paise"`
	TotalDebit   int64           `json:"total_debit_paise"`
	TotalCredit  int64           `json:"total_credit_paise"`
	ClosingPaise int64           `json:"closing_balance_paise"`
	Lines        []StatementLine `json:"lines"`
}

// statement builds a running-balance ledger for the given accounts.
func (s *Service) statement(ctx context.Context, tenantID string, accountIDs []string, from, to string) (*Statement, error) {
	st := &Statement{From: from, To: to, Lines: []StatementLine{}}
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(sum(debit_paise - credit_paise),0) FROM voucher_entries
		WHERE tenant_id=$1 AND account_id = ANY($2::uuid[]) AND vdate < $3`, tenantID, accountIDs, from).Scan(&st.OpeningPaise); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT e.vdate, v.id, v.number, v.vtype, COALESCE(v.narration,''), COALESCE(v.source_ref,''),
		e.debit_paise, e.credit_paise
		FROM voucher_entries e JOIN vouchers v ON v.id=e.voucher_id
		WHERE e.tenant_id=$1 AND e.account_id = ANY($2::uuid[]) AND e.vdate BETWEEN $3 AND $4
		ORDER BY e.vdate, v.created_at, e.line_no`, tenantID, accountIDs, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bal := st.OpeningPaise
	for rows.Next() {
		var l StatementLine
		var d time.Time
		if err := rows.Scan(&d, &l.VoucherID, &l.VoucherNumber, &l.VoucherType, &l.Narration, &l.Reference,
			&l.DebitPaise, &l.CreditPaise); err != nil {
			return nil, err
		}
		l.Date = d.Format(time.DateOnly)
		bal += l.DebitPaise - l.CreditPaise
		l.BalancePaise = bal
		st.TotalDebit += l.DebitPaise
		st.TotalCredit += l.CreditPaise
		st.Lines = append(st.Lines, l)
	}
	st.ClosingPaise = bal
	return st, rows.Err()
}

func (s *Service) writeStatement(w http.ResponseWriter, r *http.Request, st *Statement, name string) error {
	if !wantsCSV(r) {
		return httpx.OK(w, st)
	}
	rows := [][]string{{st.From, "", "", "Opening balance", "", "", rupees(st.OpeningPaise)}}
	for _, l := range st.Lines {
		rows = append(rows, []string{l.Date, l.VoucherNumber, l.VoucherType, l.Narration, rupees(l.DebitPaise),
			rupees(l.CreditPaise), rupees(l.BalancePaise)})
	}
	rows = append(rows, []string{st.To, "", "", "Closing balance", rupees(st.TotalDebit), rupees(st.TotalCredit), rupees(st.ClosingPaise)})
	return writeCSV(w, name, []string{"Date", "Voucher", "Type", "Narration", "Debit", "Credit", "Balance (Dr+)"}, rows)
}

func (s *Service) handleAccountStatement(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	var name string
	var isGroup bool
	err = s.DB.QueryRow(r.Context(), `SELECT name, is_group FROM accounts WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&name, &isGroup)
	if db.IsNotFound(err) || isBadUUID(err) {
		return httpx.NotFound("account not found")
	}
	if err != nil {
		return err
	}
	ids := []string{id}
	if isGroup {
		if ids, err = s.descendants(r.Context(), p.TenantID, id); err != nil {
			return err
		}
	}
	st, err := s.statement(r.Context(), p.TenantID, ids, from, to)
	if err != nil {
		return err
	}
	st.AccountID, st.AccountName = id, name
	return s.writeStatement(w, r, st, "ledger-"+name)
}

func (s *Service) handlePartyStatement(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	st, err := s.statement(r.Context(), p.TenantID, []string{party.AccountID}, from, to)
	if err != nil {
		return err
	}
	st.AccountID, st.AccountName = party.AccountID, party.Name
	return s.writeStatement(w, r, st, "party-"+party.Name)
}

// handleCashbook is the statement of all cash accounts (or ?account_id= one of them).
func (s *Service) handleCashbook(w http.ResponseWriter, r *http.Request) error {
	return s.moneyBook(w, r, "is_cash", "Cash Book")
}

// handleBankbook is the statement of a bank account (?account_id=) or all banks.
func (s *Service) handleBankbook(w http.ResponseWriter, r *http.Request) error {
	return s.moneyBook(w, r, "is_bank", "Bank Book")
}

func (s *Service) moneyBook(w http.ResponseWriter, r *http.Request, flag, title string) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	var ids []string
	if id := httpx.Query(r, "account_id"); id != "" {
		var ok bool
		err := s.DB.QueryRow(r.Context(), `SELECT `+flag+` FROM accounts WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&ok)
		if err != nil || !ok {
			return httpx.Validation("account_id is not a %s account", title)
		}
		ids = []string{id}
	} else {
		rows, err := s.DB.Query(r.Context(), `SELECT id FROM accounts WHERE tenant_id=$1 AND `+flag+` AND NOT is_group`, p.TenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	st, err := s.statement(r.Context(), p.TenantID, ids, from, to)
	if err != nil {
		return err
	}
	st.AccountName = title
	return s.writeStatement(w, r, st, title)
}

// descendants returns the leaf (postable) accounts under a group.
func (s *Service) descendants(ctx context.Context, tenantID, groupID string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `WITH RECURSIVE t AS (
			SELECT id, is_group FROM accounts WHERE tenant_id=$1 AND id=$2
			UNION ALL
			SELECT a.id, a.is_group FROM accounts a JOIN t ON a.parent_id=t.id WHERE a.tenant_id=$1)
		SELECT id FROM t WHERE NOT is_group`, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DayBookEntry is a voucher in the day book.
type DayBookEntry = VoucherView

// handleDayBook lists every voucher with entries in the period (default: today).
func (s *Service) handleDayBook(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if r.URL.Query().Get("from") == "" {
		q := r.URL.Query()
		q.Set("from", orDefault(q.Get("to"), s.today()))
		r.URL.RawQuery = q.Encode()
	}
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+voucherCols+` FROM vouchers v WHERE v.tenant_id=$1 AND v.vdate BETWEEN $2 AND $3
		ORDER BY v.vdate, v.created_at LIMIT 5000`, p.TenantID, from, to)
	if err != nil {
		return err
	}
	var vs []*VoucherView
	var ids []string
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			rows.Close()
			return err
		}
		vs = append(vs, v)
		ids = append(ids, v.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	es, err := loadEntries(r.Context(), s.DB, ids)
	if err != nil {
		return err
	}
	for _, v := range vs {
		v.Entries = es[v.ID]
	}
	if wantsCSV(r) {
		var out [][]string
		for _, v := range vs {
			for _, e := range v.Entries {
				out = append(out, []string{v.Date, v.Number, v.Type, v.Narration, e.AccountCode, e.AccountName,
					rupees(e.DebitPaise), rupees(e.CreditPaise)})
			}
		}
		return writeCSV(w, "day-book-"+from+"-"+to, []string{"Date", "Voucher", "Type", "Narration", "Code", "Account", "Debit", "Credit"}, out)
	}
	if vs == nil {
		vs = []*VoucherView{}
	}
	return httpx.OK(w, map[string]any{"from": from, "to": to, "count": strconv.Itoa(len(vs)), "vouchers": vs})
}
