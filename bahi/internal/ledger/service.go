// Package ledger is bahi's persistence and HTTP layer: chart of accounts,
// parties, voucher posting, statements, bank/cheques and reports.
package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// Service implements ledger use cases.
type Service struct {
	DB  *pgxpool.Pool
	Kar *svc.Client // optional GSTIN verification
	Log *slog.Logger
	Now func() time.Time
}

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// FinancialYear returns the Indian FY label ("26-27") for d.
func FinancialYear(d time.Time) string {
	y := d.Year()
	if d.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%02d-%02d", y%100, (y+1)%100)
}

func (s *Service) today() string { return s.Now().In(IST).Format(time.DateOnly) }

// fyStart is 1 April (IST) of the current Indian financial year.
func (s *Service) fyStart() string {
	t := s.Now().In(IST)
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%04d-04-01", y)
}

var voucherPrefix = map[string]string{
	posting.VSales: "SV", posting.VPurchase: "PV", posting.VReceipt: "RV", posting.VPayment: "PY",
	posting.VJournal: "JV", posting.VContra: "CV", posting.VCreditNote: "CN", posting.VDebitNote: "DN",
	posting.VProduction: "MF", posting.VOpening: "OB",
}

// EnsureChart seeds the default chart of accounts for tenantID (idempotent).
func EnsureChart(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE tenant_id=$1 AND code=$2)`,
		tenantID, posting.Cash).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	// Serialise concurrent first-time seeding for the same tenant.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-seed:' || $1))`, tenantID); err != nil {
		return err
	}
	for _, a := range posting.DefaultChart {
		_, err := tx.Exec(ctx, `INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_system, is_cash, is_bank)
			VALUES ($1,$2,$3,$4,(SELECT id FROM accounts WHERE tenant_id=$1 AND code=NULLIF($5,'')),$6,true,$7,$8)
			ON CONFLICT (tenant_id, code) DO NOTHING`,
			tenantID, a.Code, a.Name, a.Group, a.Parent, a.IsGroup, a.IsCash, a.IsBank)
		if err != nil {
			return err
		}
	}
	return nil
}

// Posted is the result of posting a voucher.
type Posted struct {
	ID      string `json:"id"`
	Number  string `json:"number"`
	Created bool   `json:"created"`
}

