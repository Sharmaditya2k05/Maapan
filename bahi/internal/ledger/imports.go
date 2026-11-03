package ledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// Import support for aayaat: bill-wise opening, invoice history, chart of
// accounts, and undoing what an import created. Every endpoint takes a batch,
// runs each record in its own transaction and answers a BulkResult with
// per-record results.

const (
	maxBulkRecords = 2000

	srcImport         = "import"          // invoices posted from invoice history (source_id = external_ref)
	srcImportRollback = "import.rollback" // reversal of an imported voucher
	createdViaImport  = "import"
)

func billSourceID(externalRef string) string { return "bill:" + externalRef }

func parseDate(name, v string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		return t, httpx.Validation("%s must be YYYY-MM-DD", name)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// POST /internal/v1/bulk/open-bills

type openBillInput struct {
	PartyExternalRef string `json:"party_external_ref"`
	PartyName        string `json:"party_name"` // informational
	BillNo           string `json:"bill_no"`
	BillDate         string `json:"bill_date"`
	DueDate          string `json:"due_date"`
	BillAmountPaise  int64  `json:"bill_amount_paise"` // original bill amount (optional)
	AmountPaise      int64  `json:"amount_paise"`      // still outstanding on as_of
	Type             string `json:"type"`              // dr = receivable, cr = payable
	ExternalRef      string `json:"external_ref"`
}

type bulkOpenBillsInput struct {
	AsOf  string          `json:"as_of"`
	Bills []openBillInput `json:"bills"`
}

// handleBulkOpenBills posts one opening voucher per bill (Dr/Cr party, Cr/Dr
// Opening Balance Difference, dated as_of — exactly like a party_totals
// opening line), raising one bill that keeps the original bill number, bill
// date and due date so outstanding and due-date aging match the old books bill
// by bill. Idempotent by external_ref.
func (s *Service) handleBulkOpenBills(w http.ResponseWriter, r *http.Request) error {
	var in bulkOpenBillsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if _, err := parseDate("as_of", in.AsOf); err != nil {
		return err
	}
	if len(in.Bills) > maxBulkRecords {
		return httpx.Validation("at most %d bills per call", maxBulkRecords)
	}
	res := newBulkResult()
	for i, b := range in.Bills {
		id, created, err := s.importOpenBill(r.Context(), p, in.AsOf, b)
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, id, created)
	}
	return httpx.OK(w, res)
}

func (s *Service) importOpenBill(ctx context.Context, p *auth.Principal, asOf string, b openBillInput) (string, bool, error) {
	b.BillNo, b.ExternalRef = strings.TrimSpace(b.BillNo), strings.TrimSpace(b.ExternalRef)
	b.Type = strings.ToLower(strings.TrimSpace(b.Type))
	switch {
	case b.ExternalRef == "":
		return "", false, httpx.Validation("external_ref is required")
	case b.BillNo == "":
		return "", false, httpx.Validation("bill_no is required")
	case b.PartyExternalRef == "":
		return "", false, httpx.Validation("party_external_ref is required")
	case b.Type != "dr" && b.Type != "cr":
		return "", false, httpx.Validation(`type must be "dr" or "cr"`)
	case b.AmountPaise <= 0:
		return "", false, httpx.Validation("amount_paise must be positive")
	case b.BillAmountPaise < 0 || (b.BillAmountPaise > 0 && b.BillAmountPaise < b.AmountPaise):
		return "", false, httpx.Validation("bill_amount_paise cannot be less than the outstanding amount_paise")
	}
	if b.BillDate == "" {
		b.BillDate = asOf
	}
	billDate, err := parseDate("bill_date", b.BillDate)
	if err != nil {
		return "", false, err
	}
	if b.DueDate != "" {
		due, err := parseDate("due_date", b.DueDate)
		if err != nil {
			return "", false, err
		}
		if due.Before(billDate) {
			return "", false, httpx.Validation("due_date %s is before bill_date %s", b.DueDate, b.BillDate)
		}
	}
	var posted Posted
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		posted = Posted{}
		var pid string
		var credit int
		var archived bool
		err := tx.QueryRow(ctx, `SELECT id, credit_days, archived FROM parties WHERE tenant_id=$1 AND external_ref=$2`,
			p.TenantID, b.PartyExternalRef).Scan(&pid, &credit, &archived)
		if db.IsNotFound(err) {
			return httpx.Validation("party_external_ref %q not found", b.PartyExternalRef)
		}
		if err != nil {
			return err
		}
		if archived {
			return httpx.Validation("party %q is archived", b.PartyExternalRef)
		}
		// Re-delivery: nothing to check or change.
		err = tx.QueryRow(ctx, `SELECT id FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_id=$3`,
			p.TenantID, posting.SrcOpening, billSourceID(b.ExternalRef)).Scan(&posted.ID)
		if err == nil {
			return nil
		}
		if !db.IsNotFound(err) {
			return err
		}
		// A party-level opening (party master or party_totals) already holds
		// this outstanding; bills on top would count it twice.
		var num string
		err = tx.QueryRow(ctx, `SELECT number FROM vouchers WHERE tenant_id=$1 AND source=$2 AND status='posted'
			AND (source_id=$3 OR source_id LIKE 'bulk:%:' || $3) LIMIT 1`, p.TenantID, posting.SrcOpening, "party:"+pid).Scan(&num)
		if err == nil {
			return httpx.Conflict("party already has an opening balance (%s); importing its bills as well would count the "+
				"outstanding twice. Roll that opening back first, or import party totals only", num)
		}
		if !db.IsNotFound(err) {
			return err
		}
		due := b.DueDate
		if due == "" {
			due = billDate.AddDate(0, 0, credit).Format(time.DateOnly)
		}
		v, err := posting.Opening(posting.Entry{PartyID: pid}, b.AmountPaise, b.Type, asOf, billSourceID(b.ExternalRef))
		if err != nil {
			return httpx.Validation("%s", err.Error())
		}
		v.SourceRef, v.DueDate = b.BillNo, due
		v.Narration = fmt.Sprintf("Opening bill %s dated %s", b.BillNo, b.BillDate)
		if posted, err = Post(ctx, tx, p.TenantID, p.UserID(), v); err != nil || !posted.Created {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE vouchers SET external_ref=$3 WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, posted.ID, b.ExternalRef); err != nil {
			return err
		}
		var orig *int64
		if b.BillAmountPaise > 0 {
			orig = &b.BillAmountPaise
		}
		// The voucher is dated as_of; the bill keeps its own date and due date
		// (an overdue bill stays overdue).
		_, err = tx.Exec(ctx, `UPDATE bills SET bill_date=$3, due_date=$4, original_amount_paise=$5
			WHERE tenant_id=$1 AND voucher_id=$2`, p.TenantID, posted.ID, b.BillDate, due, orig)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return posted.ID, posted.Created, nil
}

