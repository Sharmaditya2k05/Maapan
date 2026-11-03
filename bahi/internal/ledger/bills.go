package ledger

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Bill-wise (against-reference) accounting. A bill is the amount a
// sales/purchase/opening voucher puts on a party; settling vouchers
// (receipts, payments, credit/debit notes, TDS journals) are allocated to
// bills, and whatever they do not allocate is on account. The party ledger
// balance always equals Σ bill outstanding + unallocated movements.

// billTypes raise bills, and which side of the party they raise them on.
var billTypes = map[string]string{posting.VSales: "dr", posting.VPurchase: "cr", posting.VOpening: "any"}

type partyLine struct {
	partyID       string
	debit, credit int64
}

// raiseBills inserts one bill per party a bill-type voucher leaves a balance on.
func raiseBills(ctx context.Context, tx pgx.Tx, tenantID string, p Posted, v posting.Voucher, lines []partyLine) error {
	want, ok := billTypes[v.Type]
	if !ok || len(lines) == 0 {
		return nil
	}
	net := map[string]int64{}
	var order []string
	for _, l := range lines {
		if _, seen := net[l.partyID]; !seen {
			order = append(order, l.partyID)
		}
		net[l.partyID] += l.debit - l.credit
	}
	ref := v.SourceRef
	if ref == "" {
		ref = p.Number
	}
	for _, pid := range order {
		n := net[pid]
		side := "dr"
		if n < 0 {
			side, n = "cr", -n
		}
		if n == 0 || (want != "any" && want != side) {
			continue
		}
		var credit int
		if err := tx.QueryRow(ctx, `SELECT credit_days FROM parties WHERE tenant_id=$1 AND id=$2`, tenantID, pid).Scan(&credit); err != nil {
			return err
		}
		due := v.DueDate
		if _, err := time.Parse(time.DateOnly, due); err != nil || due < v.Date {
			d, _ := time.Parse(time.DateOnly, v.Date)
			due = d.AddDate(0, 0, credit).Format(time.DateOnly)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bills (tenant_id, party_id, voucher_id, side, ref, bill_date, due_date, amount_paise)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id, voucher_id, party_id) DO NOTHING`,
			tenantID, pid, p.ID, side, ref, v.Date, due, n); err != nil {
			return err
		}
	}
	return nil
}

// opposite is the side a settlement of a bill on side must be on.
func opposite(side string) string {
	if side == "dr" {
		return "cr"
	}
	return "dr"
}

// available is how much of voucherID's movement on partyID (on settleSide)
// is not yet allocated to bills.
func available(ctx context.Context, q db.Querier, tenantID, voucherID, partyID, settleSide string) (int64, error) {
	var net, used int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN $4='cr' THEN credit_paise - debit_paise ELSE debit_paise - credit_paise END),0)
		FROM voucher_entries WHERE tenant_id=$1 AND voucher_id=$2 AND party_id=$3`, tenantID, voucherID, partyID, settleSide).Scan(&net)
	if err != nil {
		return 0, err
	}
	err = q.QueryRow(ctx, `SELECT COALESCE(sum(amount_paise),0) FROM bill_allocations WHERE tenant_id=$1 AND voucher_id=$2 AND party_id=$3`,
		tenantID, voucherID, partyID).Scan(&used)
	return max(net-used, 0), err
}

