package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/bankstmt"
	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Bank statement import, matching and the bank reconciliation statement.
// A statement line is matched one-to-one with a posting on the bank account;
// matched postings are "cleared" as of the line's date. The BRS explains the
// gap between the books and the bank as of a date.

const maxStatementBytes = 10 << 20

type importInput struct {
	BankAccountID string           `json:"bank_account_id"`
	Preset        string           `json:"preset"`
	FileName      string           `json:"file_name"`
	ContentBase64 string           `json:"content_base64"`
	Mapping       bankstmt.Mapping `json:"mapping"`
	AutoMatch     *bool            `json:"auto_match"`
}

// readImport accepts multipart/form-data (file + form fields, mapping as a
// JSON string) or a JSON body with the file base64-encoded.
func readImport(r *http.Request) (importInput, []byte, error) {
	var in importInput
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxStatementBytes); err != nil {
			return in, nil, httpx.BadRequest("invalid upload: %s", err.Error())
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return in, nil, httpx.Validation("file is required")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxStatementBytes+1))
		if err != nil {
			return in, nil, err
		}
		in.BankAccountID, in.Preset, in.FileName = r.FormValue("bank_account_id"), r.FormValue("preset"), hdr.Filename
		if m := r.FormValue("mapping"); m != "" {
			if err := json.Unmarshal([]byte(m), &in.Mapping); err != nil {
				return in, nil, httpx.Validation("mapping must be JSON: %s", err.Error())
			}
		}
		if a := r.FormValue("auto_match"); a != "" {
			v := a != "false"
			in.AutoMatch = &v
		}
		return in, data, nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxStatementBytes*2))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, nil, httpx.BadRequest("invalid JSON: %s", err.Error())
	}
	data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil {
		return in, nil, httpx.Validation("content_base64 is not valid base64")
	}
	return in, data, nil
}

// StatementImport is the result of an import.
type StatementImport struct {
	ID            string `json:"id"`
	BankAccountID string `json:"bank_account_id"`
	Preset        string `json:"preset"`
	FileName      string `json:"file_name,omitempty"`
	From          string `json:"from"`
	To            string `json:"to"`
	Lines         int    `json:"lines"`
	Duplicates    int    `json:"duplicates"`
	Matched       int    `json:"matched"`
}

func lineHash(account string, l bankstmt.Line, occurrence int) string {
	bal := ""
	if l.Balance != nil {
		bal = fmt.Sprint(*l.Balance)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{account, l.Date, fmt.Sprint(l.Withdrawal), fmt.Sprint(l.Deposit),
		strings.TrimSpace(l.Reference), strings.Join(strings.Fields(l.Description), " "), bal, fmt.Sprint(occurrence)}, "|")))
	return hex.EncodeToString(h[:])
}

