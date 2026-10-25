package ledger

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// accountTotals holds debit/credit sums per leaf account.
type accountTotals struct {
	ID, Code, Name, Group string
	IsCash, IsBank        bool
	Debit, Credit         int64
}

// totals sums entries per account over [from, to]. from="" means since inception.
func (s *Service) totals(ctx context.Context, tenantID, from, to string) ([]accountTotals, error) {
	if from == "" {
		from = "1900-01-01"
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.code, a.name, a.grp, a.is_cash, a.is_bank,
			COALESCE(sum(e.debit_paise),0), COALESCE(sum(e.credit_paise),0)
		FROM accounts a JOIN voucher_entries e ON e.account_id=a.id AND e.tenant_id=a.tenant_id
		WHERE a.tenant_id=$1 AND e.vdate BETWEEN $2 AND $3
		GROUP BY a.id ORDER BY a.code`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []accountTotals
	for rows.Next() {
		var t accountTotals
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.Group, &t.IsCash, &t.IsBank, &t.Debit, &t.Credit); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TBRow is one trial-balance line.
type TBRow struct {
	AccountID   string `json:"account_id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Group       string `json:"group"`
	DebitPaise  int64  `json:"debit_paise"`  // closing debit balance
	CreditPaise int64  `json:"credit_paise"` // closing credit balance
}

func (s *Service) handleTrialBalance(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	asOf := orDefault(httpx.Query(r, "as_of"), s.today())
	if _, err := time.Parse(time.DateOnly, asOf); err != nil {
		return httpx.Validation("as_of must be YYYY-MM-DD")
	}
	ts, err := s.totals(r.Context(), p.TenantID, "", asOf)
	if err != nil {
		return err
	}
	rows := []TBRow{}
	var dr, cr int64
	for _, t := range ts {
		bal := t.Debit - t.Credit
		if bal == 0 {
			continue
		}
		row := TBRow{AccountID: t.ID, Code: t.Code, Name: t.Name, Group: t.Group}
		if bal > 0 {
			row.DebitPaise, dr = bal, dr+bal
		} else {
			row.CreditPaise, cr = -bal, cr-bal
		}
		rows = append(rows, row)
	}
	if wantsCSV(r) {
		var out [][]string
		for _, x := range rows {
			out = append(out, []string{x.Code, x.Name, x.Group, rupees(x.DebitPaise), rupees(x.CreditPaise)})
		}
		out = append(out, []string{"", "Total", "", rupees(dr), rupees(cr)})
		return writeCSV(w, "trial-balance-"+asOf, []string{"Code", "Account", "Group", "Debit", "Credit"}, out)
	}
	return httpx.OK(w, map[string]any{"as_of": asOf, "rows": rows, "total_debit_paise": dr, "total_credit_paise": cr,
		"balanced": dr == cr})
}

// PLLine is a profit & loss line.
type PLLine struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	AmountPaise int64  `json:"amount_paise"`
}

// ProfitLoss is the P&L for a period.
type ProfitLoss struct {
	From             string   `json:"from"`
	To               string   `json:"to"`
	Income           []PLLine `json:"income"`
	Expenses         []PLLine `json:"expenses"`
	TotalIncome      int64    `json:"total_income_paise"`
	TotalExpenses    int64    `json:"total_expenses_paise"`
	GrossProfitPaise int64    `json:"gross_profit_paise"` // sales − returns − purchases + purchase returns − direct expenses (COGS, stock loss, …)
	NetProfitPaise   int64    `json:"net_profit_paise"`
}

// grossProfitCodes is posting.GrossProfitCodes plus every account under the
// Direct Expenses group (Cost of Goods Sold, Stock Loss, Other Direct Expenses
// and any leaf the tenant adds there).
func (s *Service) grossProfitCodes(ctx context.Context, tenantID string) (map[string]bool, error) {
	out := make(map[string]bool, len(posting.GrossProfitCodes)+4)
	for c := range posting.GrossProfitCodes {
		out[c] = true
	}
	rows, err := s.DB.Query(ctx, `WITH RECURSIVE t AS (
			SELECT id, code FROM accounts WHERE tenant_id=$1 AND code=$2
			UNION ALL
			SELECT a.id, a.code FROM accounts a JOIN t ON a.parent_id=t.id WHERE a.tenant_id=$1)
		SELECT code FROM t`, tenantID, posting.DirectExpenses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		out[code] = true
	}
	return out, rows.Err()
}