// ---------------------------------------------------------------------------
// POST /internal/v1/bulk/invoice-history

type historyInvoiceInput struct {
	DocType          string `json:"doc_type"` // sale | purchase | sale_return | purchase_return
	Number           string `json:"number"`
	Date             string `json:"date"`
	PartyExternalRef string `json:"party_external_ref"`
	PartyName        string `json:"party_name"`
	PartyGSTIN       string `json:"party_gstin"`
	PlaceOfSupply    string `json:"place_of_supply"`
	TaxablePaise     int64  `json:"taxable_paise"`
	CGSTPaise        int64  `json:"cgst_paise"`
	SGSTPaise        int64  `json:"sgst_paise"`
	IGSTPaise        int64  `json:"igst_paise"`
	CessPaise        int64  `json:"cess_paise"`
	RoundOffPaise    int64  `json:"round_off_paise"`
	TotalPaise       int64  `json:"total_paise"`
	DueDate          string `json:"due_date"`
	Reference        string `json:"reference"`
	Narration        string `json:"narration"`
	StoreID          string `json:"store_id"`
	ExternalRef      string `json:"external_ref"`
}

type bulkInvoiceHistoryInput struct {
	AsOf     string                `json:"as_of"`
	Invoices []historyInvoiceInput `json:"invoices"`
}

func (in *historyInvoiceInput) check() error {
	in.ExternalRef, in.Number = strings.TrimSpace(in.ExternalRef), strings.TrimSpace(in.Number)
	switch in.DocType {
	case "sale", "purchase", "sale_return", "purchase_return":
	default:
		return httpx.Validation("doc_type must be sale, purchase, sale_return or purchase_return")
	}
	if in.ExternalRef == "" {
		return httpx.Validation("external_ref is required")
	}
	if in.Number == "" {
		return httpx.Validation("number is required")
	}
	if _, err := parseDate("date", in.Date); err != nil {
		return err
	}
	if in.DueDate != "" {
		if _, err := parseDate("due_date", in.DueDate); err != nil {
			return err
		}
	}
	if in.TaxablePaise < 0 || in.CGSTPaise < 0 || in.SGSTPaise < 0 || in.IGSTPaise < 0 || in.CessPaise < 0 {
		return httpx.Validation("taxable and tax amounts cannot be negative (send returns as sale_return / purchase_return)")
	}
	if in.TotalPaise <= 0 {
		return httpx.Validation("total_paise must be positive")
	}
	if sum := in.TaxablePaise + in.CGSTPaise + in.SGSTPaise + in.IGSTPaise + in.CessPaise + in.RoundOffPaise; sum != in.TotalPaise {
		return httpx.Validation("taxable + taxes + round off = %d paise, but total_paise is %d", sum, in.TotalPaise)
	}
	if in.StoreID != "" && !validUUID(in.StoreID) {
		return httpx.Validation("store_id must be a UUID")
	}
	return nil
}

