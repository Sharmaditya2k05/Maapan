package ledger

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Year-end close. One journal dated 31 March zeroes every income and expense
// account for the FY and carries the net profit (or loss) to Capital; with
// close_drawings the Drawings balance is transferred to Capital too. Balance
// sheet accounts need no carry-forward: every statement opens with the
// balance of all earlier entries. Reports read the closing voucher like
// this: P&L leaves it out (a closed year still shows what it earned), the
// trial balance and balance sheet include it (the year's result now sits in
// Capital instead of "P&L accumulated"). Optionally locks the year.

type yearEndInput struct {
	FY            string `json:"fy"`             // e.g. "25-26"
	CloseDrawings *bool  `json:"close_drawings"` // default true
	Lock          *bool  `json:"lock"`           // default true: period lock moves to 31 March
}

func (in *yearEndInput) Validate() error {
	if _, _, err := fyDates(in.FY); err != nil {
		return err
	}
	return nil
}

func fyDates(fy string) (string, string, error) {
	if len(fy) != 5 || fy[2] != '-' {
		return "", "", httpx.Validation("fy must look like 25-26")
	}
	a, err1 := strconv.Atoi(fy[:2])
	b, err2 := strconv.Atoi(fy[3:])
	if err1 != nil || err2 != nil || (a+1)%100 != b {
		return "", "", httpx.Validation("fy must look like 25-26")
	}
	return fmt.Sprintf("%04d-04-01", 2000+a), fmt.Sprintf("%04d-03-31", 2001+a), nil
}

// YearEnd describes a closing.
type YearEnd struct {
	FY             string `json:"fy"`
	From           string `json:"from"`
	To             string `json:"to"`
	VoucherID      string `json:"voucher_id,omitempty"`
	VoucherNumber  string `json:"voucher_number,omitempty"`
	Status         string `json:"status"` // closed | reopened
	NetProfitPaise int64  `json:"net_profit_paise"`
	DrawingsPaise  int64  `json:"drawings_closed_paise"`
	ClosedBy       string `json:"closed_by,omitempty"`
	LockedUntil    string `json:"locked_until,omitempty"`
}

