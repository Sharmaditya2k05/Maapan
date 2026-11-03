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

// Cheque register.
//
//	received (cheque date reached)  receipt  Dr Cheques in Hand / Cr Party   (post-dated: on deposit)
//	deposited → cleared             receipt  Dr Bank / Cr Cheques in Hand
//	deposited → bounced             reversal of the receipt: Dr Party / Cr Cheques in Hand
//	received → cancelled            reversal of the receipt, if one was posted
//	issued (cheque date reached)    payment  Dr Party / Cr Bank               (post-dated: on clearing)
//	issued → bounced | cancelled    reversal of the payment
//
// Cheques recorded before phase 4 have no party voucher yet; they keep the
// old behaviour and book Dr Bank / Cr Party (or the reverse) when they clear.
// "pending" means received or issued and not yet presented.

// Cheque is a received or issued cheque.
type Cheque struct {
	ID              string  `json:"id"`
	Direction       string  `json:"direction"`
	PartyID         string  `json:"party_id"`
	PartyName       string  `json:"party_name"`
	BankAccountID   string  `json:"bank_account_id"`
	ChequeNo        string  `json:"cheque_no"`
	ChequeDate      string  `json:"cheque_date"`
	DraweeBank      string  `json:"drawee_bank,omitempty"`
	AmountPaise     int64   `json:"amount_paise"`
	Status          string  `json:"status"`
	Pending         bool    `json:"pending"`
	PostDated       bool    `json:"post_dated"`
	VoucherID       *string `json:"voucher_id,omitempty"` // the voucher booking it against the party
	ClearVoucherID  *string `json:"clear_voucher_id,omitempty"`
	BounceVoucherID *string `json:"bounce_voucher_id,omitempty"`
	StatusDate      *string `json:"status_date,omitempty"`
	ReceivedOn      *string `json:"received_on,omitempty"` // issued cheques: date handed over
	DepositedOn     *string `json:"deposited_on,omitempty"`
	ClearedOn       *string `json:"cleared_on,omitempty"`
	BouncedOn       *string `json:"bounced_on,omitempty"`
	BounceReason    string  `json:"bounce_reason,omitempty"`
	Notes           string  `json:"notes,omitempty"`
}

type chequeInput struct {
	Direction     string            `json:"direction"` // received | issued
	PartyID       string            `json:"party_id"`
	BankAccountID string            `json:"bank_account_id"`
	ChequeNo      string            `json:"cheque_no"`
	ChequeDate    string            `json:"cheque_date"`
	ReceivedOn    string            `json:"received_on"` // default today
	DraweeBank    string            `json:"drawee_bank"`
	AmountPaise   int64             `json:"amount_paise"`
	Notes         string            `json:"notes"`
	Allocations   []allocationInput `json:"allocations"` // bills this cheque settles (applied when the party voucher posts now)
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
	if in.ReceivedOn != "" {
		if _, err := time.Parse(time.DateOnly, in.ReceivedOn); err != nil {
			return httpx.Validation("received_on must be YYYY-MM-DD")
		}
	}
	return (&allocationsInput{Allocations: in.Allocations}).Validate()
}

const chequeCols = `c.id, c.direction, c.party_id, p.name, c.bank_account_id, c.cheque_no, c.cheque_date,
	COALESCE(c.drawee_bank,''), c.amount_paise, c.status, c.voucher_id, c.status_date, COALESCE(c.notes,''),
	c.clear_voucher_id, c.bounce_voucher_id, c.received_on, c.deposited_on, c.cleared_on, c.bounced_on, COALESCE(c.bounce_reason,'')`

func dateStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func scanCheque(row pgx.Row) (*Cheque, error) {
	var c Cheque
	var cd time.Time
	var sd, ro, do, co, bo *time.Time
	if err := row.Scan(&c.ID, &c.Direction, &c.PartyID, &c.PartyName, &c.BankAccountID, &c.ChequeNo, &cd, &c.DraweeBank,
		&c.AmountPaise, &c.Status, &c.VoucherID, &sd, &c.Notes, &c.ClearVoucherID, &c.BounceVoucherID, &ro, &do, &co, &bo,
		&c.BounceReason); err != nil {
		return nil, err
	}
	c.ChequeDate = cd.Format(time.DateOnly)
	c.StatusDate, c.ReceivedOn, c.DepositedOn, c.ClearedOn, c.BouncedOn = dateStr(sd), dateStr(ro), dateStr(do), dateStr(co), dateStr(bo)
	c.Pending = c.Status == "received" || c.Status == "issued"
	c.PostDated = ro != nil && cd.After(*ro)
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

// partyVoucher books the cheque against the party: a received cheque into
// Cheques in Hand, an issued one straight out of Bank.
func partyVoucher(c *Cheque, date string) posting.Voucher {
	party := posting.Entry{PartyID: c.PartyID}
	v := posting.Voucher{Date: date, Source: posting.SrcCheque, SourceID: c.ID, SourceRef: "Cheque " + c.ChequeNo,
		Narration: fmt.Sprintf("Cheque %s %s — %s", c.ChequeNo, c.Direction, c.PartyName)}
	if c.Direction == "received" {
		v.Type = posting.VReceipt
		party.Credit = c.AmountPaise
		v.Entries = []posting.Entry{{AccountCode: posting.ChequesInHand, Debit: c.AmountPaise}, party}
	} else {
		v.Type = posting.VPayment
		party.Debit = c.AmountPaise
		v.Entries = []posting.Entry{party, {AccountID: c.BankAccountID, Credit: c.AmountPaise}}
	}
	return v
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
	on := orDefault(in.ReceivedOn, s.today())
	var id string
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		err := tx.QueryRow(r.Context(), `INSERT INTO cheques (tenant_id, direction, party_id, bank_account_id, cheque_no,
			cheque_date, drawee_bank, amount_paise, status, notes, status_date, received_on)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$2,$9,$10,$10) RETURNING id`,
			p.TenantID, in.Direction, in.PartyID, in.BankAccountID, strings.TrimSpace(in.ChequeNo), in.ChequeDate,
			nilIfEmpty(in.DraweeBank), in.AmountPaise, nilIfEmpty(in.Notes), on).Scan(&id)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("cheque %s already recorded", in.ChequeNo)
		}
		if err != nil {
			return err
		}
		if in.ChequeDate > on {
			if len(in.Allocations) > 0 {
				return httpx.Validation("a post-dated cheque is booked on its date; allocate it then")
			}
			return nil // post-dated: booked on deposit (received) or clearing (issued)
		}
		c, err := s.getCheque(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		posted, err := Post(r.Context(), tx, p.TenantID, p.UserID(), partyVoucher(c, on))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE cheques SET voucher_id=$3 WHERE tenant_id=$1 AND id=$2`, p.TenantID, id, posted.ID); err != nil {
			return err
		}
		if len(in.Allocations) > 0 {
			return replaceAllocations(r.Context(), tx, p.TenantID, p.UserID(), posted.ID, in.Allocations)
		}
		return nil
	})
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
	switch st := httpx.Query(r, "status"); st {
	case "":
	case "pending":
		cond += " AND c.status IN ('received','issued')"
	default:
		args = append(args, st)
		cond += fmt.Sprintf(" AND c.status=$%d", len(args))
	}
	if d := httpx.Query(r, "direction"); d != "" {
		args = append(args, d)
		cond += fmt.Sprintf(" AND c.direction=$%d", len(args))
	}
	if pid := httpx.Query(r, "party_id"); pid != "" {
		args = append(args, pid)
		cond += fmt.Sprintf(" AND c.party_id::text=$%d", len(args))
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
	if err := rows.Err(); err != nil {
		return err
	}
	if wantsCSV(r) {
		var out [][]string
		for _, c := range items {
			out = append(out, []string{c.Direction, c.ChequeNo, c.ChequeDate, c.PartyName, c.DraweeBank, paiseStr(c.AmountPaise), c.Status,
				deref(c.DepositedOn), deref(c.ClearedOn), deref(c.BouncedOn), c.BounceReason})
		}
		return writeCSV(w, "cheque-register", []string{"Direction", "Cheque no", "Cheque date", "Party", "Drawee bank", "Amount", "Status",
			"Deposited", "Cleared", "Bounced", "Bounce reason"}, out)
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
	Reason string `json:"reason"` // bounce reason (e.g. "funds insufficient")
}

// handleChequeStatus moves a cheque through its lifecycle (see the table at
// the top of this file).
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
		ctx := r.Context()
		if err := setActor(ctx, tx, p.UserID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM cheques WHERE tenant_id=$1 AND id::text=$2 FOR UPDATE`, p.TenantID, id); err != nil {
			return err
		}
		c, err := s.getCheque(ctx, tx, p.TenantID, id)
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
		var voucherID, clearID, bounceID *string
		setParty := func(v posting.Voucher) error {
			posted, err := Post(ctx, tx, p.TenantID, p.UserID(), v)
			if err == nil {
				voucherID = &posted.ID
			}
			return err
		}
		reverseParty := func() error {
			if c.VoucherID == nil {
				return nil
			}
			rv, err := Reverse(ctx, tx, p.TenantID, p.UserID(), *c.VoucherID, date, posting.SrcChequeBounce, c.ID,
				fmt.Sprintf("cheque %s %s %s", c.ChequeNo, in.Status, in.Reason))
			if err == nil {
				bounceID = &rv.ID
			}
			return err
		}
		switch {
		case c.Direction == "received" && in.Status == "deposited":
			if c.VoucherID == nil && c.ReceivedOn != nil { // post-dated cheque reaches the bank
				if date < c.ChequeDate {
					return httpx.Conflict("cheque %s is dated %s and cannot be deposited before that", c.ChequeNo, c.ChequeDate)
				}
				if err := setParty(partyVoucher(c, date)); err != nil {
					return err
				}
			}
		case c.Direction == "received" && in.Status == "cleared":
			if c.VoucherID == nil { // recorded before phase 4: Dr Bank / Cr Party
				if err := setParty(posting.Voucher{Type: posting.VReceipt, Date: date, Source: posting.SrcCheque, SourceID: c.ID,
					SourceRef: "Cheque " + c.ChequeNo, Narration: fmt.Sprintf("Cheque %s received — %s", c.ChequeNo, c.PartyName),
					Entries: []posting.Entry{{AccountID: c.BankAccountID, Debit: c.AmountPaise}, {PartyID: c.PartyID, Credit: c.AmountPaise}}}); err != nil {
					return err
				}
				break
			}
			posted, err := Post(ctx, tx, p.TenantID, p.UserID(), posting.Voucher{Type: posting.VReceipt, Date: date,
				Source: posting.SrcChequeClear, SourceID: c.ID, SourceRef: "Cheque " + c.ChequeNo,
				Narration: fmt.Sprintf("Cheque %s cleared — %s", c.ChequeNo, c.PartyName),
				Entries:   []posting.Entry{{AccountID: c.BankAccountID, Debit: c.AmountPaise}, {AccountCode: posting.ChequesInHand, Credit: c.AmountPaise}}})
			if err != nil {
				return err
			}
			clearID = &posted.ID
		case c.Direction == "issued" && in.Status == "cleared":
			if c.VoucherID == nil { // post-dated (or pre-phase-4) issued cheque: book the payment now
				if err := setParty(partyVoucher(c, date)); err != nil {
					return err
				}
			}
		case in.Status == "bounced" || in.Status == "cancelled":
			if err := reverseParty(); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE cheques SET status=$3, status_date=$4, voucher_id=COALESCE($5,voucher_id),
			clear_voucher_id=COALESCE($6,clear_voucher_id), bounce_voucher_id=COALESCE($7,bounce_voucher_id),
			deposited_on = CASE WHEN $3='deposited' THEN $4::date ELSE deposited_on END,
			cleared_on   = CASE WHEN $3='cleared' THEN $4::date ELSE cleared_on END,
			bounced_on   = CASE WHEN $3 IN ('bounced','cancelled') THEN $4::date ELSE bounced_on END,
			bounce_reason = COALESCE($9, bounce_reason),
			reconciled_at = CASE WHEN $3='cleared' THEN $4::date ELSE reconciled_at END,
			notes=COALESCE($8,notes) WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, id, in.Status, date, voucherID, clearID, bounceID, nilIfEmpty(in.Notes), nilIfEmpty(in.Reason))
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

// handlePDCReport: GET /v1/cheques/pdc?as_of=&days=7 — pending cheques by
// due (cheque) date: overdue for presentation, due within `days`, later, and
// deposited cheques awaiting clearance.
func (s *Service) handlePDCReport(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	asOf, err := s.asOf(r)
	if err != nil {
		return err
	}
	days := 7
	if d := httpx.Query(r, "days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil || days < 0 || days > 366 {
			return httpx.Validation("days must be 0..366")
		}
	}
	args := []any{p.TenantID}
	cond := "c.tenant_id=$1 AND c.status IN ('received','issued','deposited')"
	if d := httpx.Query(r, "direction"); d != "" {
		args = append(args, d)
		cond += fmt.Sprintf(" AND c.direction=$%d", len(args))
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+chequeCols+` FROM cheques c JOIN parties p ON p.id=c.party_id
		WHERE `+cond+` ORDER BY c.cheque_date, p.name`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		Cheque
		DaysToDue int    `json:"days_to_due"` // negative = past the cheque date
		Bucket    string `json:"bucket"`      // overdue | due_soon | later | awaiting_clearance
	}
	asOfT, _ := time.Parse(time.DateOnly, asOf)
	out := []item{}
	totals := map[string]map[string]int64{}
	for rows.Next() {
		c, err := scanCheque(rows)
		if err != nil {
			return err
		}
		cd, _ := time.Parse(time.DateOnly, c.ChequeDate)
		it := item{Cheque: *c, DaysToDue: int(cd.Sub(asOfT).Hours() / 24)}
		switch {
		case c.Status == "deposited":
			it.Bucket = "awaiting_clearance"
		case it.DaysToDue <= 0:
			it.Bucket = "overdue"
		case it.DaysToDue <= days:
			it.Bucket = "due_soon"
		default:
			it.Bucket = "later"
		}
		if totals[c.Direction] == nil {
			totals[c.Direction] = map[string]int64{}
		}
		totals[c.Direction][it.Bucket] += c.AmountPaise
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if wantsCSV(r) {
		var csvRows [][]string
		for _, it := range out {
			csvRows = append(csvRows, []string{it.Direction, it.ChequeNo, it.ChequeDate, fmt.Sprint(it.DaysToDue), it.Bucket, it.PartyName,
				it.DraweeBank, paiseStr(it.AmountPaise), it.Status})
		}
		return writeCSV(w, "pdc-"+asOf, []string{"Direction", "Cheque no", "Cheque date", "Days to due", "Bucket", "Party", "Drawee bank",
			"Amount", "Status"}, csvRows)
	}
	return httpx.OK(w, map[string]any{"as_of": asOf, "days": days, "items": out, "totals_paise": totals})
}