// handleBulkInvoiceHistory imports past invoices. Dated before as_of they are
// stored as history only (the opening balances already hold their effect);
// on/after as_of they are posted as sales / purchase / credit note / debit
// note vouchers with source "import" and source_id = external_ref (period lock
// respected). Nothing here ever moves stock: bahi publishes no stock events.
func (s *Service) handleBulkInvoiceHistory(w http.ResponseWriter, r *http.Request) error {
	var in bulkInvoiceHistoryInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if _, err := parseDate("as_of", in.AsOf); err != nil {
		return err
	}
	if len(in.Invoices) > maxBulkRecords {
		return httpx.Validation("at most %d invoices per call", maxBulkRecords)
	}
	res := newBulkResult()
	for i := range in.Invoices {
		inv := in.Invoices[i]
		if err := inv.check(); err != nil {
			res.fail(i, err)
			continue
		}
		id, created, err := s.importInvoice(r.Context(), p, in.AsOf, inv)
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, id, created)
	}
	return httpx.OK(w, res)
}

func (s *Service) importInvoice(ctx context.Context, p *auth.Principal, asOf string, in historyInvoiceInput) (string, bool, error) {
	var id string
	created := false
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		created = false
		if err := setActor(ctx, tx, p.UserID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || ':invoice-history:' || $2))`, p.TenantID, in.ExternalRef); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id FROM invoice_history WHERE tenant_id=$1 AND external_ref=$2`, p.TenantID, in.ExternalRef).Scan(&id)
		if err == nil {
			return nil // already imported
		}
		if !db.IsNotFound(err) {
			return err
		}
		var partyID *string
		if in.PartyExternalRef != "" {
			var pid string
			err := tx.QueryRow(ctx, `SELECT id FROM parties WHERE tenant_id=$1 AND external_ref=$2`, p.TenantID, in.PartyExternalRef).Scan(&pid)
			if db.IsNotFound(err) {
				return httpx.Validation("party_external_ref %q not found", in.PartyExternalRef)
			}
			if err != nil {
				return err
			}
			partyID = &pid
		}
		status := "history"
		var voucherID *string
		if in.Date >= asOf {
			status = "posted"
			pl := events.InvoicePayload{InvoiceID: in.ExternalRef, StoreID: in.StoreID, DocType: in.DocType, Number: in.Number,
				InvoiceDate: in.Date, DueDate: in.DueDate, PartyName: in.PartyName, PartyGSTIN: in.PartyGSTIN,
				PlaceOfSupply: in.PlaceOfSupply, TaxablePaise: in.TaxablePaise, CGSTPaise: in.CGSTPaise, SGSTPaise: in.SGSTPaise,
				IGSTPaise: in.IGSTPaise, CessPaise: in.CessPaise, RoundOffPaise: in.RoundOffPaise, GrandTotalPaise: in.TotalPaise}
			if partyID != nil {
				pl.PartyID = *partyID
			} else {
				// A cash bill: settled in cash at the counter, not left on Walk-in.
				pl.Tenders = []events.Tender{{Mode: "cash", AmountPaise: in.TotalPaise}}
			}
			v, err := posting.Invoice(pl)
			if err != nil {
				return httpx.Validation("%s", err.Error())
			}
			v.Source, v.SourceID = srcImport, in.ExternalRef
			v.Narration += " (imported)"
			if in.Narration != "" {
				v.Narration += " — " + in.Narration
			}
			posted, err := Post(ctx, tx, p.TenantID, p.UserID(), v)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE vouchers SET external_ref=$3 WHERE tenant_id=$1 AND id=$2`,
				p.TenantID, posted.ID, in.ExternalRef); err != nil {
				return err
			}
			voucherID = &posted.ID
		}
		created = true
		return tx.QueryRow(ctx, `INSERT INTO invoice_history (tenant_id, external_ref, as_of, status, voucher_id, doc_type, number,
			inv_date, due_date, party_id, party_name, party_gstin, place_of_supply, taxable_paise, cgst_paise, sgst_paise, igst_paise,
			cess_paise, round_off_paise, total_paise, reference, narration, store_id, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24) RETURNING id`,
			p.TenantID, in.ExternalRef, asOf, status, voucherID, in.DocType, in.Number, in.Date, nilIfEmpty(in.DueDate), partyID,
			nilIfEmpty(in.PartyName), nilIfEmpty(in.PartyGSTIN), nilIfEmpty(in.PlaceOfSupply), in.TaxablePaise, in.CGSTPaise,
			in.SGSTPaise, in.IGSTPaise, in.CessPaise, in.RoundOffPaise, in.TotalPaise, nilIfEmpty(in.Reference),
			nilIfEmpty(in.Narration), nilIfEmpty(in.StoreID), p.UserID()).Scan(&id)
	})
	return id, created, err
}

// HistoryInvoice is an imported past invoice.
type HistoryInvoice struct {
	ID            string `json:"id"`
	ExternalRef   string `json:"external_ref"`
	Status        string `json:"status"` // history (not posted) | posted
	VoucherID     string `json:"voucher_id,omitempty"`
	DocType       string `json:"doc_type"`
	Number        string `json:"number"`
	Date          string `json:"date"`
	DueDate       string `json:"due_date,omitempty"`
	PartyID       string `json:"party_id,omitempty"`
	PartyName     string `json:"party_name,omitempty"`
	PartyGSTIN    string `json:"party_gstin,omitempty"`
	PlaceOfSupply string `json:"place_of_supply,omitempty"`
	TaxablePaise  int64  `json:"taxable_paise"`
	CGSTPaise     int64  `json:"cgst_paise"`
	SGSTPaise     int64  `json:"sgst_paise"`
	IGSTPaise     int64  `json:"igst_paise"`
	CessPaise     int64  `json:"cess_paise"`
	RoundOffPaise int64  `json:"round_off_paise"`
	TotalPaise    int64  `json:"total_paise"`
	Reference     string `json:"reference,omitempty"`
	Narration     string `json:"narration,omitempty"`
}

// listHistory returns imported invoices matching the filters.
func (s *Service) listHistory(ctx context.Context, tenantID, partyID, docType, status, from, to string) ([]HistoryInvoice, error) {
	rows, err := s.DB.Query(ctx, `SELECT h.id, h.external_ref, h.status, COALESCE(h.voucher_id::text,''), h.doc_type, h.number, h.inv_date,
			h.due_date, COALESCE(h.party_id::text,''), COALESCE(p.name, h.party_name, ''), COALESCE(h.party_gstin,''),
			COALESCE(h.place_of_supply,''), h.taxable_paise, h.cgst_paise, h.sgst_paise, h.igst_paise, h.cess_paise,
			h.round_off_paise, h.total_paise, COALESCE(h.reference,''), COALESCE(h.narration,'')
		FROM invoice_history h LEFT JOIN parties p ON p.id=h.party_id
		WHERE h.tenant_id=$1 AND ($2='' OR h.party_id::text=$2) AND ($3='' OR h.doc_type=$3) AND ($4='' OR h.status=$4)
		  AND h.inv_date BETWEEN $5 AND $6
		ORDER BY h.inv_date, h.number`, tenantID, partyID, docType, status, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryInvoice{}
	for rows.Next() {
		var h HistoryInvoice
		var d time.Time
		var due *time.Time
		if err := rows.Scan(&h.ID, &h.ExternalRef, &h.Status, &h.VoucherID, &h.DocType, &h.Number, &d, &due, &h.PartyID,
			&h.PartyName, &h.PartyGSTIN, &h.PlaceOfSupply, &h.TaxablePaise, &h.CGSTPaise, &h.SGSTPaise, &h.IGSTPaise,
			&h.CessPaise, &h.RoundOffPaise, &h.TotalPaise, &h.Reference, &h.Narration); err != nil {
			return nil, err
		}
		h.Date = d.Format(time.DateOnly)
		if due != nil {
			h.DueDate = due.Format(time.DateOnly)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// handleInvoiceHistory is GET /v1/invoice-history?party_id=&doc_type=&status=&from=&to=[&format=csv].
// from defaults to 2000-01-01 (history usually predates the current FY).
func (s *Service) handleInvoiceHistory(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to := httpx.Query(r, "from"), httpx.Query(r, "to")
	if from == "" {
		from = "2000-01-01"
	}
	if to == "" {
		to = s.today()
	}
	if _, err := parseDate("from", from); err != nil {
		return err
	}
	if _, err := parseDate("to", to); err != nil {
		return err
	}
	partyID := httpx.Query(r, "party_id")
	if partyID != "" && !validUUID(partyID) {
		return httpx.Validation("party_id must be a UUID")
	}
	list, err := s.listHistory(r.Context(), p.TenantID, partyID, httpx.Query(r, "doc_type"), httpx.Query(r, "status"), from, to)
	if err != nil {
		return err
	}
	if !wantsCSV(r) {
		return httpx.OK(w, map[string]any{"from": from, "to": to, "items": list})
	}
	rows := make([][]string, 0, len(list))
	for _, h := range list {
		rows = append(rows, []string{h.Date, h.DocType, h.Number, h.PartyName, h.PartyGSTIN, h.PlaceOfSupply, rupees(h.TaxablePaise),
			rupees(h.CGSTPaise), rupees(h.SGSTPaise), rupees(h.IGSTPaise), rupees(h.CessPaise), rupees(h.RoundOffPaise),
			rupees(h.TotalPaise), h.Status, h.Reference})
	}
	return writeCSV(w, "invoice-history", []string{"date", "doc_type", "number", "party", "gstin", "place_of_supply", "taxable",
		"cgst", "sgst", "igst", "cess", "round_off", "total", "status", "reference"}, rows)
}

// ---------------------------------------------------------------------------
// POST /internal/v1/bulk/accounts

type bulkAccountInput struct {
	Code        string `json:"code"`        // optional; generated under the parent when empty
	Name        string `json:"name"`        // required
	ParentCode  string `json:"parent_code"` // bahi group code, or …
	Group       string `json:"group"`       // … a group by name: a bahi group or a Tally group ("Duties & Taxes", "Indirect Expenses")
	IsGroup     bool   `json:"is_group"`
	IsBank      bool   `json:"is_bank"`
	IsCash      bool   `json:"is_cash"`
	ExternalRef string `json:"external_ref"`
}

type bulkAccountsInput struct {
	Accounts []bulkAccountInput `json:"accounts"`
}

// tallyGroups maps Tally's predefined groups (lower case) to bahi groups.
var tallyGroups = map[string]struct {
	code       string
	bank, cash bool
}{
	"capital account":          {code: posting.GroupEquity},
	"reserves & surplus":       {code: posting.GroupEquity},
	"current assets":           {code: posting.GroupAssets},
	"loans & advances (asset)": {code: posting.GroupAssets},
	"deposits (asset)":         {code: posting.GroupAssets},
	"investments":              {code: posting.GroupAssets},
	"misc. expenses (asset)":   {code: posting.GroupAssets},
	"bank accounts":            {code: posting.GroupAssets, bank: true},
	"cash-in-hand":             {code: posting.GroupAssets, cash: true},
	"cash in hand":             {code: posting.GroupAssets, cash: true},
	"stock-in-hand":            {code: posting.StockInHand},
	"sundry debtors":           {code: posting.SundryDebtors},
	"sundry creditors":         {code: posting.SundryCreditors},
	"fixed assets":             {code: posting.FixedAssets},
	"current liabilities":      {code: posting.GroupLiabilities},
	"provisions":               {code: posting.GroupLiabilities},
	"suspense a/c":             {code: posting.GroupLiabilities},
	"branch / divisions":       {code: posting.GroupLiabilities},
	"loans (liability)":        {code: posting.Loans},
	"secured loans":            {code: posting.Loans},
	"unsecured loans":          {code: posting.Loans},
	"bank od a/c":              {code: posting.Loans, bank: true},
	"bank occ a/c":             {code: posting.Loans, bank: true},
	"duties & taxes":           {code: posting.OutputTaxes},
	"sales accounts":           {code: posting.GroupIncome},
	"direct incomes":           {code: posting.GroupIncome},
	"indirect incomes":         {code: posting.GroupIncome},
	"income (direct)":          {code: posting.GroupIncome},
	"income (indirect)":        {code: posting.GroupIncome},
	"purchase accounts":        {code: posting.GroupExpenses},
	"direct expenses":          {code: posting.DirectExpenses},
	"expenses (direct)":        {code: posting.DirectExpenses},
	"indirect expenses":        {code: posting.IndirectExpenses},
	"expenses (indirect)":      {code: posting.IndirectExpenses},
}

func normName(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// handleBulkAccounts creates ledger accounts (and groups) under the chart —
// e.g. Tally's "Other Ledgers" — idempotent by external_ref, then code, then
// name. An account that exists is reported as updated (renamed when matched by
// code or external_ref and not a system account); its group never changes.
func (s *Service) handleBulkAccounts(w http.ResponseWriter, r *http.Request) error {
	var in bulkAccountsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if len(in.Accounts) > maxBulkRecords {
		return httpx.Validation("at most %d accounts per call", maxBulkRecords)
	}
	if err := s.WithTx(r.Context(), func(tx pgx.Tx) error { return EnsureChart(r.Context(), tx, p.TenantID) }); err != nil {
		return err
	}
	res := newBulkResult()
	for i, a := range in.Accounts {
		a.Code, a.Name, a.ExternalRef = strings.TrimSpace(a.Code), strings.TrimSpace(a.Name), strings.TrimSpace(a.ExternalRef)
		if a.Name == "" {
			res.failf(i, "name is required")
			continue
		}
		if a.IsBank && a.IsCash {
			res.failf(i, "an account cannot be both bank and cash")
			continue
		}
		var id string
		var created bool
		err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
			var err error
			id, created, err = upsertImportedAccount(r.Context(), tx, p.TenantID, p.UserID(), a)
			return err
		})
		if db.IsUniqueViolation(err) {
			err = httpx.Conflict("account code %s already exists", a.Code)
		}
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, id, created)
	}
	return httpx.OK(w, res)
}

func upsertImportedAccount(ctx context.Context, tx pgx.Tx, tenantID, userID string, a bulkAccountInput) (string, bool, error) {
	if err := setActor(ctx, tx, userID); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || ':account:' || $2))`, tenantID, normName(a.Name)); err != nil {
		return "", false, err
	}
	// Existing account: by external_ref, code, then name (party ledgers excluded).
	var id string
	var isSystem, isGroup bool
	find := func(cond string, arg string) error {
		return tx.QueryRow(ctx, `SELECT a.id, a.is_system, a.is_group FROM accounts a WHERE a.tenant_id=$1 AND `+cond+`
			AND NOT EXISTS (SELECT 1 FROM parties p WHERE p.account_id=a.id) LIMIT 1`, tenantID, arg).Scan(&id, &isSystem, &isGroup)
	}
	tries := []struct{ cond, arg string }{}
	if a.ExternalRef != "" {
		tries = append(tries, struct{ cond, arg string }{"a.external_ref=$2", a.ExternalRef})
	}
	if a.Code != "" {
		tries = append(tries, struct{ cond, arg string }{"a.code=$2", a.Code})
	}
	tries = append(tries, struct{ cond, arg string }{"lower(regexp_replace(trim(a.name), '\\s+', ' ', 'g'))=$2", normName(a.Name)})
	for k, t := range tries {
		err := find(t.cond, t.arg)
		if db.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if isGroup != a.IsGroup {
			kind := "a ledger"
			if isGroup {
				kind = "a group"
			}
			return "", false, httpx.Conflict("account %q already exists as %s", a.Name, kind)
		}
		byKey := k < len(tries)-1
		if byKey && !isSystem {
			if _, err := tx.Exec(ctx, `UPDATE accounts SET name=$3 WHERE tenant_id=$1 AND id=$2 AND name<>$3`, tenantID, id, a.Name); err != nil {
				return "", false, err
			}
		}
		if a.ExternalRef != "" {
			if _, err := tx.Exec(ctx, `UPDATE accounts SET external_ref=$3 WHERE tenant_id=$1 AND id=$2 AND external_ref IS NULL
				AND NOT EXISTS (SELECT 1 FROM accounts x WHERE x.tenant_id=$1 AND x.external_ref=$3)`, tenantID, id, a.ExternalRef); err != nil {
				return "", false, err
			}
		}
		return id, false, nil
	}

	// Parent group.
	var parentID, parentCode, grp string
	var parentIsGroup bool
	bank, cash := a.IsBank, a.IsCash
	lookup := func(cond, arg string) error {
		return tx.QueryRow(ctx, `SELECT id, code, grp, is_group FROM accounts WHERE tenant_id=$1 AND `+cond+` ORDER BY is_group DESC, code LIMIT 1`,
			tenantID, arg).Scan(&parentID, &parentCode, &grp, &parentIsGroup)
	}
	switch {
	case a.ParentCode != "":
		err := lookup("code=$2", a.ParentCode)
		if db.IsNotFound(err) {
			return "", false, httpx.Validation("parent_code %s not found", a.ParentCode)
		}
		if err != nil {
			return "", false, err
		}
	case a.Group != "":
		err := lookup("is_group AND lower(regexp_replace(trim(name), '\\s+', ' ', 'g'))=$2", normName(a.Group))
		if db.IsNotFound(err) {
			tg, ok := tallyGroups[normName(a.Group)]
			if !ok {
				return "", false, httpx.Validation("group %q is neither a bahi group nor a Tally group; send parent_code", a.Group)
			}
			bank, cash = bank || tg.bank, cash || tg.cash
			err = lookup("code=$2", tg.code)
		}
		if err != nil {
			return "", false, err
		}
	default:
		return "", false, httpx.Validation("parent_code or group is required")
	}
	if !parentIsGroup {
		return "", false, httpx.Validation("parent %s is not a group", parentCode)
	}
	if a.IsGroup {
		bank, cash = false, false
	}
	code := a.Code
	if code == "" {
		var err error
		if code, err = nextAccountCode(ctx, tx, tenantID, parentID, parentCode); err != nil {
			return "", false, err
		}
	}
	err := tx.QueryRow(ctx, `INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_cash, is_bank, external_ref, created_via)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		tenantID, code, a.Name, grp, parentID, a.IsGroup, cash, bank, nilIfEmpty(a.ExternalRef), createdViaImport).Scan(&id)
	return id, true, err
}

// nextAccountCode picks the next free numeric code after the parent's highest
// numeric child (or after the parent's own code).
func nextAccountCode(ctx context.Context, tx pgx.Tx, tenantID, parentID, parentCode string) (string, error) {
	var top *int64
	if err := tx.QueryRow(ctx, `SELECT max(code::bigint) FROM accounts WHERE tenant_id=$1 AND parent_id=$2 AND code ~ '^[0-9]{1,12}$'`,
		tenantID, parentID).Scan(&top); err != nil {
		return "", err
	}
	base := int64(-1)
	if top != nil {
		base = *top + 1
	} else if n, err := strconv.ParseInt(parentCode, 10, 64); err == nil {
		base = n + 1
	}
	if base >= 0 {
		for k := int64(0); k < 100; k++ {
			code := strconv.FormatInt(base+k, 10)
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE tenant_id=$1 AND code=$2)`, tenantID, code).Scan(&taken); err != nil {
				return "", err
			}
			if !taken {
				return code, nil
			}
		}
	}
	return "L-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:12]), nil
}