func (s *Service) profitLoss(ctx context.Context, tenantID, from, to string) (*ProfitLoss, error) {
	ts, err := s.totals(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	direct, err := s.grossProfitCodes(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	pl := &ProfitLoss{From: from, To: to, Income: []PLLine{}, Expenses: []PLLine{}}
	for _, t := range ts {
		switch t.Group {
		case "income":
			amt := t.Credit - t.Debit
			pl.Income = append(pl.Income, PLLine{t.Code, t.Name, amt})
			pl.TotalIncome += amt
			if direct[t.Code] {
				pl.GrossProfitPaise += amt
			}
		case "expense":
			amt := t.Debit - t.Credit
			pl.Expenses = append(pl.Expenses, PLLine{t.Code, t.Name, amt})
			pl.TotalExpenses += amt
			if direct[t.Code] {
				pl.GrossProfitPaise -= amt
			}
		}
	}
	pl.NetProfitPaise = pl.TotalIncome - pl.TotalExpenses
	return pl, nil
}

func (s *Service) handleProfitLoss(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	pl, err := s.profitLoss(r.Context(), p.TenantID, from, to)
	if err != nil {
		return err
	}
	if wantsCSV(r) {
		var out [][]string
		for _, l := range pl.Income {
			out = append(out, []string{"Income", l.Code, l.Name, rupees(l.AmountPaise)})
		}
		for _, l := range pl.Expenses {
			out = append(out, []string{"Expense", l.Code, l.Name, rupees(l.AmountPaise)})
		}
		out = append(out, []string{"", "", "Gross profit", rupees(pl.GrossProfitPaise)},
			[]string{"", "", "Net profit", rupees(pl.NetProfitPaise)})
		return writeCSV(w, "profit-loss-"+from+"-"+to, []string{"Section", "Code", "Account", "Amount"}, out)
	}
	return httpx.OK(w, pl)
}

// BSLine is a balance-sheet line.
type BSLine struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	AmountPaise int64  `json:"amount_paise"`
}

func (s *Service) handleBalanceSheet(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	asOf := orDefault(httpx.Query(r, "as_of"), s.today())
	if _, err := time.Parse(time.DateOnly, asOf); err != nil {
		return httpx.Validation("as_of must be YYYY-MM-DD")
	}
	ts, err := s.totals(r.Context(), p.TenantID, "", asOf)
	if err != nil {
		return err
	}
	assets, liabilities, equity := []BSLine{}, []BSLine{}, []BSLine{}
	var ta, tl, te, profit int64
	for _, t := range ts {
		switch t.Group {
		case "asset":
			if amt := t.Debit - t.Credit; amt != 0 {
				assets = append(assets, BSLine{t.Code, t.Name, amt})
				ta += amt
			}
		case "liability":
			if amt := t.Credit - t.Debit; amt != 0 {
				liabilities = append(liabilities, BSLine{t.Code, t.Name, amt})
				tl += amt
			}
		case "equity":
			if amt := t.Credit - t.Debit; amt != 0 {
				equity = append(equity, BSLine{t.Code, t.Name, amt})
				te += amt
			}
		case "income":
			profit += t.Credit - t.Debit
		case "expense":
			profit -= t.Debit - t.Credit
		}
	}
	// Accumulated profit (all periods, not yet closed to capital) sits in equity.
	equity = append(equity, BSLine{"", "Profit & Loss (accumulated)", profit})
	te += profit
	if wantsCSV(r) {
		var out [][]string
		for _, sec := range []struct {
			n  string
			ls []BSLine
		}{{"Assets", assets}, {"Liabilities", liabilities}, {"Equity", equity}} {
			for _, l := range sec.ls {
				out = append(out, []string{sec.n, l.Code, l.Name, rupees(l.AmountPaise)})
			}
		}
		return writeCSV(w, "balance-sheet-"+asOf, []string{"Section", "Code", "Account", "Amount"}, out)
	}
	return httpx.OK(w, map[string]any{"as_of": asOf, "assets": assets, "liabilities": liabilities, "equity": equity,
		"total_assets_paise": ta, "total_liabilities_paise": tl, "total_equity_paise": te,
		"balanced": ta == tl+te})
}

// AgingRow is the outstanding of one party.
type AgingRow struct {
	PartyID   string `json:"party_id"`
	PartyName string `json:"party_name"`
	Phone     string `json:"phone,omitempty"`
	posting.Buckets
}

// handleAging: ?kind=receivables (default) or payables.
func (s *Service) handleAging(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	kind := orDefault(httpx.Query(r, "kind"), "receivables")
	if kind != "receivables" && kind != "payables" {
		return httpx.Validation("kind must be receivables or payables")
	}
	asOfStr := orDefault(httpx.Query(r, "as_of"), s.today())
	asOf, err := time.Parse(time.DateOnly, asOfStr)
	if err != nil {
		return httpx.Validation("as_of must be YYYY-MM-DD")
	}
	types := []string{"customer", "both"}
	if kind == "payables" {
		types = []string{"supplier", "both"}
	}
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.name, COALESCE(p.phone,''), e.vdate, e.debit_paise, e.credit_paise
		FROM parties p JOIN voucher_entries e ON e.account_id=p.account_id AND e.tenant_id=p.tenant_id
		WHERE p.tenant_id=$1 AND p.type = ANY($2) AND e.vdate <= $3
		ORDER BY p.id, e.vdate`, p.TenantID, types, asOfStr)
	if err != nil {
		return err
	}
	type acc struct {
		name, phone string
		ms          []posting.Movement
	}
	byParty := map[string]*acc{}
	var order []string
	for rows.Next() {
		var id, name, phone string
		var d time.Time
		var dr, cr int64
		if err := rows.Scan(&id, &name, &phone, &d, &dr, &cr); err != nil {
			rows.Close()
			return err
		}
		a, ok := byParty[id]
		if !ok {
			a = &acc{name: name, phone: phone}
			byParty[id] = a
			order = append(order, id)
		}
		amt := dr - cr
		if kind == "payables" {
			amt = cr - dr
		}
		a.ms = append(a.ms, posting.Movement{Date: d, Amount: amt})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	out := []AgingRow{}
	var total posting.Buckets
	for _, id := range order {
		a := byParty[id]
		b := posting.Age(a.ms, asOf)
		if b.Total == 0 && b.Advance == 0 {
			continue
		}
		out = append(out, AgingRow{PartyID: id, PartyName: a.name, Phone: a.phone, Buckets: b})
		total.D0to30 += b.D0to30
		total.D31to60 += b.D31to60
		total.D61to90 += b.D61to90
		total.Over90 += b.Over90
		total.Total += b.Total
		total.Advance += b.Advance
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if wantsCSV(r) {
		var csvRows [][]string
		for _, x := range out {
			csvRows = append(csvRows, []string{x.PartyName, x.Phone, rupees(x.D0to30), rupees(x.D31to60),
				rupees(x.D61to90), rupees(x.Over90), rupees(x.Total), rupees(x.Advance)})
		}
		return writeCSV(w, kind+"-aging-"+asOfStr, []string{"Party", "Phone", "0-30", "31-60", "61-90", "90+", "Total", "Advance"}, csvRows)
	}
	return httpx.OK(w, map[string]any{"kind": kind, "as_of": asOfStr, "rows": out, "totals": total})
}

// CashFlowLine is money in/out of cash+bank grouped by voucher type.
type CashFlowLine struct {
	VoucherType string `json:"voucher_type"`
	InPaise     int64  `json:"in_paise"`
	OutPaise    int64  `json:"out_paise"`
}

func (s *Service) handleCashFlow(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	var opening int64
	if err := s.DB.QueryRow(r.Context(), `SELECT COALESCE(sum(e.debit_paise - e.credit_paise),0)
		FROM voucher_entries e JOIN accounts a ON a.id=e.account_id
		WHERE e.tenant_id=$1 AND (a.is_cash OR a.is_bank) AND e.vdate < $2`, p.TenantID, from).Scan(&opening); err != nil {
		return err
	}
	// Contra vouchers move money between cash and bank; they net to zero and are excluded.
	rows, err := s.DB.Query(r.Context(), `SELECT v.vtype, COALESCE(sum(e.debit_paise),0), COALESCE(sum(e.credit_paise),0)
		FROM voucher_entries e JOIN accounts a ON a.id=e.account_id JOIN vouchers v ON v.id=e.voucher_id
		WHERE e.tenant_id=$1 AND (a.is_cash OR a.is_bank) AND e.vdate BETWEEN $2 AND $3 AND v.vtype <> 'contra'
		GROUP BY v.vtype ORDER BY v.vtype`, p.TenantID, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	lines := []CashFlowLine{}
	var in, out int64
	for rows.Next() {
		var l CashFlowLine
		if err := rows.Scan(&l.VoucherType, &l.InPaise, &l.OutPaise); err != nil {
			return err
		}
		in += l.InPaise
		out += l.OutPaise
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	res := map[string]any{"from": from, "to": to, "opening_paise": opening, "lines": lines,
		"total_in_paise": in, "total_out_paise": out, "net_paise": in - out, "closing_paise": opening + in - out}
	if wantsCSV(r) {
		var csvRows [][]string
		for _, l := range lines {
			csvRows = append(csvRows, []string{l.VoucherType, rupees(l.InPaise), rupees(l.OutPaise)})
		}
		csvRows = append(csvRows, []string{"Opening", rupees(opening), ""}, []string{"Closing", rupees(opening + in - out), ""})
		return writeCSV(w, "cash-flow-"+from+"-"+to, []string{"Voucher type", "In", "Out"}, csvRows)
	}
	return httpx.OK(w, res)
}

// Summary is GET /internal/v1/reports/summary (ARCHITECTURE §6).
type Summary struct {
	SalesPaise       int64 `json:"sales_paise"`
	PurchasesPaise   int64 `json:"purchases_paise"`
	ReceivablesPaise int64 `json:"receivables_paise"`
	PayablesPaise    int64 `json:"payables_paise"`
	CashPaise        int64 `json:"cash_paise"`
	BankPaise        int64 `json:"bank_paise"`
}

func (s *Service) summary(ctx context.Context, tenantID, from, to string) (*Summary, error) {
	period, err := s.totals(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	sum := &Summary{}
	for _, t := range period {
		switch t.Code {
		case posting.Sales:
			sum.SalesPaise += t.Credit - t.Debit
		case posting.SalesReturns:
			sum.SalesPaise -= t.Debit - t.Credit
		case posting.Purchases:
			sum.PurchasesPaise += t.Debit - t.Credit
		case posting.PurchaseReturns:
			sum.PurchasesPaise -= t.Credit - t.Debit
		}
	}
	// Balances are as of `to`, across all time. Debtors/creditors include party sub-accounts.
	err = s.DB.QueryRow(ctx, `WITH RECURSIVE tree AS (
			SELECT id, code AS root FROM accounts WHERE tenant_id=$1 AND code IN ($3, $4)
			UNION ALL
			SELECT a.id, t.root FROM accounts a JOIN tree t ON a.parent_id=t.id WHERE a.tenant_id=$1)
		SELECT
			COALESCE(sum(e.debit_paise - e.credit_paise) FILTER (WHERE t.root=$3),0),
			COALESCE(sum(e.credit_paise - e.debit_paise) FILTER (WHERE t.root=$4),0)
		FROM tree t JOIN voucher_entries e ON e.account_id=t.id
		WHERE e.tenant_id=$1 AND e.vdate <= $2`, tenantID, to, posting.SundryDebtors, posting.SundryCreditors,
	).Scan(&sum.ReceivablesPaise, &sum.PayablesPaise)
	if err != nil {
		return nil, err
	}
	err = s.DB.QueryRow(ctx, `SELECT
			COALESCE(sum(e.debit_paise - e.credit_paise) FILTER (WHERE a.is_cash),0),
			COALESCE(sum(e.debit_paise - e.credit_paise) FILTER (WHERE a.is_bank),0)
		FROM voucher_entries e JOIN accounts a ON a.id=e.account_id
		WHERE e.tenant_id=$1 AND e.vdate <= $2`, tenantID, to).Scan(&sum.CashPaise, &sum.BankPaise)
	return sum, err
}

func (s *Service) handleSummary(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.BadRequest("X-Tenant-ID header is required")
	}
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	sum, err := s.summary(r.Context(), p.TenantID, from, to)
	if err != nil {
		return err
	}
	return httpx.OK(w, sum)
}
