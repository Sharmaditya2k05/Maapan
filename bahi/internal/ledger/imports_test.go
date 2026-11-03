package ledger

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
)

// importer drives the internal bulk endpoints the way aayaat does.
type importer struct {
	t      *testing.T
	a      *api
	tenant string
}

func newImporter(t *testing.T, s *Service) *importer {
	tenant, store := uuid.NewString(), uuid.NewString()
	seedTenant(t, s, tenant, store)
	return &importer{t: t, a: newAPI(t, s, tenant, store), tenant: tenant}
}

// post calls an internal bulk endpoint and returns the BulkResult body.
func (im *importer) post(path string, body any) map[string]any {
	im.t.Helper()
	st, out := im.a.do("POST", "/internal/v1/bulk/"+path, body,
		map[string]string{auth.HeaderServiceSecret: testServiceSecret, auth.HeaderTenantID: im.tenant})
	if st != 200 {
		im.t.Fatalf("bulk/%s: %d %v", path, st, out)
	}
	return out
}

func errorsOf(body map[string]any) map[int]string {
	out := map[int]string{}
	for _, e := range items(body, "errors") {
		out[int(num(e["index"]))] = e["message"].(string)
	}
	return out
}

func resultsOf(body map[string]any) map[int]map[string]any {
	out := map[int]map[string]any{}
	for _, r := range items(body, "results") {
		out[int(num(r["index"]))] = r
	}
	return out
}

func wantCounts(t *testing.T, body map[string]any, created, updated, errs int) {
	t.Helper()
	if num(body["created"]) != int64(created) || num(body["updated"]) != int64(updated) || len(items(body, "errors")) != errs {
		t.Fatalf("want created=%d updated=%d errors=%d, got %v", created, updated, errs, body)
	}
	if len(items(body, "results")) != created+updated {
		t.Fatalf("want %d results, got %v", created+updated, body["results"])
	}
}

func partyByRef(t *testing.T, s *Service, tenant, ref string) string {
	t.Helper()
	var id string
	mustOK(t, s.DB.QueryRow(context.Background(), `SELECT id FROM parties WHERE tenant_id=$1 AND external_ref=$2`, tenant, ref).Scan(&id))
	return id
}

var importParties = map[string]any{"parties": []map[string]any{
	{"name": "Sharma Agencies", "type": "customer", "credit_limit_paise": 0, "credit_days": 30, "opening_balance_paise": 0,
		"opening_type": "dr", "opening_as_of": "2026-04-01", "external_ref": "marg:party:SHARMA"},
	{"name": "Gupta Distributors", "type": "supplier", "credit_limit_paise": 0, "credit_days": 15, "opening_balance_paise": 0,
		"opening_type": "cr", "opening_as_of": "2026-04-01", "external_ref": "marg:party:GUPTA"},
}}

var importBills = []map[string]any{
	{"party_external_ref": "marg:party:SHARMA", "party_name": "Sharma Agencies", "bill_no": "S-101", "bill_date": "2026-01-10",
		"due_date": "2026-02-09", "bill_amount_paise": 150000, "amount_paise": 100000, "type": "dr", "external_ref": "bill:SHARMA:S-101"},
	{"party_external_ref": "marg:party:SHARMA", "party_name": "Sharma Agencies", "bill_no": "S-140", "bill_date": "2026-03-20",
		"amount_paise": 50000, "type": "dr", "external_ref": "bill:SHARMA:S-140"}, // due = bill date + 30 credit days
	{"party_external_ref": "marg:party:GUPTA", "party_name": "Gupta Distributors", "bill_no": "G/77", "bill_date": "2026-03-01",
		"due_date": "2026-03-16", "amount_paise": 80000, "type": "cr", "external_ref": "bill:GUPTA:G/77"},
}