// ---------------------------------------------------------------------------
// POST /internal/v1/bulk/rollback

type rollbackRecordInput struct {
	ExternalRef      string  `json:"external_ref"`
	AccountCode      string  `json:"account_code"`
	PartyExternalRef string  `json:"party_external_ref"`
	ItemExternalRef  string  `json:"item_external_ref"` // bhandar kinds; not used by bahi
	ItemID           string  `json:"item_id"`
	BatchNo          string  `json:"batch_no"`
	StoreID          string  `json:"store_id"`
	Qty              float64 `json:"qty"`
	Created          *bool   `json:"created"` // from the bulk result; nil = unknown
}

type rollbackInput struct {
	Kind    string                `json:"kind"` // parties | opening_balances | open_bills | invoices | accounts
	JobID   string                `json:"job_id"`
	AsOf    string                `json:"as_of"`
	Records []rollbackRecordInput `json:"records"`
}

// handleBulkRollback undoes what an import created, record by record:
//
//	parties          archive the party (and its ledger) and reverse its opening
//	                 balance — only parties the import created, and only while
//	                 nothing else has been posted, issued or imported for them
//	opening_balances reverse the opening line (key as_of + account / party)
//	open_bills       reverse the opening bill (key external_ref)
//	invoices         drop the history row; reverse the voucher of a posted one
//	accounts         delete an imported account nothing has been posted to
//
// A record whose bills have been settled since, whose voucher falls in the
// locked period, or whose party has other activity is refused with a reason
// in errors[] and left untouched. Reversed vouchers keep the audit trail; the
// key is retired so the same record can be imported again. Records already
// undone (or never there) succeed. A record that existed before the import
// (created=false) is left as it is — updates are never reverted. Success
// counts as "updated" (nothing is created).
func (s *Service) handleBulkRollback(w http.ResponseWriter, r *http.Request) error {
	var in rollbackInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if len(in.Records) > maxBulkRecords {
		return httpx.Validation("at most %d records per call", maxBulkRecords)
	}
	var undo func(ctx context.Context, tx pgx.Tx, tenantID, userID string, rec rollbackRecordInput) (string, error)
	switch in.Kind {
	case "parties":
		undo = undoParty
	case "opening_balances":
		if _, err := parseDate("as_of", in.AsOf); err != nil {
			return err
		}
		asOf := in.AsOf
		undo = func(ctx context.Context, tx pgx.Tx, tenantID, userID string, rec rollbackRecordInput) (string, error) {
			return undoOpeningLine(ctx, tx, tenantID, userID, asOf, rec)
		}
	case "open_bills":
		undo = undoOpenBill
	case "invoices":
		undo = undoInvoice
	case "accounts":
		undo = undoAccount
	default:
		return httpx.Validation("kind %q is not imported into bahi (parties, opening_balances, open_bills, invoices, accounts)", in.Kind)
	}
	res := newBulkResult()
	for i, rec := range in.Records {
		var id string
		err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
			if err := setActor(r.Context(), tx, p.UserID()); err != nil {
				return err
			}
			var err error
			id, err = undo(r.Context(), tx, p.TenantID, p.UserID(), rec)
			return err
		})
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, id, false)
	}
	return httpx.OK(w, res)
}

