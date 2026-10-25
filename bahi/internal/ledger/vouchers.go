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

// VoucherView is a posted voucher with its entries.
type VoucherView struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Number     string      `json:"number"`
	Date       string      `json:"date"`
	Narration  string      `json:"narration"`
	Source     string      `json:"source"`
	SourceID   string      `json:"source_id,omitempty"`
	SourceRef  string      `json:"source_ref,omitempty"`
	Status     string      `json:"status"`
	ReversalOf string      `json:"reversal_of,omitempty"` // set on a reversal voucher: the original
	ReversedBy string      `json:"reversed_by,omitempty"` // set on a reversed original: its reversal
	TotalPaise int64       `json:"total_paise"`
	CreatedBy  string      `json:"created_by"`
	CreatedAt  time.Time   `json:"created_at"`
	Entries    []EntryView `json:"entries,omitempty"`
}

// EntryView is a voucher line with account names resolved.
type EntryView struct {
	LineNo      int    `json:"line_no"`
	AccountID   string `json:"account_id"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	PartyID     string `json:"party_id,omitempty"`
	StoreID     string `json:"store_id,omitempty"`
	DebitPaise  int64  `json:"debit_paise"`
	CreditPaise int64  `json:"credit_paise"`
}

const voucherCols = `v.id, v.vtype, v.number, v.vdate, COALESCE(v.narration,''), v.source, COALESCE(v.source_id,''),
	COALESCE(v.source_ref,''), v.status, COALESCE(v.reversal_of::text,''), COALESCE(v.reversed_by::text,''), v.total_paise,
	v.created_by, v.created_at`

func scanVoucher(row pgx.Row) (*VoucherView, error) {
	var v VoucherView
	var d time.Time
	err := row.Scan(&v.ID, &v.Type, &v.Number, &d, &v.Narration, &v.Source, &v.SourceID, &v.SourceRef, &v.Status,
		&v.ReversalOf, &v.ReversedBy, &v.TotalPaise, &v.CreatedBy, &v.CreatedAt)
	v.Date = d.Format(time.DateOnly)
	return &v, err
}

func loadEntries(ctx context.Context, q db.Querier, voucherIDs []string) (map[string][]EntryView, error) {
	out := map[string][]EntryView{}
	if len(voucherIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT e.voucher_id, e.line_no, e.account_id, a.code, a.name, COALESCE(e.party_id::text,''),
		COALESCE(e.store_id::text,''), e.debit_paise, e.credit_paise
		FROM voucher_entries e JOIN accounts a ON a.id=e.account_id
		WHERE e.voucher_id = ANY($1::uuid[]) ORDER BY e.voucher_id, e.line_no`, voucherIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vid string
		var e EntryView
		if err := rows.Scan(&vid, &e.LineNo, &e.AccountID, &e.AccountCode, &e.AccountName, &e.PartyID, &e.StoreID,
			&e.DebitPaise, &e.CreditPaise); err != nil {
			return nil, err
		}
		out[vid] = append(out[vid], e)
	}
	return out, rows.Err()
}