// allocate ties amount of voucherID to billID (inside tx). amount<=0 means
// "as much as possible"; the allocated amount is returned.
func allocate(ctx context.Context, tx pgx.Tx, tenantID, userID, voucherID, billID string, amount int64, method string) (int64, error) {
	var partyID, side, status, billVoucher string
	var billAmt int64
	err := tx.QueryRow(ctx, `SELECT party_id, side, status, voucher_id, amount_paise FROM bills WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		tenantID, billID).Scan(&partyID, &side, &status, &billVoucher, &billAmt)
	if db.IsNotFound(err) || isBadUUID(err) {
		return 0, httpx.Validation("bill %s not found", billID)
	}
	if err != nil {
		return 0, err
	}
	if status != "open" {
		return 0, httpx.Conflict("bill %s is cancelled", billID)
	}
	if billVoucher == voucherID {
		return 0, httpx.Validation("a voucher cannot settle its own bill")
	}
	var vstatus string
	var vdate time.Time
	err = tx.QueryRow(ctx, `SELECT status, vdate FROM vouchers WHERE tenant_id=$1 AND id=$2`, tenantID, voucherID).Scan(&vstatus, &vdate)
	if db.IsNotFound(err) || isBadUUID(err) {
		return 0, httpx.NotFound("voucher not found")
	}
	if err != nil {
		return 0, err
	}
	if vstatus != "posted" {
		return 0, httpx.Conflict("a reversed voucher cannot be allocated")
	}
	var used int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_paise),0) FROM bill_allocations WHERE bill_id=$1`, billID).Scan(&used); err != nil {
		return 0, err
	}
	outstanding := billAmt - used
	avail, err := available(ctx, tx, tenantID, voucherID, partyID, opposite(side))
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		amount = min(outstanding, avail)
		if amount <= 0 {
			return 0, nil
		}
	}
	if amount > outstanding {
		return 0, httpx.Validation("bill %s has only %d paise outstanding", billID, outstanding)
	}
	if amount > avail {
		return 0, httpx.Validation("voucher has only %d paise unallocated for this party", avail)
	}
	_, err = tx.Exec(ctx, `INSERT INTO bill_allocations (tenant_id, bill_id, voucher_id, party_id, amount_paise, alloc_date, method, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (bill_id, voucher_id) DO UPDATE SET amount_paise = bill_allocations.amount_paise + EXCLUDED.amount_paise`,
		tenantID, billID, voucherID, partyID, amount, vdate, method, userID)
	return amount, err
}

// allocateAgainstSource allocates a just-posted voucher against the bill of
// the document (source, sourceID) — e.g. the invoice a payment or a return
// refers to. A missing bill (invoice not posted yet) leaves it on account.
func allocateAgainstSource(ctx context.Context, tx pgx.Tx, tenantID, userID, voucherID, source, sourceID string) error {
	rows, err := tx.Query(ctx, `SELECT b.id FROM bills b JOIN vouchers v ON v.id=b.voucher_id
		WHERE v.tenant_id=$1 AND v.source=$2 AND v.source_id=$3 AND b.status='open'
		  AND b.party_id IN (SELECT party_id FROM voucher_entries WHERE voucher_id=$4 AND party_id IS NOT NULL)`,
		tenantID, source, sourceID, voucherID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := allocate(ctx, tx, tenantID, userID, voucherID, id, 0, "event"); err != nil {
			if httpx.AsError(err).Status < 500 {
				continue // e.g. opposite side; stays on account
			}
			return err
		}
	}
	return nil
}

// BillAllocation is one settlement of a bill.
type BillAllocation struct {
	ID            string `json:"id"`
	BillID        string `json:"bill_id"`
	BillRef       string `json:"bill_ref"`
	VoucherID     string `json:"voucher_id"`
	VoucherNumber string `json:"voucher_number"`
	VoucherType   string `json:"voucher_type"`
	Date          string `json:"date"`
	AmountPaise   int64  `json:"amount_paise"`
	Method        string `json:"method"`
}

// Bill is a bill with its allocation state.
type Bill struct {
	ID               string           `json:"id"`
	PartyID          string           `json:"party_id"`
	PartyName        string           `json:"party_name,omitempty"`
	VoucherID        string           `json:"voucher_id"`
	VoucherNumber    string           `json:"voucher_number"`
	VoucherType      string           `json:"voucher_type"`
	Side             string           `json:"side"` // dr receivable, cr payable
	Ref              string           `json:"ref"`
	BillDate         string           `json:"bill_date"`
	DueDate          string           `json:"due_date"`
	AmountPaise      int64            `json:"amount_paise"`
	AllocatedPaise   int64            `json:"allocated_paise"`
	OutstandingPaise int64            `json:"outstanding_paise"`
	OverdueDays      int              `json:"overdue_days"` // 0 when not yet due
	Status           string           `json:"status"`
	Allocations      []BillAllocation `json:"allocations,omitempty"`
}