// Post resolves account codes/parties, numbers and inserts a balanced voucher
// inside tx. Vouchers with a SourceID are idempotent: re-posting returns the
// existing voucher with Created=false, and concurrent deliveries of the same
// source document are serialised on a transaction-scoped advisory lock so the
// loser sees the winner's row instead of a unique violation. A voucher dated
// inside the tenant's locked period is refused with 409 period_locked.
func Post(ctx context.Context, tx pgx.Tx, tenantID, userID string, v posting.Voucher) (Posted, error) {
	if err := v.Validate(); err != nil {
		return Posted{}, httpx.Validation("%s", err.Error())
	}
	if err := EnsureChart(ctx, tx, tenantID); err != nil {
		return Posted{}, err
	}
	if v.SourceID != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text || ':' || $2::text || ':' || $3::text))`,
			tenantID, v.Source, v.SourceID); err != nil {
			return Posted{}, err
		}
		var p Posted
		err := tx.QueryRow(ctx, `SELECT id, number FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_id=$3`,
			tenantID, v.Source, v.SourceID).Scan(&p.ID, &p.Number)
		if err == nil {
			return p, nil
		}
		if !db.IsNotFound(err) {
			return Posted{}, err
		}
	}
	date, err := time.ParseInLocation(time.DateOnly, v.Date, IST)
	if err != nil {
		return Posted{}, httpx.Validation("date must be YYYY-MM-DD")
	}
	if err := checkPeriodLock(ctx, tx, tenantID, v.Date); err != nil {
		return Posted{}, err
	}
	prefix, ok := voucherPrefix[v.Type]
	if !ok {
		return Posted{}, httpx.Validation("unknown voucher type %q", v.Type)
	}

	type resolved struct {
		accountID string
		partyID   *string
	}
	res := make([]resolved, len(v.Entries))
	codeCache := map[string]string{}
	for i, e := range v.Entries {
		switch {
		case e.PartyID != "":
			var acct string
			err := tx.QueryRow(ctx, `SELECT account_id FROM parties WHERE tenant_id=$1 AND id=$2`, tenantID, e.PartyID).Scan(&acct)
			if db.IsNotFound(err) || isBadUUID(err) {
				return Posted{}, httpx.Validation("entry %d: party %s not found", i+1, e.PartyID)
			}
			if err != nil {
				return Posted{}, err
			}
			pid := e.PartyID
			res[i] = resolved{accountID: acct, partyID: &pid}
		case e.AccountCode != "":
			id, ok := codeCache[e.AccountCode]
			if !ok {
				var isGroup bool
				err := tx.QueryRow(ctx, `SELECT id, is_group FROM accounts WHERE tenant_id=$1 AND code=$2`, tenantID, e.AccountCode).Scan(&id, &isGroup)
				if db.IsNotFound(err) {
					return Posted{}, httpx.Validation("entry %d: account code %s not found", i+1, e.AccountCode)
				}
				if err != nil {
					return Posted{}, err
				}
				if isGroup {
					return Posted{}, httpx.Validation("entry %d: cannot post to group account %s", i+1, e.AccountCode)
				}
				codeCache[e.AccountCode] = id
			}
			res[i] = resolved{accountID: id}
		default:
			var isGroup, archived bool
			err := tx.QueryRow(ctx, `SELECT is_group, archived FROM accounts WHERE tenant_id=$1 AND id=$2`, tenantID, e.AccountID).Scan(&isGroup, &archived)
			if db.IsNotFound(err) || isBadUUID(err) {
				return Posted{}, httpx.Validation("entry %d: account %s not found", i+1, e.AccountID)
			}
			if err != nil {
				return Posted{}, err
			}
			if isGroup || archived {
				return Posted{}, httpx.Validation("entry %d: account %s is a group or archived", i+1, e.AccountID)
			}
			// Posting straight to a party's account still tags the party.
			var pid *string
			_ = tx.QueryRow(ctx, `SELECT id FROM parties WHERE tenant_id=$1 AND account_id=$2`, tenantID, e.AccountID).Scan(&pid)
			res[i] = resolved{accountID: e.AccountID, partyID: pid}
		}
	}

	fy := FinancialYear(date)
	var seq int64
	if err := tx.QueryRow(ctx, `INSERT INTO voucher_series (tenant_id, vtype, fy, next_no) VALUES ($1,$2,$3,2)
		ON CONFLICT (tenant_id, vtype, fy) DO UPDATE SET next_no = voucher_series.next_no + 1
		RETURNING next_no - 1`, tenantID, v.Type, fy).Scan(&seq); err != nil {
		return Posted{}, err
	}
	p := Posted{Number: fmt.Sprintf("%s/%s/%05d", prefix, fy, seq), Created: true}
	err = tx.QueryRow(ctx, `INSERT INTO vouchers (tenant_id, vtype, number, fy, vdate, narration, source, source_id,
		source_ref, total_paise, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		tenantID, v.Type, p.Number, fy, v.Date, nilIfEmpty(v.Narration), orDefault(v.Source, posting.SrcManual),
		nilIfEmpty(v.SourceID), nilIfEmpty(v.SourceRef), v.Total(), userID).Scan(&p.ID)
	if err != nil {
		return Posted{}, err
	}
	batch := &pgx.Batch{}
	for i, e := range v.Entries {
		batch.Queue(`INSERT INTO voucher_entries (tenant_id, voucher_id, line_no, account_id, party_id, store_id, vdate,
			debit_paise, credit_paise) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			tenantID, p.ID, i+1, res[i].accountID, res[i].partyID, nilIfEmpty(e.StoreID), v.Date, e.Debit, e.Credit)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return Posted{}, err
	}
	return p, nil
}

// Reverse posts a mirror voucher dated date and marks the ORIGINAL reversed,
// pointing it at the reversal (reversed_by); the reversal voucher itself stays
// posted and points back (reversal_of). Returns the reversal; reversing an
// already-reversed voucher is a conflict.
func Reverse(ctx context.Context, tx pgx.Tx, tenantID, userID, voucherID, date, source, sourceID, reason string) (Posted, error) {
	var vtype, number, status string
	err := tx.QueryRow(ctx, `SELECT vtype, number, status FROM vouchers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		tenantID, voucherID).Scan(&vtype, &number, &status)
	if db.IsNotFound(err) || isBadUUID(err) {
		return Posted{}, httpx.NotFound("voucher not found")
	}
	if err != nil {
		return Posted{}, err
	}
	if status != "posted" {
		return Posted{}, httpx.Conflict("voucher %s is already reversed", number)
	}
	rows, err := tx.Query(ctx, `SELECT account_id, COALESCE(store_id::text,''), debit_paise, credit_paise
		FROM voucher_entries WHERE voucher_id=$1 ORDER BY line_no`, voucherID)
	if err != nil {
		return Posted{}, err
	}
	var es []posting.Entry
	for rows.Next() {
		var e posting.Entry
		if err := rows.Scan(&e.AccountID, &e.StoreID, &e.Debit, &e.Credit); err != nil {
			rows.Close()
			return Posted{}, err
		}
		es = append(es, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Posted{}, err
	}
	narr := "Reversal of " + number
	if reason != "" {
		narr += ": " + reason
	}
	rv := posting.Voucher{Type: vtype, Date: date, Narration: narr, Source: source, SourceID: sourceID,
		SourceRef: number, Entries: posting.Swap(es)}
	p, err := Post(ctx, tx, tenantID, userID, rv)
	if err != nil {
		return Posted{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE vouchers SET reversal_of=$2 WHERE tenant_id=$3 AND id=$1`, p.ID, voucherID, tenantID); err != nil {
		return Posted{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE vouchers SET status='reversed', reversed_by=$2 WHERE tenant_id=$3 AND id=$1`, voucherID, p.ID, tenantID)
	return p, err
}

// WithTx runs fn in a transaction (serialisation failures are retried once).
func (s *Service) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	err := db.WithTx(ctx, s.DB, fn)
	if isSerialization(err) {
		err = db.WithTx(ctx, s.DB, fn)
	}
	return err
}