func (s *Service) getVoucher(ctx context.Context, tenantID, id string) (*VoucherView, error) {
	v, err := scanVoucher(s.DB.QueryRow(ctx, `SELECT `+voucherCols+` FROM vouchers v WHERE v.tenant_id=$1 AND v.id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("voucher not found")
	}
	if err != nil {
		return nil, err
	}
	es, err := loadEntries(ctx, s.DB, []string{v.ID})
	v.Entries = es[v.ID]
	return v, err
}

// manualVoucher is the body of POST /v1/vouchers.
type manualVoucher struct {
	Type      string          `json:"type"`
	Date      string          `json:"date"`
	Narration string          `json:"narration"`
	Reference string          `json:"reference"`
	Entries   []posting.Entry `json:"entries"`
}

var manualTypes = map[string]bool{posting.VJournal: true, posting.VReceipt: true, posting.VPayment: true, posting.VContra: true}

func (in *manualVoucher) Validate() error {
	if !manualTypes[in.Type] {
		return httpx.Validation("type must be journal, receipt, payment or contra")
	}
	if len(in.Entries) > 200 {
		return httpx.Validation("at most 200 entries")
	}
	for _, e := range in.Entries {
		if e.AccountCode != "" {
			return httpx.Validation("manual entries use account_id or party_id")
		}
	}
	return nil
}

func (s *Service) handleCreateVoucher(w http.ResponseWriter, r *http.Request) error {
	var in manualVoucher
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	v := posting.Voucher{Type: in.Type, Date: orDefault(in.Date, s.today()), Narration: in.Narration,
		Source: posting.SrcManual, SourceRef: in.Reference, Entries: in.Entries}
	if err := v.Validate(); err != nil {
		return httpx.Validation("%s", err.Error())
	}
	if in.Type == posting.VContra {
		if err := s.checkContra(r.Context(), p.TenantID, in.Entries); err != nil {
			return err
		}
	}
	var posted Posted
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		posted, err = Post(r.Context(), tx, p.TenantID, p.UserID(), v)
		return err
	})
	if err != nil {
		return err
	}
	out, err := s.getVoucher(r.Context(), p.TenantID, posted.ID)
	if err != nil {
		return err
	}
	return httpx.Created(w, out)
}

// checkContra enforces that contra vouchers only move money between cash and bank.
func (s *Service) checkContra(ctx context.Context, tenantID string, es []posting.Entry) error {
	for i, e := range es {
		if e.AccountID == "" {
			return httpx.Validation("contra entry %d must use a cash or bank account_id", i+1)
		}
		var ok bool
		err := s.DB.QueryRow(ctx, `SELECT is_cash OR is_bank FROM accounts WHERE tenant_id=$1 AND id=$2`, tenantID, e.AccountID).Scan(&ok)
		if err != nil || !ok {
			return httpx.Validation("contra entry %d must use a cash or bank account", i+1)
		}
	}
	return nil
}

func (s *Service) handleListVouchers(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	page := httpx.ParsePage(r)
	where := []string{"v.tenant_id=$1", "v.vdate BETWEEN $2 AND $3"}
	args := []any{p.TenantID, from, to}
	if t := httpx.Query(r, "type"); t != "" {
		args = append(args, t)
		where = append(where, fmt.Sprintf("v.vtype=$%d", len(args)))
	}
	if src := httpx.Query(r, "source"); src != "" {
		args = append(args, src)
		where = append(where, fmt.Sprintf("v.source=$%d", len(args)))
	}
	if q := httpx.Query(r, "q"); q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(v.number ILIKE $%d OR v.narration ILIKE $%d OR v.source_ref ILIKE $%d)", n, n, n))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM vouchers v WHERE `+cond, args...).Scan(&total); err != nil {
		return err
	}
	args = append(args, page.Limit, page.Offset)
	rows, err := s.DB.Query(r.Context(), fmt.Sprintf(`SELECT %s FROM vouchers v WHERE %s ORDER BY v.vdate DESC, v.created_at DESC
		LIMIT $%d OFFSET $%d`, voucherCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []VoucherView{}
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return err
		}
		items = append(items, *v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[VoucherView]{Items: items, Total: total, Limit: page.Limit, Offset: page.Offset})
}

func (s *Service) handleGetVoucher(w http.ResponseWriter, r *http.Request) error {
	v, err := s.getVoucher(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, v)
}

type reverseInput struct {
	Date   string `json:"date"`
	Reason string `json:"reason"`
}

func (s *Service) handleReverseVoucher(w http.ResponseWriter, r *http.Request) error {
	var in reverseInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return httpx.Validation("reason is required")
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var posted Posted
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		var src string
		if err := tx.QueryRow(r.Context(), `SELECT source FROM vouchers WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&src); err != nil {
			if db.IsNotFound(err) || isBadUUID(err) {
				return httpx.NotFound("voucher not found")
			}
			return err
		}
		if src != posting.SrcManual && src != posting.SrcOpening {
			return httpx.Conflict("vouchers from %s are reversed by cancelling the source document", src)
		}
		var err error
		posted, err = Reverse(r.Context(), tx, p.TenantID, p.UserID(), id, orDefault(in.Date, s.today()),
			posting.SrcReversal, id, in.Reason)
		return err
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