func TestBulkOpenBillsMatchPartyTotals(t *testing.T) {
	s, pool := newTestService(t)

	// Bill-wise.
	bw := newImporter(t, s)
	wantCounts(t, bw.post("parties", importParties), 2, 0, 0)
	res := bw.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": importBills})
	wantCounts(t, res, 3, 0, 0)
	for i, r := range resultsOf(res) {
		if r["created"] != true || r["id"] == "" {
			t.Fatalf("result %d: %v", i, r)
		}
	}
	// Re-delivery is a no-op.
	wantCounts(t, bw.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": importBills}), 0, 3, 0)

	sharma, gupta := partyByRef(t, s, bw.tenant, "marg:party:SHARMA"), partyByRef(t, s, bw.tenant, "marg:party:GUPTA")
	if b := partyBalance(t, pool, bw.tenant, sharma); b != 150000 {
		t.Fatalf("sharma balance %d", b)
	}
	if b := partyBalance(t, pool, bw.tenant, gupta); b != -80000 {
		t.Fatalf("gupta balance %d", b)
	}
	trialBalanced(t, pool, bw.tenant)

	// Bills keep their own number, date and due date; the vouchers are dated as_of.
	st, body := bw.a.do("GET", "/v1/parties/"+sharma+"/bills?as_of=2026-10-23", nil, nil)
	if st != 200 {
		t.Fatalf("bills: %d %v", st, body)
	}
	bills := items(body, "items")
	if len(bills) != 2 || bills[0]["ref"] != "S-101" || bills[0]["bill_date"] != "2026-01-10" || bills[0]["due_date"] != "2026-02-09" ||
		num(bills[0]["outstanding_paise"]) != 100000 || bills[1]["due_date"] != "2026-04-19" {
		t.Fatalf("bills %v", bills)
	}
	var orig int64
	var vdate string
	mustOK(t, pool.QueryRow(context.Background(), `SELECT b.original_amount_paise, v.vdate::text FROM bills b JOIN vouchers v ON v.id=b.voucher_id
		WHERE v.tenant_id=$1 AND v.external_ref='bill:SHARMA:S-101'`, bw.tenant).Scan(&orig, &vdate))
	if orig != 150000 || vdate != "2026-04-01" {
		t.Fatalf("original %d vdate %s", orig, vdate)
	}
	// Due-date aging sees the overdue opening bill.
	if st, body := bw.a.do("GET", "/v1/reports/aging?kind=receivables&as_of=2026-10-23", nil, nil); st != 200 {
		t.Fatalf("aging: %d %v", st, body)
	}

	// The same data as party totals gives the same balances.
	pt := newImporter(t, s)
	wantCounts(t, pt.post("parties", importParties), 2, 0, 0)
	res = pt.post("opening-balances", map[string]any{"as_of": "2026-04-01", "balances": []map[string]any{
		{"party_external_ref": "marg:party:SHARMA", "amount_paise": 150000, "type": "dr", "external_ref": "ob:2026-04-01:party:marg:party:SHARMA"},
		{"party_external_ref": "marg:party:GUPTA", "amount_paise": 80000, "type": "cr"},
	}})
	wantCounts(t, res, 2, 0, 0)
	for _, ref := range []string{"marg:party:SHARMA", "marg:party:GUPTA"} {
		if a, b := partyBalance(t, pool, bw.tenant, partyByRef(t, s, bw.tenant, ref)), partyBalance(t, pool, pt.tenant, partyByRef(t, s, pt.tenant, ref)); a != b {
			t.Fatalf("%s: bill-wise %d vs party totals %d", ref, a, b)
		}
	}
	if a, b := balance(t, pool, bw.tenant, posting.OpeningDiff), balance(t, pool, pt.tenant, posting.OpeningDiff); a != b {
		t.Fatalf("opening difference: bill-wise %d vs party totals %d", a, b)
	}
	var ext string
	mustOK(t, pool.QueryRow(context.Background(), `SELECT external_ref FROM vouchers WHERE tenant_id=$1 AND source_id LIKE 'bulk:%'
		AND external_ref IS NOT NULL`, pt.tenant).Scan(&ext))
	if ext != "ob:2026-04-01:party:marg:party:SHARMA" {
		t.Fatalf("opening line external_ref %q", ext)
	}

	// Bills on top of a party total would count the outstanding twice.
	res = pt.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": importBills[:1]})
	if msg := errorsOf(res)[0]; !strings.Contains(msg, "twice") {
		t.Fatalf("double count not refused: %v", res)
	}

	// Row-level validation.
	res = bw.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": []map[string]any{
		{"party_external_ref": "marg:party:NOBODY", "bill_no": "X", "amount_paise": 100, "type": "dr", "external_ref": "x1"},
		{"party_external_ref": "marg:party:SHARMA", "bill_no": "X", "bill_date": "2026-03-01", "due_date": "2026-02-01",
			"amount_paise": 100, "type": "dr", "external_ref": "x2"},
		{"party_external_ref": "marg:party:SHARMA", "bill_no": "X", "amount_paise": 100, "type": "dr"},
	}})
	errs := errorsOf(res)
	if !strings.Contains(errs[0], "not found") || !strings.Contains(errs[1], "before bill_date") || !strings.Contains(errs[2], "external_ref") {
		t.Fatalf("errors %v", errs)
	}
}