func (s *Service) handleYearEndClose(w http.ResponseWriter, r *http.Request) error {
	var in yearEndInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	from, to, _ := fyDates(in.FY)
	if to >= s.today() {
		return httpx.Validation("FY %s has not ended yet", in.FY)
	}
	p := auth.FromContext(r.Context())
	ye := YearEnd{FY: in.FY, From: from, To: to, Status: "closed", ClosedBy: p.UserID()}
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := setActor(ctx, tx, p.UserID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-yearend:' || $1))`, p.TenantID); err != nil {
			return err
		}
		var n int
		var open bool
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(status='posted'),false) FROM vouchers
			WHERE tenant_id=$1 AND source=$2 AND source_ref=$3`, p.TenantID, posting.SrcYearEnd, "FY "+in.FY).Scan(&n, &open); err != nil {
			return err
		}
		if open {
			return httpx.Conflict("FY %s is already closed; reverse the closing voucher to reopen it", in.FY)
		}
		rows, err := tx.Query(ctx, `SELECT a.id, a.code, a.grp, COALESCE(sum(e.debit_paise - e.credit_paise),0)
			FROM voucher_entries e JOIN accounts a ON a.id=e.account_id JOIN vouchers v ON v.id=e.voucher_id
			WHERE e.tenant_id=$1 AND e.vdate BETWEEN $2 AND $3 AND (a.grp IN ('income','expense') OR a.code=$4)
			  AND v.source <> $5
			GROUP BY a.id, a.code, a.grp HAVING sum(e.debit_paise - e.credit_paise) <> 0 ORDER BY a.code`,
			p.TenantID, from, to, posting.Drawings, posting.SrcYearEnd)
		if err != nil {
			return err
		}
		closeDrawings := in.CloseDrawings == nil || *in.CloseDrawings
		var es []posting.Entry
		var net int64 // debit − credit of the P&L accounts = −profit
		for rows.Next() {
			var id, code, grp string
			var bal int64
			if err := rows.Scan(&id, &code, &grp, &bal); err != nil {
				rows.Close()
				return err
			}
			if code == posting.Drawings && grp == "equity" {
				if !closeDrawings {
					continue
				}
				ye.DrawingsPaise = bal
			} else {
				net += bal
			}
			e := posting.Entry{AccountID: id}
			if bal > 0 {
				e.Credit = bal
			} else {
				e.Debit = -bal
			}
			es = append(es, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(es) == 0 {
			return httpx.Validation("nothing to close for FY %s", in.FY)
		}
		ye.NetProfitPaise = -net
		toCapital := net + ye.DrawingsPaise // debit balance carried to Capital
		if toCapital > 0 {
			es = append(es, posting.Entry{AccountCode: posting.Capital, Debit: toCapital})
		} else if toCapital < 0 {
			es = append(es, posting.Entry{AccountCode: posting.Capital, Credit: -toCapital})
		}
		v := posting.Voucher{Type: posting.VJournal, Date: to, Source: posting.SrcYearEnd, SourceID: fmt.Sprintf("%s#%d", in.FY, n+1),
			SourceRef: "FY " + in.FY, Narration: fmt.Sprintf("Year-end close FY %s: net %s to Capital", in.FY, paiseStr(ye.NetProfitPaise)),
			Entries: es}
		posted, err := postVoucher(ctx, tx, p.TenantID, p.UserID(), v, postOpts{skipLock: true})
		if err != nil {
			return err
		}
		ye.VoucherID, ye.VoucherNumber = posted.ID, posted.Number
		if in.Lock == nil || *in.Lock {
			cur, err := s.periodLock(ctx, tx, p.TenantID)
			if err != nil {
				return err
			}
			if cur.LockedUntil < to {
				if _, err := tx.Exec(ctx, `INSERT INTO period_locks (tenant_id, locked_until, locked_by) VALUES ($1,$2,$3)
					ON CONFLICT (tenant_id) DO UPDATE SET locked_until=EXCLUDED.locked_until, locked_by=EXCLUDED.locked_by, locked_at=now()`,
					p.TenantID, to, p.UserID()); err != nil {
					return err
				}
				ye.LockedUntil = to
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.Created(w, ye)
}

// handleListYearEnds: GET /v1/year-end — closings (and reopened ones).
func (s *Service) handleListYearEnds(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	rows, err := s.DB.Query(r.Context(), `SELECT v.id, v.number, v.source_ref, v.status, v.created_by,
			COALESCE((SELECT e.credit_paise - e.debit_paise FROM voucher_entries e JOIN accounts a ON a.id=e.account_id
			          WHERE e.voucher_id=v.id AND a.code=$3 LIMIT 1),0)
		FROM vouchers v WHERE v.tenant_id=$1 AND v.source=$2 AND v.reversal_of IS NULL ORDER BY v.vdate DESC, v.created_at DESC`, p.TenantID, posting.SrcYearEnd, posting.Capital)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []YearEnd{}
	for rows.Next() {
		var y YearEnd
		var ref, status string
		var toCapital int64
		if err := rows.Scan(&y.VoucherID, &y.VoucherNumber, &ref, &status, &y.ClosedBy, &toCapital); err != nil {
			return err
		}
		y.FY = ref[len("FY "):]
		y.From, y.To, _ = fyDates(y.FY)
		y.Status = "closed"
		if status != "posted" {
			y.Status = "reopened"
		}
		y.NetProfitPaise = toCapital // net of drawings when they were closed too
		items = append(items, y)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// handleYearEndReopen: POST /v1/year-end/{fy}/reopen reverses the closing
// voucher (the period must be unlocked first).
func (s *Service) handleYearEndReopen(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	fy := r.PathValue("fy")
	if _, _, err := fyDates(fy); err != nil {
		return err
	}
	var posted Posted
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(r.Context(), `SELECT id FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_ref=$3 AND status='posted'`,
			p.TenantID, posting.SrcYearEnd, "FY "+fy).Scan(&id)
		if db.IsNotFound(err) {
			return httpx.NotFound("FY %s is not closed", fy)
		}
		if err != nil {
			return err
		}
		_, to, _ := fyDates(fy)
		// Source year_end keeps the reversal out of the P&L as well.
		posted, err = Reverse(r.Context(), tx, p.TenantID, p.UserID(), id, to, posting.SrcYearEnd, "reopen/"+id, "year reopened")
		return err
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"fy": fy, "status": "reopened", "reversal": posted, "reopened_at": time.Now().UTC()})
}