func (s *Service) handleImportStatement(w http.ResponseWriter, r *http.Request) error {
	in, data, err := readImport(r)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxStatementBytes {
		return httpx.Validation("statement file must be 1 byte .. 10 MB")
	}
	p := auth.FromContext(r.Context())
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.BankAccountID); err != nil {
		return err
	}
	preset := strings.ToLower(orDefault(in.Preset, "generic"))
	lines, err := bankstmt.Parse(data, in.FileName, preset, in.Mapping)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	out := StatementImport{BankAccountID: in.BankAccountID, Preset: preset, FileName: in.FileName, From: lines[0].Date, To: lines[0].Date}
	for _, l := range lines {
		out.From, out.To = min(out.From, l.Date), max(out.To, l.Date)
	}
	err = s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO bank_statements (tenant_id, bank_account_id, preset, file_name, from_date, to_date, imported_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, p.TenantID, in.BankAccountID, preset, nilIfEmpty(in.FileName), out.From, out.To,
			p.UserID()).Scan(&out.ID); err != nil {
			return err
		}
		seen := map[string]int{}
		for _, l := range lines {
			if l.Withdrawal > 0 && l.Deposit > 0 { // both columns filled: keep the net movement
				if l.Withdrawal > l.Deposit {
					l.Withdrawal, l.Deposit = l.Withdrawal-l.Deposit, 0
				} else {
					l.Deposit, l.Withdrawal = l.Deposit-l.Withdrawal, 0
				}
				if l.Withdrawal == 0 && l.Deposit == 0 {
					continue
				}
			}
			base := lineHash(in.BankAccountID, l, 0)
			seen[base]++
			tag, err := tx.Exec(r.Context(), `INSERT INTO bank_statement_lines (tenant_id, statement_id, bank_account_id, row_no, txn_date,
				value_date, description, reference, withdrawal_paise, deposit_paise, balance_paise, line_hash)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (tenant_id, bank_account_id, line_hash) DO NOTHING`,
				p.TenantID, out.ID, in.BankAccountID, l.Row, l.Date, nilIfEmpty(l.ValueDate), l.Description, nilIfEmpty(l.Reference),
				l.Withdrawal, l.Deposit, l.Balance, lineHash(in.BankAccountID, l, seen[base]))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				out.Duplicates++
			} else {
				out.Lines++
			}
		}
		_, err := tx.Exec(r.Context(), `UPDATE bank_statements SET line_count=$2, duplicate_count=$3 WHERE id=$1`, out.ID, out.Lines, out.Duplicates)
		return err
	})
	if err != nil {
		return err
	}
	if in.AutoMatch == nil || *in.AutoMatch {
		n, err := s.autoMatch(r.Context(), p.TenantID, p.UserID(), in.BankAccountID, out.From, out.To, posting.DefaultWindow)
		if err != nil {
			return err
		}
		out.Matched = n
	}
	return httpx.Created(w, out)
}