func invoice(docType, number, date, party string, taxable, tax int64) map[string]any {
	inv := map[string]any{"doc_type": docType, "number": number, "date": date, "party_external_ref": party, "party_name": "",
		"party_gstin": "", "place_of_supply": "27", "taxable_paise": taxable, "cgst_paise": tax, "sgst_paise": tax, "igst_paise": 0,
		"cess_paise": 0, "round_off_paise": 0, "total_paise": taxable + 2*tax, "due_date": "", "reference": "", "narration": "",
		"store_id": "", "external_ref": "inv:" + docType + ":" + number}
	return inv
}

func TestBulkInvoiceHistory(t *testing.T) {
	s, pool := newTestService(t)
	im := newImporter(t, s)
	wantCounts(t, im.post("parties", importParties), 2, 0, 0)
	sharma := partyByRef(t, s, im.tenant, "marg:party:SHARMA")

	invs := []map[string]any{
		invoice("sale", "S-090", "2026-03-15", "marg:party:SHARMA", 100000, 9000),  // before as_of: history only
		invoice("sale", "S-201", "2026-05-10", "marg:party:SHARMA", 200000, 18000), // posted on credit
		invoice("sale", "S-202", "2026-05-11", "", 10000, 900),                     // cash bill
		invoice("purchase", "G-55", "2026-06-01", "marg:party:GUPTA", 50000, 4500), // posted purchase
		invoice("purchase_return", "DN-3", "2026-06-05", "marg:party:GUPTA", 5000, 450),
	}
	invs[1]["due_date"] = "2026-06-30"
	invs[2]["round_off_paise"] = int64(-200)
	invs[2]["total_paise"] = int64(10000 + 1800 - 200)
	res := im.post("invoice-history", map[string]any{"as_of": "2026-04-01", "invoices": invs})
	wantCounts(t, res, 5, 0, 0)
	wantCounts(t, im.post("invoice-history", map[string]any{"as_of": "2026-04-01", "invoices": invs}), 0, 5, 0)

	if n := countVouchers(t, pool, im.tenant, srcImport); n != 4 {
		t.Fatalf("posted vouchers %d", n)
	}
	if b := partyBalance(t, pool, im.tenant, sharma); b != 236000 {
		t.Fatalf("sharma balance %d (history must not post)", b)
	}
	if b := balance(t, pool, im.tenant, posting.Cash); b != 11600 {
		t.Fatalf("cash %d", b)
	}
	if b := balance(t, pool, im.tenant, posting.Sales); b != -210000 {
		t.Fatalf("sales %d", b)
	}
	if b := balance(t, pool, im.tenant, posting.RoundOff); b != 200 {
		t.Fatalf("round off %d", b)
	}
	trialBalanced(t, pool, im.tenant)
	var due string
	mustOK(t, pool.QueryRow(context.Background(), `SELECT b.due_date::text FROM bills b JOIN vouchers v ON v.id=b.voucher_id
		WHERE v.tenant_id=$1 AND v.source=$2 AND v.source_id='inv:sale:S-201'`, im.tenant, srcImport).Scan(&due))
	if due != "2026-06-30" {
		t.Fatalf("due %s", due)
	}

	// Party statement shows the history invoice, not as a posting.
	st, body := im.a.do("GET", "/v1/parties/"+sharma+"/statement?from=2026-01-01&to=2026-10-23", nil, nil)
	if st != 200 {
		t.Fatalf("statement %d %v", st, body)
	}
	hist := items(body, "history")
	if len(hist) != 1 || hist[0]["number"] != "S-090" || hist[0]["status"] != "history" || len(items(body, "lines")) != 1 {
		t.Fatalf("statement history %v lines %v", hist, body["lines"])
	}
	st, body = im.a.do("GET", "/v1/invoice-history", nil, nil)
	if st != 200 || len(items(body, "items")) != 5 {
		t.Fatalf("history list %d %v", st, body)
	}
	if st, _, csv := im.a.raw("/v1/invoice-history?status=history&format=csv"); st != 200 || !strings.Contains(csv, "S-090") || strings.Contains(csv, "S-201") {
		t.Fatalf("history csv %d %s", st, csv)
	}

	// Validation and the period lock.
	if st, b := im.a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": "2026-04-30"}, nil); st != 200 {
		t.Fatalf("lock %d %v", st, b)
	}
	bad := invoice("sale", "S-BAD", "2026-05-01", "marg:party:SHARMA", 1000, 90)
	bad["total_paise"] = int64(9999)
	res = im.post("invoice-history", map[string]any{"as_of": "2026-04-01", "invoices": []map[string]any{
		invoice("sale", "S-150", "2026-04-15", "marg:party:SHARMA", 1000, 90), // locked period
		bad,
		invoice("sale", "S-151", "2026-03-31", "marg:party:SHARMA", 1000, 90), // history: no lock check
		invoice("sale", "S-152", "2026-05-02", "marg:party:NOBODY", 1000, 90),
	}})
	errs := errorsOf(res)
	if !strings.Contains(errs[0], "locked") || !strings.Contains(errs[1], "total_paise") || errs[2] != "" || !strings.Contains(errs[3], "not found") {
		t.Fatalf("errors %v", errs)
	}
}