// partyBills loads a party's bills as of asOf (allocations dated after asOf
// are ignored). openOnly drops settled and cancelled bills.
func (s *Service) partyBills(ctx context.Context, q db.Querier, tenantID, partyID, asOf string, openOnly, withAllocs bool) ([]Bill, error) {
	rows, err := q.Query(ctx, `SELECT b.id, b.party_id, p.name, b.voucher_id, v.number, v.vtype, b.side, b.ref, b.bill_date, b.due_date,
			b.amount_paise, COALESCE((SELECT sum(a.amount_paise) FROM bill_allocations a WHERE a.bill_id=b.id AND a.alloc_date <= $3),0), b.status
		FROM bills b JOIN vouchers v ON v.id=b.voucher_id JOIN parties p ON p.id=b.party_id
		WHERE b.tenant_id=$1 AND b.party_id=$2 AND b.bill_date <= $3
		ORDER BY b.due_date, b.bill_date, v.number`, tenantID, partyID, asOf)
	if err != nil {
		return nil, err
	}
	asOfT, _ := time.Parse(time.DateOnly, asOf)
	out := []Bill{}
	var ids []string
	for rows.Next() {
		var b Bill
		var bd, dd time.Time
		if err := rows.Scan(&b.ID, &b.PartyID, &b.PartyName, &b.VoucherID, &b.VoucherNumber, &b.VoucherType, &b.Side, &b.Ref, &bd, &dd,
			&b.AmountPaise, &b.AllocatedPaise, &b.Status); err != nil {
			rows.Close()
			return nil, err
		}
		b.BillDate, b.DueDate = bd.Format(time.DateOnly), dd.Format(time.DateOnly)
		if b.Status == "open" {
			b.OutstandingPaise = b.AmountPaise - b.AllocatedPaise
		}
		if b.OutstandingPaise > 0 && asOfT.After(dd) {
			b.OverdueDays = int(asOfT.Sub(dd).Hours() / 24)
		}
		if openOnly && b.OutstandingPaise == 0 {
			continue
		}
		out = append(out, b)
		ids = append(ids, b.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil || !withAllocs || len(ids) == 0 {
		return out, err
	}
	allocs, err := loadAllocations(ctx, q, tenantID, "a.bill_id = ANY($2::uuid[]) AND a.alloc_date <= $3", ids, asOf)
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, a := range allocs {
			if a.BillID == out[i].ID {
				out[i].Allocations = append(out[i].Allocations, a)
			}
		}
	}
	return out, nil
}

func loadAllocations(ctx context.Context, q db.Querier, tenantID, cond string, args ...any) ([]BillAllocation, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.bill_id, b.ref, a.voucher_id, v.number, v.vtype, a.alloc_date, a.amount_paise, a.method
		FROM bill_allocations a JOIN bills b ON b.id=a.bill_id JOIN vouchers v ON v.id=a.voucher_id
		WHERE a.tenant_id=$1 AND `+cond+` ORDER BY a.alloc_date, v.number`, append([]any{tenantID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BillAllocation{}
	for rows.Next() {
		var a BillAllocation
		var d time.Time
		if err := rows.Scan(&a.ID, &a.BillID, &a.BillRef, &a.VoucherID, &a.VoucherNumber, &a.VoucherType, &d, &a.AmountPaise, &a.Method); err != nil {
			return nil, err
		}
		a.Date = d.Format(time.DateOnly)
		out = append(out, a)
	}
	return out, rows.Err()
}

// OnAccount is a settling voucher's unallocated part.
type OnAccount struct {
	VoucherID        string `json:"voucher_id"`
	VoucherNumber    string `json:"voucher_number"`
	VoucherType      string `json:"voucher_type"`
	Date             string `json:"date"`
	Side             string `json:"side"` // side of the movement on the party (cr = settles receivables)
	AmountPaise      int64  `json:"amount_paise"`
	AllocatedPaise   int64  `json:"allocated_paise"`
	UnallocatedPaise int64  `json:"unallocated_paise"`
}

// unallocated lists a party's posted non-bill vouchers with a movement not
// fully allocated to bills, as of asOf.
func (s *Service) unallocated(ctx context.Context, q db.Querier, tenantID, partyID, asOf string) ([]OnAccount, error) {
	rows, err := q.Query(ctx, `SELECT v.id, v.number, v.vtype, v.vdate, sum(e.debit_paise - e.credit_paise),
			COALESCE((SELECT sum(a.amount_paise) FROM bill_allocations a WHERE a.voucher_id=v.id AND a.party_id=$2),0)
		FROM vouchers v JOIN voucher_entries e ON e.voucher_id=v.id AND e.party_id=$2
		WHERE v.tenant_id=$1 AND v.vdate <= $3
		  AND NOT EXISTS (SELECT 1 FROM bills b WHERE b.voucher_id=v.id AND b.party_id=$2)
		  AND v.status='posted' AND v.reversal_of IS NULL
		GROUP BY v.id ORDER BY v.vdate, v.number`, tenantID, partyID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OnAccount{}
	for rows.Next() {
		var o OnAccount
		var d time.Time
		var net int64
		if err := rows.Scan(&o.VoucherID, &o.VoucherNumber, &o.VoucherType, &d, &net, &o.AllocatedPaise); err != nil {
			return nil, err
		}
		o.Date = d.Format(time.DateOnly)
		o.Side, o.AmountPaise = "dr", net
		if net < 0 {
			o.Side, o.AmountPaise = "cr", -net
		}
		o.UnallocatedPaise = o.AmountPaise - o.AllocatedPaise
		if o.UnallocatedPaise <= 0 {
			continue
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Service) asOf(r *http.Request) (string, error) {
	a := orDefault(httpx.Query(r, "as_of"), s.today())
	if _, err := time.Parse(time.DateOnly, a); err != nil {
		return "", httpx.Validation("as_of must be YYYY-MM-DD")
	}
	return a, nil
}

// handlePartyBills: GET /v1/parties/{id}/bills?status=open|all&as_of=
func (s *Service) handlePartyBills(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	asOf, err := s.asOf(r)
	if err != nil {
		return err
	}
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	bills, err := s.partyBills(r.Context(), s.DB, p.TenantID, party.ID, asOf, httpx.Query(r, "status") != "all", true)
	if err != nil {
		return err
	}
	if wantsCSV(r) {
		var out [][]string
		for _, b := range bills {
			out = append(out, []string{b.Ref, b.VoucherNumber, b.BillDate, b.DueDate, b.Side, paiseStr(b.AmountPaise),
				paiseStr(b.AllocatedPaise), paiseStr(b.OutstandingPaise), fmt.Sprint(b.OverdueDays), b.Status})
		}
		return writeCSV(w, "bills-"+party.Name+"-"+asOf, []string{"Ref", "Voucher", "Bill date", "Due date", "Side", "Amount",
			"Settled", "Outstanding", "Overdue days", "Status"}, out)
	}
	return httpx.OK(w, map[string]any{"party_id": party.ID, "party_name": party.Name, "as_of": asOf, "items": bills})
}

// BillwiseStatement is GET /v1/parties/{id}/statement?view=bills.
type BillwiseStatement struct {
	PartyID          string      `json:"party_id"`
	PartyName        string      `json:"party_name"`
	AsOf             string      `json:"as_of"`
	CreditDays       int         `json:"credit_days"`
	Bills            []Bill      `json:"bills"`
	OnAccount        []OnAccount `json:"on_account"`
	BalancePaise     int64       `json:"balance_paise"` // ledger balance, Dr +
	OutstandingPaise int64       `json:"bills_outstanding_paise"`
	OverduePaise     int64       `json:"overdue_paise"`
	UnallocatedPaise int64       `json:"unallocated_paise"` // balance − Σ signed outstanding (− = on-account credit)
}

func (s *Service) billwise(ctx context.Context, tenantID string, party *Party, asOf string, all bool) (*BillwiseStatement, error) {
	bills, err := s.partyBills(ctx, s.DB, tenantID, party.ID, asOf, !all, true)
	if err != nil {
		return nil, err
	}
	on, err := s.unallocated(ctx, s.DB, tenantID, party.ID, asOf)
	if err != nil {
		return nil, err
	}
	st := &BillwiseStatement{PartyID: party.ID, PartyName: party.Name, AsOf: asOf, CreditDays: party.CreditDays, Bills: bills, OnAccount: on}
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(sum(debit_paise - credit_paise),0) FROM voucher_entries
		WHERE tenant_id=$1 AND account_id=$2 AND vdate <= $3`, tenantID, party.AccountID, asOf).Scan(&st.BalancePaise); err != nil {
		return nil, err
	}
	var signed int64
	for _, b := range bills {
		o := b.OutstandingPaise
		if b.Side == "cr" {
			o = -o
		}
		signed += o
		st.OutstandingPaise += b.OutstandingPaise
		if b.OverdueDays > 0 {
			st.OverduePaise += b.OutstandingPaise
		}
	}
	st.UnallocatedPaise = st.BalancePaise - signed
	return st, nil
}