// undoImportedVoucher reverses the voucher (source, sourceID) on its own date
// and retires its key. Missing → "" (nothing to undo). Refused when a bill it
// raised has settlements, when it settles bills itself, when TDS was computed
// on it, or when its date is in the locked period.
func undoImportedVoucher(ctx context.Context, tx pgx.Tx, tenantID, userID, source, sourceID, what string) (string, error) {
	var id, number, status string
	var vdate time.Time
	err := tx.QueryRow(ctx, `SELECT id, number, status, vdate FROM vouchers WHERE tenant_id=$1 AND source=$2 AND source_id=$3 FOR UPDATE`,
		tenantID, source, sourceID).Scan(&id, &number, &status, &vdate)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if status == "posted" {
		var n int
		var by string
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(min(sv.number),'') FROM bill_allocations a
			JOIN bills b ON b.id=a.bill_id JOIN vouchers sv ON sv.id=a.voucher_id
			WHERE b.tenant_id=$1 AND b.voucher_id=$2`, tenantID, id).Scan(&n, &by); err != nil {
			return "", err
		}
		if n > 0 {
			return "", httpx.Conflict("%s %s has been settled since the import (%d allocation(s), e.g. %s); remove those allocations first",
				what, number, n, by)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM bill_allocations WHERE tenant_id=$1 AND voucher_id=$2`, tenantID, id).Scan(&n); err != nil {
			return "", err
		}
		if n > 0 {
			return "", httpx.Conflict("%s %s has been allocated against %d bill(s) since the import; remove those allocations first", what, number, n)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tds_entries WHERE tenant_id=$1 AND (ref_voucher_id=$2 OR voucher_id=$2)
			AND status='active'`, tenantID, id).Scan(&n); err != nil {
			return "", err
		}
		if n > 0 {
			return "", httpx.Conflict("%s %s has TDS/TCS entries since the import; reverse them first", what, number)
		}
		if _, err := Reverse(ctx, tx, tenantID, userID, id, vdate.Format(time.DateOnly), srcImportRollback, id, "import rolled back"); err != nil {
			return "", err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE vouchers SET source_id = source_id || '#rolled-back:' || id::text WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	return id, err
}

func undoParty(ctx context.Context, tx pgx.Tx, tenantID, userID string, rec rollbackRecordInput) (string, error) {
	if rec.ExternalRef == "" {
		return "", httpx.Validation("external_ref is required")
	}
	var id, acct, via string
	err := tx.QueryRow(ctx, `SELECT id, account_id, COALESCE(created_via,'') FROM parties WHERE tenant_id=$1 AND external_ref=$2 FOR UPDATE`,
		tenantID, rec.ExternalRef).Scan(&id, &acct, &via)
	if db.IsNotFound(err) {
		return "", nil // already rolled back
	}
	if err != nil {
		return "", err
	}
	if (rec.Created != nil && !*rec.Created) || (rec.Created == nil && via != createdViaImport) {
		return id, nil // existed before the import: its update is kept
	}
	var n int
	var num string
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(min(v.number),'') FROM vouchers v
		WHERE v.tenant_id=$1 AND v.status='posted' AND v.reversal_of IS NULL
		  AND EXISTS (SELECT 1 FROM voucher_entries e WHERE e.voucher_id=v.id AND e.account_id=$2)
		  AND NOT (v.source=$3 AND v.source_id=$4)`, tenantID, acct, posting.SrcOpening, "party:"+id).Scan(&n, &num); err != nil {
		return "", err
	}
	if n > 0 {
		return "", httpx.Conflict("party has %d voucher(s) besides its opening balance (e.g. %s); roll back or reverse those first", n, num)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_history WHERE tenant_id=$1 AND party_id=$2`, tenantID, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", httpx.Conflict("party has %d imported invoice(s); roll back the invoice import first", n)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM cheques WHERE tenant_id=$1 AND party_id=$2 AND status NOT IN ('cancelled','bounced')`,
		tenantID, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", httpx.Conflict("party has %d cheque(s) in the register", n)
	}
	if _, err := undoImportedVoucher(ctx, tx, tenantID, userID, posting.SrcOpening, "party:"+id, "opening balance"); err != nil {
		return "", err
	}
	// external_ref is released so the same record can be imported again (the
	// audit trail keeps it).
	if _, err := tx.Exec(ctx, `UPDATE parties SET archived=true, external_ref=NULL, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE accounts SET archived=true WHERE tenant_id=$1 AND id=$2`, tenantID, acct)
	return id, err
}