func TestBulkPartiesExtrasUpdateOnReimport(t *testing.T) {
	s, _ := newTestService(t)
	im := newImporter(t, s)
	row := map[string]any{"name": "Sharma Agencies", "type": "customer", "email": "a@sharma.in", "gstin": "27AAPFU0939F1ZV",
		"credit_limit_paise": 500000, "credit_days": 30, "opening_balance_paise": 25000, "opening_type": "dr",
		"opening_as_of": "2026-04-01", "external_ref": "tally:Sharma", "pan": "AAPFU0939F", "shipping_address": "Godown 4, Bhiwandi"}
	res := im.post("parties", map[string]any{"parties": []any{row}})
	wantCounts(t, res, 1, 0, 0)
	id := resultsOf(res)[0]["id"].(string)

	row["email"], row["credit_limit_paise"], row["credit_days"], row["shipping_address"] = "accounts@sharma.in", 900000, 45, "Unit 9, Thane"
	res = im.post("parties", map[string]any{"parties": []any{row}})
	wantCounts(t, res, 0, 1, 0)
	if r := resultsOf(res)[0]; r["id"] != id || r["created"] != false {
		t.Fatalf("result %v", r)
	}
	st, p := im.a.do("GET", "/v1/parties/"+id, nil, nil)
	if st != 200 || p["email"] != "accounts@sharma.in" || num(p["credit_limit_paise"]) != 900000 || num(p["credit_days"]) != 45 ||
		p["shipping_address"] != "Unit 9, Thane" || p["pan"] != "AAPFU0939F" || num(p["balance_paise"]) != 25000 {
		t.Fatalf("party %d %v", st, p)
	}
	row["pan"] = "BAD"
	if errs := errorsOf(im.post("parties", map[string]any{"parties": []any{row}})); !strings.Contains(errs[0], "pan") {
		t.Fatalf("bad pan accepted: %v", errs)
	}
}