type allocationInput struct {
	BillID      string `json:"bill_id"`
	AmountPaise int64  `json:"amount_paise"`
}

type allocationsInput struct {
	Allocations []allocationInput `json:"allocations"` // empty = everything on account
}

func (in *allocationsInput) Validate() error {
	if len(in.Allocations) > 500 {
		return httpx.Validation("at most 500 allocations")
	}
	seen := map[string]bool{}
	for i, a := range in.Allocations {
		if a.BillID == "" || a.AmountPaise <= 0 {
			return httpx.Validation("allocation %d: bill_id and a positive amount_paise are required", i+1)
		}
		if seen[a.BillID] {
			return httpx.Validation("allocation %d: bill %s listed twice", i+1, a.BillID)
		}
		seen[a.BillID] = true
	}
	return nil
}

// replaceAllocations sets voucherID's allocations to exactly ins.
func replaceAllocations(ctx context.Context, tx pgx.Tx, tenantID, userID, voucherID string, ins []allocationInput) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM vouchers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, voucherID); err != nil {
		if isBadUUID(err) {
			return httpx.NotFound("voucher not found")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bill_allocations WHERE tenant_id=$1 AND voucher_id=$2`, tenantID, voucherID); err != nil {
		return err
	}
	for _, a := range ins {
		if _, err := allocate(ctx, tx, tenantID, userID, voucherID, a.BillID, a.AmountPaise, "manual"); err != nil {
			return err
		}
	}
	return nil
}

// handlePutAllocations: PUT /v1/vouchers/{id}/allocations replaces the
// voucher's against-reference allocations; the rest is on account.
func (s *Service) handlePutAllocations(w http.ResponseWriter, r *http.Request) error {
	var in allocationsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if _, err := s.getVoucher(r.Context(), p.TenantID, id); err != nil {
		return err
	}
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		return replaceAllocations(r.Context(), tx, p.TenantID, p.UserID(), id, in.Allocations)
	})
	if err != nil {
		return err
	}
	return s.writeVoucherAllocations(w, r, p.TenantID, id)
}

func (s *Service) handleGetAllocations(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if _, err := s.getVoucher(r.Context(), p.TenantID, id); err != nil {
		return err
	}
	return s.writeVoucherAllocations(w, r, p.TenantID, id)
}

func (s *Service) writeVoucherAllocations(w http.ResponseWriter, r *http.Request, tenantID, voucherID string) error {
	allocs, err := loadAllocations(r.Context(), s.DB, tenantID, "a.voucher_id=$2", voucherID)
	if err != nil {
		return err
	}
	var total int64
	for _, a := range allocs {
		total += a.AmountPaise
	}
	return httpx.OK(w, map[string]any{"voucher_id": voucherID, "allocations": allocs, "allocated_paise": total})
}

// autoAllocate settles a party's open bills FIFO by due date with its
// unallocated settlements (oldest first). Returns the number of allocations.
func autoAllocate(ctx context.Context, tx pgx.Tx, s *Service, tenantID, userID, partyID string) (int, error) {
	asOf := "9999-12-31"
	bills, err := s.partyBills(ctx, tx, tenantID, partyID, asOf, true, false)
	if err != nil {
		return 0, err
	}
	on, err := s.unallocated(ctx, tx, tenantID, partyID, asOf)
	if err != nil {
		return 0, err
	}
	sort.SliceStable(bills, func(i, j int) bool { return bills[i].DueDate < bills[j].DueDate })
	n := 0
	for _, o := range on {
		for i := range bills {
			b := &bills[i]
			if b.OutstandingPaise == 0 || opposite(b.Side) != o.Side || o.UnallocatedPaise == 0 {
				continue
			}
			amt, err := allocate(ctx, tx, tenantID, userID, o.VoucherID, b.ID, min(b.OutstandingPaise, o.UnallocatedPaise), "auto")
			if err != nil {
				return n, err
			}
			b.OutstandingPaise -= amt
			o.UnallocatedPaise -= amt
			n++
		}
	}
	return n, nil
}

// handleAutoAllocate: POST /v1/parties/{id}/allocations/auto
func (s *Service) handleAutoAllocate(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	var n int
	err = s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('bahi-alloc:' || $1))`, party.ID); err != nil {
			return err
		}
		var err error
		n, err = autoAllocate(r.Context(), tx, s, p.TenantID, p.UserID(), party.ID)
		return err
	})
	if err != nil {
		return err
	}
	st, err := s.billwise(r.Context(), p.TenantID, party, s.today(), false)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"allocations_created": n, "statement": st})
}