// bookEntries loads unmatched postings on a bank account dated [from, to].
func bookEntries(ctx context.Context, q db.Querier, tenantID, accountID, from, to string) ([]posting.BookEntry, error) {
	rows, err := q.Query(ctx, `SELECT e.id, e.vdate, e.debit_paise - e.credit_paise, v.number, COALESCE(v.source_ref,''), COALESCE(v.narration,''),
			COALESCE((SELECT string_agg(c.cheque_no, ' ') FROM cheques c WHERE c.tenant_id=e.tenant_id
			          AND (c.voucher_id=v.id OR c.clear_voucher_id=v.id)),''),
			COALESCE((SELECT string_agg(COALESCE(t.reference,''), ' ') FROM bank_transactions t WHERE t.voucher_id=v.id),'')
		FROM voucher_entries e JOIN vouchers v ON v.id=e.voucher_id
		WHERE e.tenant_id=$1 AND e.account_id=$2 AND e.vdate BETWEEN $3 AND $4
		  AND NOT EXISTS (SELECT 1 FROM bank_statement_lines l WHERE l.entry_id=e.id)
		ORDER BY e.vdate`, tenantID, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []posting.BookEntry
	for rows.Next() {
		var be posting.BookEntry
		var number, ref, narr, cheques, txref string
		if err := rows.Scan(&be.ID, &be.Date, &be.Amount, &number, &ref, &narr, &cheques, &txref); err != nil {
			return nil, err
		}
		be.Refs = append([]string{number, ref, txref}, strings.Fields(cheques)...)
		// UTRs and references inside the narration ("… UTR HDFC0012345 …")
		for _, f := range strings.Fields(narr) {
			if len(f) >= 8 {
				be.Refs = append(be.Refs, f)
			}
		}
		out = append(out, be)
	}
	return out, rows.Err()
}

// autoMatch runs posting.AutoMatch over unmatched lines of [from, to].
func (s *Service) autoMatch(ctx context.Context, tenantID, userID, accountID, from, to string, win posting.MatchWindow) (int, error) {
	n := 0
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := setActor(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-brs:' || $1))`, accountID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, txn_date, deposit_paise - withdrawal_paise, description || ' ' || COALESCE(reference,'')
			FROM bank_statement_lines WHERE tenant_id=$1 AND bank_account_id=$2 AND status='unmatched' AND txn_date BETWEEN $3 AND $4`,
			tenantID, accountID, from, to)
		if err != nil {
			return err
		}
		var lines []posting.StmtLine
		for rows.Next() {
			var l posting.StmtLine
			if err := rows.Scan(&l.ID, &l.Date, &l.Amount, &l.Text); err != nil {
				rows.Close()
				return err
			}
			lines = append(lines, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(lines) == 0 {
			return err
		}
		f, _ := time.Parse(time.DateOnly, from)
		t, _ := time.Parse(time.DateOnly, to)
		books, err := bookEntries(ctx, tx, tenantID, accountID, f.AddDate(0, 0, -win.AfterDays).Format(time.DateOnly),
			t.AddDate(0, 0, win.BeforeDays).Format(time.DateOnly))
		if err != nil {
			return err
		}
		for _, m := range posting.AutoMatch(lines, books, win) {
			if err := setMatch(ctx, tx, tenantID, userID, m.LineID, m.EntryID, m.Rule); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func setMatch(ctx context.Context, tx pgx.Tx, tenantID, userID, lineID, entryID, rule string) error {
	_, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET status='matched', entry_id=$3,
		voucher_id=(SELECT voucher_id FROM voucher_entries WHERE id=$3), match_rule=$4, matched_by=$5, matched_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, lineID, entryID, rule, userID)
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("that book entry is already matched to another statement line")
	}
	return err
}

type autoMatchInput struct {
	BankAccountID string `json:"bank_account_id"`
	From          string `json:"from"`
	To            string `json:"to"`
	BeforeDays    *int   `json:"before_days"`
	AfterDays     *int   `json:"after_days"`
}

func (s *Service) handleAutoMatch(w http.ResponseWriter, r *http.Request) error {
	var in autoMatchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.BankAccountID); err != nil {
		return err
	}
	from, to := orDefault(in.From, "1900-01-01"), orDefault(in.To, "9999-12-31")
	for _, d := range []string{from, to} {
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return httpx.Validation("from/to must be YYYY-MM-DD")
		}
	}
	win := posting.DefaultWindow
	if in.BeforeDays != nil {
		win.BeforeDays = max(0, min(*in.BeforeDays, 60))
	}
	if in.AfterDays != nil {
		win.AfterDays = max(0, min(*in.AfterDays, 60))
	}
	n, err := s.autoMatch(r.Context(), p.TenantID, p.UserID(), in.BankAccountID, from, to, win)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"matched": n, "window": win})
}

// StatementLine is a bank statement row with its match.
type BankLine struct {
	ID            string  `json:"id"`
	StatementID   string  `json:"statement_id"`
	BankAccountID string  `json:"bank_account_id"`
	Date          string  `json:"date"`
	ValueDate     *string `json:"value_date,omitempty"`
	Description   string  `json:"description"`
	Reference     string  `json:"reference,omitempty"`
	Withdrawal    int64   `json:"withdrawal_paise"`
	Deposit       int64   `json:"deposit_paise"`
	Balance       *int64  `json:"balance_paise,omitempty"`
	Status        string  `json:"status"`
	EntryID       *string `json:"entry_id,omitempty"`
	VoucherID     *string `json:"voucher_id,omitempty"`
	VoucherNumber string  `json:"voucher_number,omitempty"`
	MatchRule     string  `json:"match_rule,omitempty"`
}

const bankLineCols = `l.id, l.statement_id, l.bank_account_id, l.txn_date, l.value_date, l.description, COALESCE(l.reference,''),
	l.withdrawal_paise, l.deposit_paise, l.balance_paise, l.status, l.entry_id::text, l.voucher_id::text, COALESCE(v.number,''), COALESCE(l.match_rule,'')`

func scanBankLine(row pgx.Row) (*BankLine, error) {
	var l BankLine
	var d time.Time
	var vd *time.Time
	err := row.Scan(&l.ID, &l.StatementID, &l.BankAccountID, &d, &vd, &l.Description, &l.Reference, &l.Withdrawal, &l.Deposit,
		&l.Balance, &l.Status, &l.EntryID, &l.VoucherID, &l.VoucherNumber, &l.MatchRule)
	l.Date, l.ValueDate = d.Format(time.DateOnly), dateStr(vd)
	return &l, err
}

func (s *Service) getBankLine(ctx context.Context, q db.Querier, tenantID, id string) (*BankLine, error) {
	l, err := scanBankLine(q.QueryRow(ctx, `SELECT `+bankLineCols+` FROM bank_statement_lines l LEFT JOIN vouchers v ON v.id=l.voucher_id
		WHERE l.tenant_id=$1 AND l.id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("statement line not found")
	}
	return l, err
}

func (s *Service) handleListBankLines(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	args := []any{p.TenantID, from, to}
	cond := "l.tenant_id=$1 AND l.txn_date BETWEEN $2 AND $3"
	for _, f := range []struct{ col, q string }{{"l.bank_account_id::text", "bank_account_id"}, {"l.status", "status"}, {"l.statement_id::text", "statement_id"}} {
		if v := httpx.Query(r, f.q); v != "" {
			args = append(args, v)
			cond += fmt.Sprintf(" AND %s=$%d", f.col, len(args))
		}
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+bankLineCols+` FROM bank_statement_lines l LEFT JOIN vouchers v ON v.id=l.voucher_id
		WHERE `+cond+` ORDER BY l.txn_date, l.row_no LIMIT 5000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []BankLine{}
	for rows.Next() {
		l, err := scanBankLine(rows)
		if err != nil {
			return err
		}
		items = append(items, *l)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) handleListStatements(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	args := []any{p.TenantID}
	cond := "tenant_id=$1"
	if id := httpx.Query(r, "bank_account_id"); id != "" {
		args = append(args, id)
		cond += " AND bank_account_id::text=$2"
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id, bank_account_id, preset, COALESCE(file_name,''), from_date, to_date, line_count, duplicate_count,
			(SELECT count(*) FROM bank_statement_lines l WHERE l.statement_id=s.id AND l.status='matched')
		FROM bank_statements s WHERE `+cond+` ORDER BY created_at DESC LIMIT 500`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []StatementImport{}
	for rows.Next() {
		var st StatementImport
		var f, t *time.Time
		if err := rows.Scan(&st.ID, &st.BankAccountID, &st.Preset, &st.FileName, &f, &t, &st.Lines, &st.Duplicates, &st.Matched); err != nil {
			return err
		}
		st.From, st.To = deref(dateStr(f)), deref(dateStr(t))
		items = append(items, st)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) handleStatementPresets(w http.ResponseWriter, _ *http.Request) error {
	return httpx.OK(w, map[string]any{"items": bankstmt.PresetList()})
}

type matchInput struct {
	EntryID   string `json:"entry_id"`
	VoucherID string `json:"voucher_id"`
}

// handleMatchLine: POST /v1/bank/statement-lines/{id}/match {entry_id | voucher_id}
func (s *Service) handleMatchLine(w http.ResponseWriter, r *http.Request) error {
	var in matchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if (in.EntryID == "") == (in.VoucherID == "") {
		return httpx.Validation("give entry_id or voucher_id")
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		l, err := s.getBankLine(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if l.Status == "matched" {
			return httpx.Conflict("line is already matched; unmatch it first")
		}
		var entry string
		var amt int64
		q := `SELECT id, debit_paise - credit_paise FROM voucher_entries WHERE tenant_id=$1 AND account_id=$2 AND `
		arg := in.EntryID
		if in.EntryID != "" {
			q += "id::text=$3"
		} else {
			q += "voucher_id::text=$3"
			arg = in.VoucherID
		}
		rows, err := tx.Query(r.Context(), q, p.TenantID, l.BankAccountID, arg)
		if err != nil {
			return err
		}
		n := 0
		want := l.Deposit - l.Withdrawal
		for rows.Next() {
			var eid string
			var a int64
			if err := rows.Scan(&eid, &a); err != nil {
				rows.Close()
				return err
			}
			if n == 0 || a == want {
				entry, amt = eid, a
			}
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if n == 0 {
			return httpx.Validation("no posting on this bank account for that entry/voucher")
		}
		if amt != want {
			return httpx.Validation("amounts differ: statement %s, books %s", paiseStr(want), paiseStr(amt))
		}
		return setMatch(r.Context(), tx, p.TenantID, p.UserID(), id, entry, "manual")
	})
	if err != nil {
		return err
	}
	l, err := s.getBankLine(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) setLineStatus(w http.ResponseWriter, r *http.Request, status string) error {
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE bank_statement_lines SET status=$3, entry_id=NULL, voucher_id=NULL, match_rule=NULL,
			matched_by=$4, matched_at=now() WHERE tenant_id=$1 AND id::text=$2`, p.TenantID, id, status, p.UserID())
		if err == nil && tag.RowsAffected() == 0 {
			return httpx.NotFound("statement line not found")
		}
		return err
	})
	if err != nil {
		return err
	}
	l, err := s.getBankLine(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, l)
}

func (s *Service) handleUnmatchLine(w http.ResponseWriter, r *http.Request) error {
	return s.setLineStatus(w, r, "unmatched")
}

func (s *Service) handleIgnoreLine(w http.ResponseWriter, r *http.Request) error {
	return s.setLineStatus(w, r, "ignored")
}

type lineVoucherInput struct {
	AccountID   string            `json:"account_id"` // counter account (bank charges, interest, expense …)
	PartyID     string            `json:"party_id"`   // or a party (customer deposit, supplier payment)
	Narration   string            `json:"narration"`
	Allocations []allocationInput `json:"allocations"`
}

func (in *lineVoucherInput) Validate() error {
	if (in.AccountID == "") == (in.PartyID == "") {
		return httpx.Validation("give account_id or party_id")
	}
	return (&allocationsInput{Allocations: in.Allocations}).Validate()
}

// handleLineVoucher books an unmatched statement line (bank charges, interest,
// a customer's direct deposit …) and matches it: deposit → receipt Dr Bank /
// Cr counter; withdrawal → payment Dr counter / Cr Bank.
func (s *Service) handleLineVoucher(w http.ResponseWriter, r *http.Request) error {
	var in lineVoucherInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var posted Posted
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM bank_statement_lines WHERE tenant_id=$1 AND id::text=$2 FOR UPDATE`, p.TenantID, id); err != nil {
			return err
		}
		l, err := s.getBankLine(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if l.Status == "matched" {
			return httpx.Conflict("line is already matched")
		}
		counter := posting.Entry{AccountID: in.AccountID, PartyID: in.PartyID}
		bank := posting.Entry{AccountID: l.BankAccountID}
		vtype := posting.VReceipt
		if l.Deposit > 0 {
			bank.Debit, counter.Credit = l.Deposit, l.Deposit
		} else {
			bank.Credit, counter.Debit, vtype = l.Withdrawal, l.Withdrawal, posting.VPayment
		}
		narr := orDefault(in.Narration, "Bank statement: "+l.Description)
		posted, err = Post(ctx, tx, p.TenantID, p.UserID(), posting.Voucher{Type: vtype, Date: l.Date, Narration: narr,
			Source: posting.SrcBankLine, SourceID: l.ID, SourceRef: l.Reference, Entries: []posting.Entry{bank, counter}})
		if err != nil {
			return err
		}
		var entry string
		if err := tx.QueryRow(ctx, `SELECT id FROM voucher_entries WHERE voucher_id=$1 AND account_id=$2 LIMIT 1`, posted.ID, l.BankAccountID).Scan(&entry); err != nil {
			return err
		}
		if err := setMatch(ctx, tx, p.TenantID, p.UserID(), l.ID, entry, "created"); err != nil {
			return err
		}
		if len(in.Allocations) > 0 {
			return replaceAllocations(ctx, tx, p.TenantID, p.UserID(), posted.ID, in.Allocations)
		}
		return nil
	})
	if err != nil {
		return err
	}
	v, err := s.getVoucher(r.Context(), p.TenantID, posted.ID)
	if err != nil {
		return err
	}
	return httpx.Created(w, v)
}

// BRSItem is one reconciling item.
type BRSItem struct {
	Date          string `json:"date"`
	VoucherID     string `json:"voucher_id,omitempty"`
	VoucherNumber string `json:"voucher_number,omitempty"`
	LineID        string `json:"line_id,omitempty"`
	Description   string `json:"description"`
	Reference     string `json:"reference,omitempty"`
	AmountPaise   int64  `json:"amount_paise"`
}

// BRS is the bank reconciliation statement as of a date.
type BRS struct {
	BankAccountID        string    `json:"bank_account_id"`
	AccountName          string    `json:"account_name"`
	AsOf                 string    `json:"as_of"`
	From                 string    `json:"from,omitempty"`
	BookBalancePaise     int64     `json:"balance_per_books_paise"`
	UnpresentedCheques   []BRSItem `json:"unpresented_cheques"` // paid in the books, not yet debited by the bank
	UncreditedDeposits   []BRSItem `json:"uncredited_deposits"` // received in the books, not yet credited by the bank
	BankDebitsNotBooked  []BRSItem `json:"bank_debits_not_in_books"`
	BankCreditsNotBooked []BRSItem `json:"bank_credits_not_in_books"`
	UnpresentedPaise     int64     `json:"unpresented_paise"`
	UncreditedPaise      int64     `json:"uncredited_paise"`
	BankDebitsPaise      int64     `json:"bank_debits_not_in_books_paise"`
	BankCreditsPaise     int64     `json:"bank_credits_not_in_books_paise"`
	ComputedBankPaise    int64     `json:"balance_per_bank_computed_paise"`
	StatementBalance     *int64    `json:"balance_per_statement_paise,omitempty"` // last imported running balance on or before as_of
	DifferencePaise      *int64    `json:"difference_paise,omitempty"`            // statement − computed; 0 when reconciled
}

// handleBRS: GET /v1/reports/brs?bank_account_id=&as_of=[&from=]. Book
// entries dated before `from` (e.g. the first imported statement) are taken
// as already cleared.
func (s *Service) handleBRS(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	acct := httpx.Query(r, "bank_account_id")
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, acct); err != nil {
		return err
	}
	asOf, err := s.asOf(r)
	if err != nil {
		return err
	}
	from := httpx.Query(r, "from")
	if from != "" {
		if _, err := time.Parse(time.DateOnly, from); err != nil {
			return httpx.Validation("from must be YYYY-MM-DD")
		}
	}
	b := BRS{BankAccountID: acct, AsOf: asOf, From: from, UnpresentedCheques: []BRSItem{}, UncreditedDeposits: []BRSItem{},
		BankDebitsNotBooked: []BRSItem{}, BankCreditsNotBooked: []BRSItem{}}
	ctx := r.Context()
	if err := s.DB.QueryRow(ctx, `SELECT a.name, COALESCE((SELECT sum(debit_paise - credit_paise) FROM voucher_entries e
		WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id AND e.vdate <= $3),0) FROM accounts a WHERE a.tenant_id=$1 AND a.id=$2`,
		p.TenantID, acct, asOf).Scan(&b.AccountName, &b.BookBalancePaise); err != nil {
		return err
	}
	// Book entries not cleared by the bank on or before as_of.
	rows, err := s.DB.Query(ctx, `SELECT e.vdate, v.id, v.number, COALESCE(v.narration,''), COALESCE(v.source_ref,''), e.debit_paise - e.credit_paise
		FROM voucher_entries e JOIN vouchers v ON v.id=e.voucher_id
		WHERE e.tenant_id=$1 AND e.account_id=$2 AND e.vdate <= $3 AND ($4 = '' OR e.vdate >= $4::date)
		  AND NOT EXISTS (SELECT 1 FROM bank_statement_lines l WHERE l.entry_id=e.id AND l.txn_date <= $3)
		  AND NOT EXISTS (SELECT 1 FROM bank_transactions t WHERE t.voucher_id=e.voucher_id AND t.reconciled_at <= $3)
		ORDER BY e.vdate, v.number`, p.TenantID, acct, asOf, from)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d time.Time
		var it BRSItem
		var amt int64
		if err := rows.Scan(&d, &it.VoucherID, &it.VoucherNumber, &it.Description, &it.Reference, &amt); err != nil {
			rows.Close()
			return err
		}
		it.Date = d.Format(time.DateOnly)
		if amt < 0 {
			it.AmountPaise = -amt
			b.UnpresentedCheques = append(b.UnpresentedCheques, it)
			b.UnpresentedPaise += it.AmountPaise
		} else {
			it.AmountPaise = amt
			b.UncreditedDeposits = append(b.UncreditedDeposits, it)
			b.UncreditedPaise += amt
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Bank lines on or before as_of not (yet) matched to a book entry of that date.
	rows, err = s.DB.Query(ctx, `SELECT l.id, l.txn_date, l.description, COALESCE(l.reference,''), l.deposit_paise - l.withdrawal_paise
		FROM bank_statement_lines l LEFT JOIN voucher_entries e ON e.id=l.entry_id
		WHERE l.tenant_id=$1 AND l.bank_account_id=$2 AND l.txn_date <= $3 AND l.status <> 'ignored'
		  AND (l.entry_id IS NULL OR e.vdate > $3)
		ORDER BY l.txn_date, l.row_no`, p.TenantID, acct, asOf)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d time.Time
		var it BRSItem
		var amt int64
		if err := rows.Scan(&it.LineID, &d, &it.Description, &it.Reference, &amt); err != nil {
			rows.Close()
			return err
		}
		it.Date = d.Format(time.DateOnly)
		if amt < 0 {
			it.AmountPaise = -amt
			b.BankDebitsNotBooked = append(b.BankDebitsNotBooked, it)
			b.BankDebitsPaise += it.AmountPaise
		} else {
			it.AmountPaise = amt
			b.BankCreditsNotBooked = append(b.BankCreditsNotBooked, it)
			b.BankCreditsPaise += amt
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	b.ComputedBankPaise = b.BookBalancePaise - b.UncreditedPaise + b.UnpresentedPaise + b.BankCreditsPaise - b.BankDebitsPaise
	var bal *int64
	err = s.DB.QueryRow(ctx, `SELECT balance_paise FROM bank_statement_lines WHERE tenant_id=$1 AND bank_account_id=$2 AND txn_date <= $3
		AND balance_paise IS NOT NULL ORDER BY txn_date DESC, created_at DESC, row_no DESC LIMIT 1`, p.TenantID, acct, asOf).Scan(&bal)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if bal != nil {
		diff := *bal - b.ComputedBankPaise
		b.StatementBalance, b.DifferencePaise = bal, &diff
	}
	if wantsCSV(r) {
		out := [][]string{{"Balance as per books", "", "", "", paiseStr(b.BookBalancePaise)}}
		sec := func(title string, items []BRSItem, sign string) {
			for _, it := range items {
				out = append(out, []string{title, it.Date, it.VoucherNumber, it.Description, sign + paiseStr(it.AmountPaise)})
			}
		}
		sec("Add: cheques issued, not presented", b.UnpresentedCheques, "")
		sec("Less: deposits not yet credited", b.UncreditedDeposits, "-")
		sec("Add: bank credits not in books", b.BankCreditsNotBooked, "")
		sec("Less: bank debits not in books", b.BankDebitsNotBooked, "-")
		out = append(out, []string{"Balance as per bank (computed)", "", "", "", paiseStr(b.ComputedBankPaise)})
		if bal != nil {
			out = append(out, []string{"Balance as per statement", "", "", "", paiseStr(*bal)}, []string{"Difference", "", "", "", paiseStr(*b.DifferencePaise)})
		}
		return writeCSV(w, "brs-"+b.AccountName+"-"+asOf, []string{"Item", "Date", "Voucher", "Description", "Amount"}, out)
	}
	return httpx.OK(w, b)
}