func TestBulkAccounts(t *testing.T) {
	s, pool := newTestService(t)
	im := newImporter(t, s)
	accts := []map[string]any{
		{"name": "Shop Rent", "group": "Indirect Expenses", "external_ref": "tally:Shop Rent"}, // bahi group by name
		{"name": "HDFC Current A/c", "group": "Bank Accounts", "external_ref": "tally:HDFC"},   // Tally group → asset, bank
		{"name": "Professional Tax Payable", "group": "Duties & Taxes"},                        // Tally group → 2200
		{"name": "Partner Capital - Ravi", "group": "Capital Account", "code": "3150"},         // Tally group, explicit code
		{"name": "Office Assets", "parent_code": "1500", "is_group": true},                     // a group
		{"name": "Bad", "parent_code": "1100"},                                                 // leaf parent
		{"name": "Bad", "group": "Imaginary Group"},
	}
	res := im.post("accounts", map[string]any{"accounts": accts})
	wantCounts(t, res, 5, 0, 2)
	errs := errorsOf(res)
	if !strings.Contains(errs[5], "not a group") || !strings.Contains(errs[6], "Tally group") {
		t.Fatalf("errors %v", errs)
	}
	var code, parent string
	var bank bool
	mustOK(t, pool.QueryRow(context.Background(), `SELECT a.code, p.code, a.is_bank FROM accounts a JOIN accounts p ON p.id=a.parent_id
		WHERE a.tenant_id=$1 AND a.external_ref='tally:HDFC'`, im.tenant).Scan(&code, &parent, &bank))
	if parent != posting.GroupAssets || !bank || code == "" {
		t.Fatalf("hdfc code=%s parent=%s bank=%v", code, parent, bank)
	}
	mustOK(t, pool.QueryRow(context.Background(), `SELECT p.code FROM accounts a JOIN accounts p ON p.id=a.parent_id
		WHERE a.tenant_id=$1 AND a.name='Professional Tax Payable'`, im.tenant).Scan(&parent))
	if parent != posting.OutputTaxes {
		t.Fatalf("pt parent %s", parent)
	}
	// Idempotent (by external_ref, code, name).
	wantCounts(t, im.post("accounts", map[string]any{"accounts": accts[:5]}), 0, 5, 0)

	// Opening balances can now go to the imported ledgers.
	res = im.post("opening-balances", map[string]any{"as_of": "2026-04-01", "balances": []map[string]any{
		{"account_code": code, "amount_paise": 1234500, "type": "dr"},
		{"account_code": "3150", "amount_paise": 1234500, "type": "cr"},
	}})
	wantCounts(t, res, 2, 0, 0)
	trialBalanced(t, pool, im.tenant)

	// Rollback: the used account stays, the unused one goes.
	res = im.post("rollback", map[string]any{"kind": "accounts", "job_id": "j1", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "tally:HDFC"}, {"external_ref": "tally:Shop Rent"}, {"account_code": "1100"},
	}})
	errs = errorsOf(res)
	if !strings.Contains(errs[0], "posting") || len(errs) != 1 {
		t.Fatalf("rollback %v", res)
	}
	var n int
	mustOK(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM accounts WHERE tenant_id=$1 AND (code='1100' OR external_ref='tally:Shop Rent')`, im.tenant).Scan(&n))
	if n != 1 {
		t.Fatalf("system account must stay and Shop Rent must go: %d", n)
	}
}

func TestBulkRollback(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	im := newImporter(t, s)
	parties := map[string]any{"parties": []map[string]any{
		importParties["parties"].([]map[string]any)[0],
		importParties["parties"].([]map[string]any)[1],
		{"name": "Verma Stores", "type": "customer", "credit_limit_paise": 0, "credit_days": 0, "opening_balance_paise": 70000,
			"opening_type": "dr", "opening_as_of": "2026-04-01", "external_ref": "marg:party:VERMA"},
	}}
	wantCounts(t, im.post("parties", parties), 3, 0, 0)
	wantCounts(t, im.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": importBills}), 3, 0, 0)
	invs := []map[string]any{
		invoice("sale", "S-090", "2026-03-15", "marg:party:SHARMA", 100000, 9000),
		invoice("sale", "S-201", "2026-05-10", "marg:party:SHARMA", 200000, 18000),
	}
	wantCounts(t, im.post("invoice-history", map[string]any{"as_of": "2026-04-01", "invoices": invs}), 2, 0, 0)
	wantCounts(t, im.post("opening-balances", map[string]any{"as_of": "2026-04-01", "balances": []map[string]any{
		{"account_code": posting.Cash, "amount_paise": 500000, "type": "dr"}}}), 1, 0, 0)
	sharma := partyByRef(t, s, im.tenant, "marg:party:SHARMA")

	// A receipt settles bill S-140.
	st, body := im.a.do("GET", "/v1/parties/"+sharma+"/bills", nil, nil)
	if st != 200 {
		t.Fatalf("bills %d %v", st, body)
	}
	var s140 string
	for _, b := range items(body, "items") {
		if b["ref"] == "S-140" {
			s140 = b["id"].(string)
		}
	}
	st, body = im.a.do("POST", "/v1/vouchers", map[string]any{"type": "receipt", "date": "2026-06-01", "narration": "part payment",
		"entries": []map[string]any{{"account_id": accountID(t, pool, im.tenant, posting.Cash), "debit_paise": 20000},
			{"party_id": sharma, "credit_paise": 20000}},
		"allocations": []map[string]any{{"bill_id": s140, "amount_paise": 20000}}}, nil)
	if st != 201 {
		t.Fatalf("receipt %d %v", st, body)
	}

	// Parties with activity are refused, with the reason.
	res := im.post("rollback", map[string]any{"kind": "parties", "job_id": "j1", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "marg:party:SHARMA", "created": true}, {"external_ref": "marg:party:VERMA", "created": true}}})
	errs := errorsOf(res)
	if !strings.Contains(errs[0], "voucher") || errs[1] != "" {
		t.Fatalf("party rollback %v", res)
	}
	var archived bool
	var ref *string
	mustOK(t, pool.QueryRow(ctx, `SELECT archived, external_ref FROM parties WHERE tenant_id=$1 AND name='Verma Stores'`, im.tenant).Scan(&archived, &ref))
	if !archived || ref != nil {
		t.Fatalf("verma archived=%v ref=%v", archived, ref)
	}
	if b := balance(t, pool, im.tenant, posting.OpeningDiff); b != -(150000-80000)-500000 {
		t.Fatalf("opening difference after verma rollback %d", b)
	}

	// Bills: the settled one is refused, the others are reversed.
	res = im.post("rollback", map[string]any{"kind": "open_bills", "job_id": "j2", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "bill:SHARMA:S-101", "party_external_ref": "marg:party:SHARMA"},
		{"external_ref": "bill:SHARMA:S-140", "party_external_ref": "marg:party:SHARMA"},
		{"external_ref": "bill:GUPTA:G/77", "party_external_ref": "marg:party:GUPTA"},
		{"external_ref": "bill:never-imported"},
	}})
	errs = errorsOf(res)
	if len(errs) != 1 || !strings.Contains(errs[1], "settled") {
		t.Fatalf("bills rollback %v", res)
	}
	if b := partyBalance(t, pool, im.tenant, partyByRef(t, s, im.tenant, "marg:party:GUPTA")); b != 0 {
		t.Fatalf("gupta after rollback %d", b)
	}
	// Re-importing a rolled-back bill creates it again.
	wantCounts(t, im.post("open-bills", map[string]any{"as_of": "2026-04-01", "bills": importBills[2:]}), 1, 0, 0)

	// Invoices: history dropped, posted one reversed; both can be imported again.
	res = im.post("rollback", map[string]any{"kind": "invoices", "job_id": "j3", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "inv:sale:S-090"}, {"external_ref": "inv:sale:S-201"}}})
	wantCounts(t, res, 0, 2, 0)
	if b := balance(t, pool, im.tenant, posting.Sales); b != 0 {
		t.Fatalf("sales after rollback %d", b)
	}
	var n int
	mustOK(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoice_history WHERE tenant_id=$1`, im.tenant).Scan(&n))
	if n != 0 {
		t.Fatalf("history rows %d", n)
	}
	wantCounts(t, im.post("invoice-history", map[string]any{"as_of": "2026-04-01", "invoices": invs}), 2, 0, 0)
	wantCounts(t, im.post("rollback", map[string]any{"kind": "invoices", "job_id": "j3", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "inv:sale:S-090"}, {"external_ref": "inv:sale:S-201"}}}), 0, 2, 0)

	// Opening line by key (as_of + account).
	wantCounts(t, im.post("rollback", map[string]any{"kind": "opening_balances", "job_id": "j4", "as_of": "2026-04-01",
		"records": []map[string]any{{"account_code": posting.Cash}}}), 0, 1, 0)
	if b := balance(t, pool, im.tenant, posting.Cash); b != 20000 {
		t.Fatalf("cash after rollback %d", b)
	}

	// Period lock: refused per record.
	if st, b := im.a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": "2026-04-30"}, nil); st != 200 {
		t.Fatalf("lock %d %v", st, b)
	}
	res = im.post("rollback", map[string]any{"kind": "open_bills", "job_id": "j5", "as_of": "2026-04-01", "records": []map[string]any{
		{"external_ref": "bill:GUPTA:G/77"}}})
	if !strings.Contains(errorsOf(res)[0], "locked") {
		t.Fatalf("locked rollback %v", res)
	}
	if st, b := im.a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": ""}, nil); st != 200 {
		t.Fatalf("unlock %d %v", st, b)
	}

	// A party the import only updated (created=false) is left alone.
	wantCounts(t, im.post("rollback", map[string]any{"kind": "parties", "job_id": "j6", "records": []map[string]any{
		{"external_ref": "marg:party:GUPTA", "created": false}}}), 0, 1, 0)
	mustOK(t, pool.QueryRow(ctx, `SELECT archived FROM parties WHERE tenant_id=$1 AND external_ref='marg:party:GUPTA'`, im.tenant).Scan(&archived))
	if archived {
		t.Fatal("updated-only party was archived")
	}
	// Unknown kind / missing as_of.
	st, body = im.a.do("POST", "/internal/v1/bulk/rollback", map[string]any{"kind": "items", "records": []any{}},
		map[string]string{auth.HeaderServiceSecret: testServiceSecret, auth.HeaderTenantID: im.tenant})
	if st != 422 {
		t.Fatalf("unknown kind %d %v", st, body)
	}
	trialBalanced(t, pool, im.tenant)
}