// handleBillwiseStatement is the ?view=bills variant of the party statement.
func (s *Service) handleBillwiseStatement(w http.ResponseWriter, r *http.Request, party *Party) error {
	p := auth.FromContext(r.Context())
	asOf, err := s.asOf(r)
	if err != nil {
		return err
	}
	st, err := s.billwise(r.Context(), p.TenantID, party, asOf, httpx.Query(r, "status") == "all")
	if err != nil {
		return err
	}
	if wantsCSV(r) {
		var out [][]string
		for _, b := range st.Bills {
			out = append(out, []string{"Bill", b.Ref, b.BillDate, b.DueDate, paiseStr(b.AmountPaise), "", paiseStr(b.OutstandingPaise), fmt.Sprint(b.OverdueDays)})
			for _, a := range b.Allocations {
				out = append(out, []string{"  " + a.VoucherType, a.VoucherNumber, a.Date, "", "", paiseStr(a.AmountPaise), "", ""})
			}
		}
		for _, o := range st.OnAccount {
			out = append(out, []string{"On account", o.VoucherNumber, o.Date, "", "", paiseStr(o.UnallocatedPaise), "", ""})
		}
		out = append(out, []string{"Ledger balance", "", st.AsOf, "", "", "", paiseStr(st.BalancePaise), ""})
		return writeCSV(w, "billwise-"+party.Name+"-"+asOf, []string{"Kind", "Ref / voucher", "Date", "Due", "Bill amount", "Settled", "Outstanding", "Overdue days"}, out)
	}
	return httpx.OK(w, st)
}

// handleInternalCredit: GET /internal/v1/parties/{id}/credit — outstanding vs
// credit limit for aadesh to enforce at billing.
func (s *Service) handleInternalCredit(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.BadRequest("X-Tenant-ID header is required")
	}
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	st, err := s.billwise(r.Context(), p.TenantID, party, s.today(), false)
	if err != nil {
		return err
	}
	headroom := int64(-1) // no limit
	if party.CreditLimitPaise > 0 {
		headroom = max(party.CreditLimitPaise-st.BalancePaise, 0)
	}
	return httpx.OK(w, map[string]any{"party_id": party.ID, "credit_limit_paise": party.CreditLimitPaise, "credit_days": party.CreditDays,
		"balance_paise": st.BalancePaise, "overdue_paise": st.OverduePaise, "available_paise": headroom,
		"over_limit": party.CreditLimitPaise > 0 && st.BalancePaise > party.CreditLimitPaise})
}

// paiseStr renders paise as rupees with two decimals, without float.
func paiseStr(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	return fmt.Sprintf("%s%d.%02d", sign, p/100, p%100)
}
