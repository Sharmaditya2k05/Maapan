package ledger

import (
	"net/http"
	"sort"
	"time"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// DueAgingRow is one party's outstanding by days past due.
type DueAgingRow struct {
	PartyID    string `json:"party_id"`
	PartyName  string `json:"party_name"`
	Phone      string `json:"phone,omitempty"`
	CreditDays int    `json:"credit_days"`
	OpenBills  int    `json:"open_bills"`
	posting.DueBuckets
}

// handleDueAging ages open bills by due date (bill date + party
// credit_days, or the due date aadesh sent). Unallocated receipts/payments
// are shown as on account and netted, so net = ledger balance.
func (s *Service) handleDueAging(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	kind := orDefault(httpx.Query(r, "kind"), "receivables")
	if kind != "receivables" && kind != "payables" {
		return httpx.Validation("kind must be receivables or payables")
	}
	asOfStr, err := s.asOf(r)
	if err != nil {
		return err
	}
	asOf, _ := time.Parse(time.DateOnly, asOfStr)
	types, side, sign := []string{"customer", "both"}, "dr", int64(1)
	if kind == "payables" {
		types, side, sign = []string{"supplier", "both"}, "cr", -1
	}
	type acc struct {
		row   DueAgingRow
		bills []posting.OpenBill
		bal   int64
	}
	byParty := map[string]*acc{}
	var order []string
	get := func(id, name, phone string, cd int) *acc {
		a, ok := byParty[id]
		if !ok {
			a = &acc{row: DueAgingRow{PartyID: id, PartyName: name, Phone: phone, CreditDays: cd}}
			byParty[id] = a
			order = append(order, id)
		}
		return a
	}
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.name, COALESCE(p.phone,''), p.credit_days, COALESCE(sum(e.debit_paise - e.credit_paise),0)
		FROM parties p JOIN voucher_entries e ON e.account_id=p.account_id AND e.tenant_id=p.tenant_id
		WHERE p.tenant_id=$1 AND p.type = ANY($2) AND e.vdate <= $3
		GROUP BY p.id ORDER BY p.name`, p.TenantID, types, asOfStr)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, phone string
		var cd int
		var bal int64
		if err := rows.Scan(&id, &name, &phone, &cd, &bal); err != nil {
			rows.Close()
			return err
		}
		get(id, name, phone, cd).bal = bal * sign
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.DB.Query(r.Context(), `SELECT p.id, p.name, COALESCE(p.phone,''), p.credit_days, b.bill_date, b.due_date,
			b.amount_paise - COALESCE((SELECT sum(a.amount_paise) FROM bill_allocations a WHERE a.bill_id=b.id AND a.alloc_date <= $3),0)
		FROM bills b JOIN parties p ON p.id=b.party_id
		WHERE b.tenant_id=$1 AND p.type = ANY($2) AND b.bill_date <= $3 AND b.status='open' AND b.side=$4`,
		p.TenantID, types, asOfStr, side)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, phone string
		var cd int
		var bd, dd time.Time
		var out int64
		if err := rows.Scan(&id, &name, &phone, &cd, &bd, &dd, &out); err != nil {
			rows.Close()
			return err
		}
		if out <= 0 {
			continue
		}
		a := get(id, name, phone, cd)
		a.bills = append(a.bills, posting.OpenBill{BillDate: bd, DueDate: dd, Outstanding: out})
		a.row.OpenBills++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	out := []DueAgingRow{}
	var total posting.DueBuckets
	for _, id := range order {
		a := byParty[id]
		var open int64
		for _, b := range a.bills {
			open += b.Outstanding
		}
		a.row.DueBuckets = posting.AgeByDue(a.bills, a.bal-open, asOf)
		bk := a.row.DueBuckets
		if bk.Total == 0 && bk.Net == 0 && bk.OnAccount == 0 {
			continue
		}
		out = append(out, a.row)
		total.NotDue += bk.NotDue
		total.D0to30 += bk.D0to30
		total.D31to60 += bk.D31to60
		total.D61to90 += bk.D61to90
		total.Over90 += bk.Over90
		total.Total += bk.Total
		total.OnAccount += bk.OnAccount
		total.Net += bk.Net
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if wantsCSV(r) {
		var csvRows [][]string
		for _, x := range out {
			csvRows = append(csvRows, []string{x.PartyName, x.Phone, paiseStr(x.NotDue), paiseStr(x.D0to30), paiseStr(x.D31to60),
				paiseStr(x.D61to90), paiseStr(x.Over90), paiseStr(x.Total), paiseStr(x.OnAccount), paiseStr(x.Net)})
		}
		return writeCSV(w, kind+"-aging-"+asOfStr, []string{"Party", "Phone", "Not due", "0-30 overdue", "31-60", "61-90", "90+",
			"Bills outstanding", "On account", "Net balance"}, csvRows)
	}
	return httpx.OK(w, map[string]any{"kind": kind, "basis": "due", "as_of": asOfStr, "rows": out, "totals": total})
}

// OverdueRow is one customer with bills past their due date.
type OverdueRow struct {
	PartyID       string `json:"party_id"`
	PartyName     string `json:"party_name"`
	Phone         string `json:"phone"`
	Email         string `json:"email"`
	OverduePaise  int64  `json:"overdue_paise"`
	OldestDueDate string `json:"oldest_due_date"`
	DaysOverdue   int    `json:"days_overdue"` // of the oldest overdue bill
}

// handleOverdueReceivables: GET /internal/v1/receivables/overdue?as_of= —
// customers with bill-wise outstanding past the due date (bill date +
// credit_days, or aadesh's due date), for soochna's payment reminders.
func (s *Service) handleOverdueReceivables(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.BadRequest("X-Tenant-ID header is required")
	}
	asOf, err := s.asOf(r)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.name, COALESCE(p.phone,''), COALESCE(p.email,''), sum(o.out), min(o.due_date)
		FROM (SELECT b.party_id, b.due_date,
		             b.amount_paise - COALESCE((SELECT sum(a.amount_paise) FROM bill_allocations a WHERE a.bill_id=b.id AND a.alloc_date <= $2),0) AS out
		        FROM bills b
		       WHERE b.tenant_id=$1 AND b.status='open' AND b.side='dr' AND b.bill_date <= $2 AND b.due_date < $2) o
		JOIN parties p ON p.id=o.party_id
		WHERE o.out > 0 AND p.type IN ('customer','both') AND NOT p.archived
		GROUP BY p.id ORDER BY sum(o.out) DESC`, p.TenantID, asOf)
	if err != nil {
		return err
	}
	defer rows.Close()
	asOfT, _ := time.Parse(time.DateOnly, asOf)
	out := []OverdueRow{}
	for rows.Next() {
		var x OverdueRow
		var oldest time.Time
		if err := rows.Scan(&x.PartyID, &x.PartyName, &x.Phone, &x.Email, &x.OverduePaise, &oldest); err != nil {
			return err
		}
		x.OldestDueDate = oldest.Format(time.DateOnly)
		x.DaysOverdue = int(asOfT.Sub(oldest).Hours() / 24)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"as_of": asOf, "rows": out})
}