func undoOpeningLine(ctx context.Context, tx pgx.Tx, tenantID, userID, asOf string, rec rollbackRecordInput) (string, error) {
	var key string
	switch {
	case rec.AccountCode != "":
		key = "acct:" + rec.AccountCode
	case rec.PartyExternalRef != "":
		var pid string
		err := tx.QueryRow(ctx, `SELECT id FROM parties WHERE tenant_id=$1 AND external_ref=$2`, tenantID, rec.PartyExternalRef).Scan(&pid)
		if db.IsNotFound(err) {
			return "", nil // the party is gone, and with it its opening line
		}
		if err != nil {
			return "", err
		}
		key = "party:" + pid
	default:
		return "", httpx.Validation("account_code or party_external_ref is required")
	}
	return undoImportedVoucher(ctx, tx, tenantID, userID, posting.SrcOpening, openingKey(asOf, key), "opening balance")
}

func undoOpenBill(ctx context.Context, tx pgx.Tx, tenantID, userID string, rec rollbackRecordInput) (string, error) {
	if rec.ExternalRef == "" {
		return "", httpx.Validation("external_ref is required")
	}
	return undoImportedVoucher(ctx, tx, tenantID, userID, posting.SrcOpening, billSourceID(rec.ExternalRef), "opening bill")
}

func undoInvoice(ctx context.Context, tx pgx.Tx, tenantID, userID string, rec rollbackRecordInput) (string, error) {
	if rec.ExternalRef == "" {
		return "", httpx.Validation("external_ref is required")
	}
	var id, status string
	err := tx.QueryRow(ctx, `SELECT id, status FROM invoice_history WHERE tenant_id=$1 AND external_ref=$2 FOR UPDATE`,
		tenantID, rec.ExternalRef).Scan(&id, &status)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if status == "posted" {
		if _, err := undoImportedVoucher(ctx, tx, tenantID, userID, srcImport, rec.ExternalRef, "invoice"); err != nil {
			return "", err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM invoice_history WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	return id, err
}

func undoAccount(ctx context.Context, tx pgx.Tx, tenantID, _ string, rec rollbackRecordInput) (string, error) {
	var id, via, code string
	err := tx.QueryRow(ctx, `SELECT id, COALESCE(created_via,''), code FROM accounts WHERE tenant_id=$1
		AND (($2 <> '' AND external_ref=$2) OR ($2 = '' AND code=$3)) FOR UPDATE`,
		tenantID, rec.ExternalRef, rec.AccountCode).Scan(&id, &via, &code)
	if db.IsNotFound(err) {
		if rec.ExternalRef == "" && rec.AccountCode == "" {
			return "", httpx.Validation("external_ref or account_code is required")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if via != createdViaImport || (rec.Created != nil && !*rec.Created) {
		return id, nil // not created by an import
	}
	var entries, children int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM voucher_entries WHERE account_id=$1),
		(SELECT count(*) FROM accounts WHERE parent_id=$1)`, id).Scan(&entries, &children); err != nil {
		return "", err
	}
	if entries > 0 {
		return "", httpx.Conflict("account %s has %d posting(s) since the import", code, entries)
	}
	if children > 0 {
		return "", httpx.Conflict("account %s has %d sub-account(s)", code, children)
	}
	// Something else (a cheque, a bank statement, a challan) may still point at it.
	if _, err := tx.Exec(ctx, `DELETE FROM accounts WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23503" {
			return "", httpx.Conflict("account %s is referenced by %s; archive it instead", code, pe.TableName)
		}
		return "", err
	}
	return id, nil
}
