package ledger

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
)

// raw GETs path and returns status, content type and body (CSV/XML endpoints).
func (a *api) raw(path string) (int, string, string) {
	a.t.Helper()
	req, err := http.NewRequest("GET", a.srv.URL+path, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), string(b)
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func items(body map[string]any, key string) []map[string]any {
	var out []map[string]any
	list, _ := body[key].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		out = append(out, m)
	}
	return out
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func billOf(t *testing.T, s *Service, tenant, invoiceID string) string {
	t.Helper()
	var id string
	mustOK(t, s.DB.QueryRow(context.Background(), `SELECT b.id FROM bills b JOIN vouchers v ON v.id=b.voucher_id
		WHERE v.tenant_id=$1 AND v.source=$2 AND v.source_id=$3`, tenant, posting.SrcInvoice, invoiceID).Scan(&id))
	return id
}

func TestBillwiseAllocationAndDueDateAging(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	cust := newParty(t, s, tenant, PartyInput{Name: "Ramesh Traders", Type: "customer", CreditDays: 30, CreditLimitPaise: 100000})

	sale := func(id, date, due string, total int64) events.InvoicePayload {
		tax := total * 9 / 118
		return events.InvoicePayload{InvoiceID: id, StoreID: store, DocType: "sale", Number: "INV-" + id[:4], InvoiceDate: date, DueDate: due,
			PartyID: cust, PartyName: "Ramesh Traders", TaxablePaise: total - 2*tax, CGSTPaise: tax, SGSTPaise: tax, GrandTotalPaise: total}
	}
	inv1 := sale(uuid.NewString(), "2026-08-01", "", 118000)           // due 2026-08-31 (credit days)
	inv2 := sale(uuid.NewString(), "2026-10-10", "2026-11-30", 59000) // due date sent by aadesh
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, inv1)))
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, inv2)))
	b1, b2 := billOf(t, s, tenant, inv1.InvoiceID), billOf(t, s, tenant, inv2.InvoiceID)

	// A lenden payment against inv1 and a sales return against inv2 allocate themselves.
	mustOK(t, router[events.PaymentCaptured](ctx, envelope(t, events.PaymentCaptured, tenant, store, events.PaymentPayload{
		PaymentID: "pay-1", InvoiceID: inv1.InvoiceID, PartyID: cust, AmountPaise: 50000, Method: "upi", Gateway: "razorpay",
		GatewayRef: "pay_1", CapturedAt: "2026-10-01T06:00:00Z"})))
	mustOK(t, router[events.NoteCreated](ctx, envelope(t, events.NoteCreated, tenant, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale_return", Number: "CN-1", InvoiceDate: "2026-10-12",
		RefInvoiceID: inv2.InvoiceID, PartyID: cust, TaxablePaise: 5000, CGSTPaise: 450, SGSTPaise: 450, GrandTotalPaise: 5900})))

	st, body := a.do("GET", "/v1/parties/"+cust+"/bills", nil, nil)
	bills := items(body, "items")
	if st != 200 || len(bills) != 2 {
		t.Fatalf("bills: %d %v", st, body)
	}
	if bills[0]["id"] != b1 || num(bills[0]["outstanding_paise"]) != 68000 || bills[0]["due_date"] != "2026-08-31" || num(bills[0]["overdue_days"]) != 53 {
		t.Fatalf("inv1 bill %v", bills[0])
	}
	if bills[1]["due_date"] != "2026-11-30" || num(bills[1]["outstanding_paise"]) != 53100 || num(bills[1]["overdue_days"]) != 0 {
		t.Fatalf("inv2 bill %v", bills[1])
	}

	// Manual receipt against inv1 (against reference) …
	cash := accountID(t, pool, tenant, posting.Cash)
	st, body = a.do("POST", "/v1/vouchers", map[string]any{"type": "receipt", "date": "2026-10-15",
		"entries":     []map[string]any{{"account_id": cash, "debit_paise": 30000}, {"party_id": cust, "credit_paise": 30000}},
		"allocations": []map[string]any{{"bill_id": b1, "amount_paise": 30000}}}, nil)
	if st != 201 {
		t.Fatalf("receipt: %d %v", st, body)
	}
	rcpt := body["id"].(string)
	if st, body := a.do("PUT", "/v1/vouchers/"+rcpt+"/allocations", map[string]any{"allocations": []map[string]any{
		{"bill_id": b1, "amount_paise": 40000}}}, nil); st != 422 {
		t.Fatalf("allocating more than the receipt must fail: %d %v", st, body)
	}
	if st, body := a.do("PUT", "/v1/vouchers/"+rcpt+"/allocations", map[string]any{"allocations": []map[string]any{
		{"bill_id": b2, "amount_paise": 60000}}}, nil); st != 422 {
		t.Fatalf("allocating more than the bill's outstanding must fail: %d %v", st, body)
	}
	// … and one on account.
	if st, body := a.do("POST", "/v1/vouchers", map[string]any{"type": "receipt", "date": "2026-10-16",
		"entries": []map[string]any{{"account_id": cash, "debit_paise": 100000}, {"party_id": cust, "credit_paise": 100000}}}, nil); st != 201 {
		t.Fatalf("on-account receipt: %d %v", st, body)
	}

	// Aging by due date: inv1's 38,000 is 53 days overdue, inv2 not due; the advance is on account.
	st, body = a.do("GET", "/v1/reports/aging?kind=receivables", nil, nil)
	rows := items(body, "rows")
	if st != 200 || len(rows) != 1 || body["basis"] != "due" {
		t.Fatalf("aging %d %v", st, body)
	}
	r := rows[0]
	if num(r["d31_60_paise"]) != 38000 || num(r["not_due_paise"]) != 53100 || num(r["total_paise"]) != 91100 ||
		num(r["on_account_paise"]) != 100000 || num(r["net_paise"]) != -8900 {
		t.Fatalf("aging row %v", r)
	}
	if st, _ := a.do("GET", "/v1/reports/aging?kind=receivables&basis=date", nil, nil); st != 200 {
		t.Fatalf("legacy basis: %d", st)
	}

	// FIFO auto-allocation by due date uses the on-account receipt.
	st, body = a.do("POST", "/v1/parties/"+cust+"/allocations/auto", nil, nil)
	if st != 200 || num(body["allocations_created"]) != 2 {
		t.Fatalf("auto: %d %v", st, body)
	}
	stmt := body["statement"].(map[string]any)
	if num(stmt["bills_outstanding_paise"]) != 0 || num(stmt["unallocated_paise"]) != -8900 || num(stmt["balance_paise"]) != -8900 {
		t.Fatalf("statement after auto %v", stmt)
	}

	// Reversing the manual receipt frees its allocation: inv1 is open again.
	if st, body := a.do("POST", "/v1/vouchers/"+rcpt+"/reverse", map[string]any{"reason": "keyed twice"}, nil); st != 201 {
		t.Fatalf("reverse: %d %v", st, body)
	}
	st, body = a.do("GET", "/v1/parties/"+cust+"/statement?view=bills", nil, nil)
	if st != 200 || num(body["bills_outstanding_paise"]) != 30000 || num(body["overdue_paise"]) != 30000 || num(body["balance_paise"]) != 21100 ||
		num(body["unallocated_paise"]) != -8900 {
		t.Fatalf("bill-wise statement %d %v", st, body)
	}
	if len(items(body, "on_account")) != 1 {
		t.Fatalf("on-account list %v", body["on_account"])
	}
	if code, ctype, csv := a.raw("/v1/parties/" + cust + "/statement?view=bills&format=csv"); code != 200 || !strings.HasPrefix(ctype, "text/csv") ||
		!strings.Contains(csv, "On account") {
		t.Fatalf("csv %d %s %s", code, ctype, csv)
	}

	// aadesh's credit check.
	st, body = a.do("GET", "/internal/v1/parties/"+cust+"/credit", nil, map[string]string{auth.HeaderServiceSecret: testServiceSecret, auth.HeaderTenantID: tenant})
	if st != 200 || num(body["balance_paise"]) != 21100 || num(body["available_paise"]) != 78900 || body["over_limit"] != false {
		t.Fatalf("credit %d %v", st, body)
	}

	// soochna's reminder feed: inv1's ₹300 is overdue since 31 Aug; inv2 is not due.
	svc := map[string]string{auth.HeaderServiceSecret: testServiceSecret, auth.HeaderTenantID: tenant}
	st, body = a.do("GET", "/internal/v1/receivables/overdue?as_of=2026-10-23", nil, svc)
	od := items(body, "rows")
	if st != 200 || len(od) != 1 || od[0]["party_id"] != cust || num(od[0]["overdue_paise"]) != 30000 ||
		od[0]["oldest_due_date"] != "2026-08-31" || num(od[0]["days_overdue"]) != 53 || od[0]["pay_link"] != nil {
		t.Fatalf("overdue %d %v", st, body)
	}
	if st, body := a.do("GET", "/internal/v1/receivables/overdue?as_of=2026-08-15", nil, svc); st != 200 || len(items(body, "rows")) != 0 {
		t.Fatalf("nothing overdue before the due date: %v", body)
	}
	if st, _ := a.do("GET", "/internal/v1/receivables/overdue", nil, nil); st != 401 && st != 403 {
		t.Fatalf("needs the service secret: %d", st)
	}
	trialBalanced(t, pool, tenant)
}

func TestChequeRegisterLifecycle(t *testing.T) {
	s, pool := newTestService(t)
	tenant, store := uuid.NewString(), uuid.NewString()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	cust := newParty(t, s, tenant, PartyInput{Name: "Cheque Customer", Type: "customer"})
	supp := newParty(t, s, tenant, PartyInput{Name: "Cheque Supplier", Type: "supplier"})
	bank := accountID(t, pool, tenant, posting.Bank)
	cheque := func(dir, party, no, date string, amt int64) map[string]any {
		t.Helper()
		st, body := a.do("POST", "/v1/cheques", map[string]any{"direction": dir, "party_id": party, "bank_account_id": bank,
			"cheque_no": no, "cheque_date": date, "amount_paise": amt}, nil)
		if st != 201 {
			t.Fatalf("cheque %s: %d %v", no, st, body)
		}
		return body
	}
	status := func(id, to, date string) (int, map[string]any) {
		return a.do("POST", "/v1/cheques/"+id+"/status", map[string]any{"status": to, "date": date, "reason": "funds insufficient"}, nil)
	}

	// Received today: Dr Cheques in Hand / Cr party at once; clearing moves it to Bank.
	c1 := cheque("received", cust, "100001", "2026-10-23", 50000)
	if c1["voucher_id"] == nil || balance(t, pool, tenant, posting.ChequesInHand) != 50000 || partyBalance(t, pool, tenant, cust) != -50000 {
		t.Fatalf("received cheque not booked to Cheques in Hand: %v", c1)
	}
	if st, _ := status(c1["id"].(string), "deposited", "2026-10-23"); st != 200 {
		t.Fatal("deposit")
	}
	if st, body := status(c1["id"].(string), "cleared", "2026-10-23"); st != 200 || body["clear_voucher_id"] == nil {
		t.Fatalf("clear %d %v", st, body)
	}
	if balance(t, pool, tenant, posting.ChequesInHand) != 0 || balance(t, pool, tenant, posting.Bank) != 50000 {
		t.Fatal("clearing must move Cheques in Hand to Bank")
	}

	// A bounce reverses the receipt: the customer owes again.
	c2 := cheque("received", cust, "100002", "2026-10-20", 20000)
	status(c2["id"].(string), "deposited", "2026-10-21")
	st, body := status(c2["id"].(string), "bounced", "2026-10-23")
	if st != 200 || body["bounce_voucher_id"] == nil || body["bounce_reason"] != "funds insufficient" {
		t.Fatalf("bounce %d %v", st, body)
	}
	if balance(t, pool, tenant, posting.ChequesInHand) != 0 || partyBalance(t, pool, tenant, cust) != -50000 {
		t.Fatalf("after bounce: CIH %d party %d", balance(t, pool, tenant, posting.ChequesInHand), partyBalance(t, pool, tenant, cust))
	}

	// Post-dated: nothing booked until it reaches the bank on its date.
	pdc := cheque("received", cust, "100003", "2026-11-05", 30000)
	if pdc["voucher_id"] != nil || pdc["post_dated"] != true {
		t.Fatalf("PDC booked early: %v", pdc)
	}
	if st, _ := status(pdc["id"].(string), "deposited", "2026-10-25"); st != 409 {
		t.Fatalf("depositing a PDC before its date: %d", st)
	}
	st, body = a.do("GET", "/v1/cheques/pdc?days=7", nil, nil)
	var found bool
	for _, it := range items(body, "items") {
		if it["cheque_no"] == "100003" {
			found = it["bucket"] == "later" && num(it["days_to_due"]) == 13
		}
	}
	if st != 200 || !found {
		t.Fatalf("pdc report %d %v", st, body)
	}

	// Issued: paid out of Bank at once; a bounce puts it back.
	c4 := cheque("issued", supp, "500001", "2026-10-22", 15000)
	if balance(t, pool, tenant, posting.Bank) != 35000 || partyBalance(t, pool, tenant, supp) != 15000 {
		t.Fatalf("issued cheque: bank %d", balance(t, pool, tenant, posting.Bank))
	}
	if st, _ := status(c4["id"].(string), "bounced", "2026-10-23"); st != 200 || balance(t, pool, tenant, posting.Bank) != 50000 ||
		partyBalance(t, pool, tenant, supp) != 0 {
		t.Fatal("issued bounce")
	}
	if st, _ := status(c4["id"].(string), "cleared", "2026-10-23"); st != 409 {
		t.Fatal("a bounced cheque cannot clear")
	}
	if st, body := a.do("GET", "/v1/cheques?status=pending", nil, nil); st != 200 || len(items(body, "items")) != 1 {
		t.Fatalf("pending %v", body)
	}
	trialBalanced(t, pool, tenant)
}

func TestBankStatementImportAndBRS(t *testing.T) {
	s, pool := newTestService(t)
	tenant, store := uuid.NewString(), uuid.NewString()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	cust := newParty(t, s, tenant, PartyInput{Name: "Neft Customer", Type: "customer"})
	supp := newParty(t, s, tenant, PartyInput{Name: "Cheque Payee", Type: "supplier"})
	bank := accountID(t, pool, tenant, posting.Bank)

	// Books: NEFT receipt ₹1,000 (UTR in the reference), cheque 000777 issued ₹500.
	if st, body := a.do("POST", "/v1/vouchers", map[string]any{"type": "receipt", "date": "2026-10-09", "reference": "HDFCR52026100912345",
		"entries": []map[string]any{{"account_id": bank, "debit_paise": 100000}, {"party_id": cust, "credit_paise": 100000}}}, nil); st != 201 {
		t.Fatalf("receipt %d %v", st, body)
	}
	if st, body := a.do("POST", "/v1/cheques", map[string]any{"direction": "issued", "party_id": supp, "bank_account_id": bank,
		"cheque_no": "777", "cheque_date": "2026-10-10", "received_on": "2026-10-10", "amount_paise": 50000}, nil); st != 201 {
		t.Fatalf("cheque %d %v", st, body)
	}

	stmt1 := "Date,Narration,Chq./Ref.No.,Value Dt,Withdrawal Amt.,Deposit Amt.,Closing Balance\n" +
		"10/10/26,NEFT CR-HDFCR52026100912345-NEFT CUSTOMER,,10/10/26,,1000.00,1000.00\n" +
		"12/10/26,SMS CHARGES QTR,,12/10/26,59.00,,941.00\n"
	imp := func(csv string) map[string]any {
		st, body := a.do("POST", "/v1/bank/statements/import", map[string]any{"bank_account_id": bank, "preset": "hdfc",
			"file_name": "oct.csv", "content_base64": base64.StdEncoding.EncodeToString([]byte(csv))}, nil)
		if st != 201 {
			t.Fatalf("import %d %v", st, body)
		}
		return body
	}
	res := imp(stmt1)
	if num(res["lines"]) != 2 || num(res["matched"]) != 1 {
		t.Fatalf("import 1: %v", res)
	}
	if again := imp(stmt1); num(again["lines"]) != 0 || num(again["duplicates"]) != 2 {
		t.Fatalf("re-import must skip duplicates: %v", again)
	}

	// BRS as of 15 Oct: the cheque is unpresented, the SMS charge is not in the books.
	st, brs := a.do("GET", "/v1/reports/brs?bank_account_id="+bank+"&as_of=2026-10-15", nil, nil)
	if st != 200 || num(brs["balance_per_books_paise"]) != 50000 || num(brs["unpresented_paise"]) != 50000 ||
		num(brs["bank_debits_not_in_books_paise"]) != 5900 || num(brs["balance_per_bank_computed_paise"]) != 94100 ||
		num(brs["balance_per_statement_paise"]) != 94100 || num(brs["difference_paise"]) != 0 {
		t.Fatalf("brs %d %v", st, brs)
	}

	// Book the charge from the unmatched line.
	st, body := a.do("GET", "/v1/bank/statement-lines?bank_account_id="+bank+"&status=unmatched&from=2026-10-01&to=2026-10-31", nil, nil)
	lines := items(body, "items")
	if st != 200 || len(lines) != 1 {
		t.Fatalf("unmatched %d %v", st, body)
	}
	charge := lines[0]["id"].(string)
	if st, body := a.do("POST", "/v1/bank/statement-lines/"+charge+"/voucher", map[string]any{
		"account_id": accountID(t, pool, tenant, posting.BankCharges)}, nil); st != 201 || body["type"] != "payment" {
		t.Fatalf("line voucher %d %v", st, body)
	}
	if b := balance(t, pool, tenant, posting.BankCharges); b != 5900 {
		t.Fatalf("bank charges %d", b)
	}

	// The cheque is presented in the next statement; cheque no 000777 matches 777.
	res = imp("Date,Narration,Chq./Ref.No.,Value Dt,Withdrawal Amt.,Deposit Amt.,Closing Balance\n" +
		"16/10/26,CHQ PAID-CHEQUE PAYEE,000777,16/10/26,500.00,,441.00\n")
	if num(res["matched"]) != 1 {
		t.Fatalf("cheque not matched: %v", res)
	}
	st, brs = a.do("GET", "/v1/reports/brs?bank_account_id="+bank+"&as_of=2026-10-20", nil, nil)
	if st != 200 || num(brs["unpresented_paise"]) != 0 || num(brs["bank_debits_not_in_books_paise"]) != 0 || num(brs["difference_paise"]) != 0 ||
		num(brs["balance_per_books_paise"]) != 44100 {
		t.Fatalf("brs after %v", brs)
	}

	// Manual unmatch / match.
	st, body = a.do("GET", "/v1/bank/statement-lines?bank_account_id="+bank+"&status=matched&from=2026-10-16&to=2026-10-16", nil, nil)
	ln := items(body, "items")[0]
	vid := ln["voucher_id"].(string)
	if st, body := a.do("POST", "/v1/bank/statement-lines/"+ln["id"].(string)+"/unmatch", nil, nil); st != 200 || body["status"] != "unmatched" {
		t.Fatalf("unmatch %d %v", st, body)
	}
	if st, body := a.do("POST", "/v1/bank/statement-lines/"+ln["id"].(string)+"/match", map[string]any{"voucher_id": vid}, nil); st != 200 ||
		body["match_rule"] != "manual" {
		t.Fatalf("match %d %v", st, body)
	}
	if code, _, csv := a.raw("/v1/reports/brs?bank_account_id=" + bank + "&as_of=2026-10-15&format=csv"); code != 200 || !strings.Contains(csv, "Balance as per books") {
		t.Fatalf("brs csv %d %s", code, csv)
	}
	if st, body := a.do("GET", "/v1/bank/statement-presets", nil, nil); st != 200 || len(items(body, "items")) != 6 {
		t.Fatalf("presets %v", body)
	}
	trialBalanced(t, pool, tenant)
}

func TestTDSDeductionChallanAnd26Q(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	bank := accountID(t, pool, tenant, posting.Bank)
	contractor := newParty(t, s, tenant, PartyInput{Name: "Build Co", Type: "supplier", PAN: "aaacb1234c", TDSSection: "194c"})
	consultant := newParty(t, s, tenant, PartyInput{Name: "CA Sharma", Type: "supplier", TDSSection: "194J"}) // no PAN
	if _, err := tryParty(s, tenant, PartyInput{Name: "Bad PAN", PAN: "123"}); err == nil {
		t.Fatal("invalid PAN accepted")
	}

	st, body := a.do("POST", "/v1/tds/compute", map[string]any{"party_id": contractor, "base_paise": 4000000, "date": "2026-10-05"}, nil)
	res, _ := body["result"].(map[string]any)
	if st != 200 || num(res["tds_paise"]) != 80000 || num(res["rate_bp"]) != 200 {
		t.Fatalf("compute %d %v", st, body)
	}
	// Deduction on the contractor's bill: Dr party / Cr TDS Payable.
	st, body = a.do("POST", "/v1/tds/deductions", map[string]any{"party_id": contractor, "base_paise": 4000000, "date": "2026-10-05", "key": "bill-1"}, nil)
	if st != 201 || num(body["tds_paise"]) != 80000 || body["section"] != "194C" {
		t.Fatalf("deduct %d %v", st, body)
	}
	if st, body := a.do("POST", "/v1/tds/deductions", map[string]any{"party_id": contractor, "base_paise": 4000000, "date": "2026-10-05", "key": "bill-1"}, nil); st != 201 ||
		num(body["tds_paise"]) != 80000 {
		t.Fatal("same key is idempotent")
	}
	if b := balance(t, pool, tenant, posting.TDSPayable); b != -80000 {
		t.Fatalf("TDS payable %d", b)
	}
	// Payment voucher with TDS to a consultant without PAN: 20 % (206AA).
	st, body = a.do("POST", "/v1/vouchers", map[string]any{"type": "payment", "date": "2026-10-10",
		"entries": []map[string]any{{"party_id": consultant, "debit_paise": 4800000}, {"account_id": bank, "credit_paise": 4800000}},
		"tds":     map[string]any{"party_id": consultant, "base_paise": 6000000}}, nil)
	if st != 201 {
		t.Fatalf("payment with tds %d %v", st, body)
	}
	if b := partyBalance(t, pool, tenant, consultant); b != 6000000 {
		t.Fatalf("consultant settled gross %d", b)
	}
	if b := balance(t, pool, tenant, posting.TDSPayable); b != -80000-1200000 {
		t.Fatalf("TDS payable %d", b)
	}

	// Challan for October settles both entries.
	st, body = a.do("POST", "/v1/tds/challans", map[string]any{"bsr_code": "0510308", "challan_no": "00042", "paid_on": "2026-11-07",
		"bank_account_id": bank, "month": "2026-10", "interest_paise": 1500}, nil)
	if st != 201 || num(body["amount_paise"]) != 1280000 || num(body["entries"]) != 2 {
		t.Fatalf("challan %d %v", st, body)
	}
	if b := balance(t, pool, tenant, posting.TDSPayable); b != 0 {
		t.Fatalf("payable after challan %d", b)
	}
	if st, _ := a.do("POST", "/v1/tds/challans", map[string]any{"bsr_code": "0510308", "challan_no": "00043", "paid_on": "2026-11-07",
		"bank_account_id": bank, "month": "2026-10"}, nil); st != 422 {
		t.Fatal("nothing left to deposit")
	}
	code, ctype, csv := a.raw("/v1/tds/26q?fy=26-27&quarter=3&format=csv")
	if code != 200 || !strings.HasPrefix(ctype, "text/csv") || !strings.Contains(csv, "94C") || !strings.Contains(csv, "PANNOTAVBL") ||
		!strings.Contains(csv, "0510308") || !strings.Contains(csv, ",C,") {
		t.Fatalf("26Q %d %s\n%s", code, ctype, csv)
	}
	st, body = a.do("GET", "/v1/tds/summary?fy=26-27", nil, nil)
	if st != 200 || len(items(body, "lines")) != 2 {
		t.Fatalf("summary %v", body)
	}

	// 194Q on purchase bills, automatically: ₹60 lakh bill from a 194Q supplier → 0.1 % of ₹10 lakh.
	if st, _ := a.do("PUT", "/v1/settings/tds", map[string]any{"tan": "MUMA12345B", "deductor_name": "Maapan", "auto_194q": true}, nil); st != 200 {
		t.Fatal("tds settings")
	}
	goods := newParty(t, s, tenant, PartyInput{Name: "Big Mill", Type: "supplier", PAN: "AAACM1234M", TDSSection: "194Q"})
	inv := events.InvoicePayload{InvoiceID: uuid.NewString(), StoreID: store, DocType: "purchase", Number: "PB-1", InvoiceDate: "2026-10-12",
		PartyID: goods, PartyName: "Big Mill", TaxablePaise: 60_00_000_00, IGSTPaise: 3_00_000_00, GrandTotalPaise: 63_00_000_00}
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, inv)))
	if b := partyBalance(t, pool, tenant, goods); b != -(63_00_000_00 - 1_000_00) {
		t.Fatalf("supplier after 194Q %d", b)
	}
	st, body = a.do("GET", "/v1/parties/"+goods+"/bills", nil, nil)
	if bl := items(body, "items"); st != 200 || len(bl) != 1 || num(bl[0]["outstanding_paise"]) != 63_00_000_00-1_000_00 {
		t.Fatalf("TDS journal allocated against the bill: %v", body)
	}
	trialBalanced(t, pool, tenant)
}

func TestGSTSetOff(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store := uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	bank := accountID(t, pool, tenant, posting.Bank)
	// October: output CGST/SGST ₹900 each; input IGST ₹500, CGST ₹200.
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), DocType: "sale", Number: "S1", InvoiceDate: "2026-10-05", TaxablePaise: 1000000,
		CGSTPaise: 90000, SGSTPaise: 90000, GrandTotalPaise: 1180000, Tenders: []events.Tender{{Mode: "cash", AmountPaise: 1180000}}})))
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), DocType: "purchase", Number: "P1", InvoiceDate: "2026-10-06", TaxablePaise: 500000,
		IGSTPaise: 50000, CGSTPaise: 20000, GrandTotalPaise: 570000, Tenders: []events.Tender{{Mode: "cash", AmountPaise: 570000}}})))

	// kar's computation (rupees): IGST 500 → CGST 500 is wrong per 88A only if CGST credit is idle; here kar
	// used IGST on CGST 500 and SGST 0 → CGST credit 200 pays CGST … as computed by kar.
	comp := map[string]any{
		"utilisation":  []map[string]any{{"from": "igst", "to": "cgst", "amount": 500.00}, {"from": "cgst", "to": "cgst", "amount": 200.00}},
		"cash_payable": map[string]any{"igst": 0, "cgst": 200.00, "sgst": "900.00", "cess": 0},
	}
	st, body := a.do("POST", "/v1/gst/setoff", map[string]any{"gstin": "27AAPFU0939F1ZV", "period": "102026", "date": "2026-10-31",
		"computation": comp, "pay_from_bank_account_id": bank}, nil)
	if st != 201 || body["setoff_voucher"] == nil || body["cash_voucher"] == nil {
		t.Fatalf("setoff %d %v", st, body)
	}
	for code, want := range map[string]int64{posting.OutputCGST: 0, posting.OutputSGST: 0, posting.InputIGST: 0, posting.InputCGST: 0,
		posting.ElectronicCashCGST: 0, posting.ElectronicCashSGST: 0, posting.Bank: -110000} {
		if b := balance(t, pool, tenant, code); b != want {
			t.Errorf("%s = %d, want %d", code, b, want)
		}
	}
	if st, _ := a.do("POST", "/v1/gst/setoff", map[string]any{"gstin": "27AAPFU0939F1ZV", "period": "102026", "computation": comp}, nil); st != 409 {
		t.Fatal("second set-off for the period must conflict")
	}
	bad := map[string]any{"utilisation": []map[string]any{{"from": "cgst", "to": "sgst", "amount": 1}}}
	if st, _ := a.do("POST", "/v1/gst/setoff", map[string]any{"gstin": "27AAPFU0939F1ZV", "period": "112026", "computation": bad}, nil); st != 422 {
		t.Fatal("CGST credit against SGST must be refused")
	}
	if st, _ := a.do("POST", "/v1/gst/setoff", map[string]any{"gstin": "27AAPFU0939F1ZV", "period": "112026"}, nil); st != 422 {
		t.Fatal("no computation and no kar must be a validation error")
	}

	// kar.return.filed posts the next period's set-off (cash part only here).
	env := envelope(t, events.ReturnFiled, tenant, store, events.ReturnFiledPayload{ReturnType: "GSTR3B", GSTIN: "27AAPFU0939F1ZV",
		Period: "092026", FiledAt: "2026-10-20T10:00:00Z", CashPaise: map[string]int64{"cgst": 100}})
	mustOK(t, router[events.ReturnFiled](ctx, env))
	mustOK(t, router[events.ReturnFiled](ctx, env))
	if b := balance(t, pool, tenant, posting.OutputCGST); b != 100 {
		t.Fatalf("event set-off %d", b)
	}
	st, body = a.do("GET", "/v1/gst/setoffs", nil, nil)
	if st != 200 || len(items(body, "items")) != 2 {
		t.Fatalf("setoffs %v", body)
	}
	trialBalanced(t, pool, tenant)
}

func TestYearEndCloseStorePLTallyAndAudit(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant, store, store2 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := s.EventRoutes()
	seedTenant(t, s, tenant, store)
	a := newAPI(t, s, tenant, store)
	cust := newParty(t, s, tenant, PartyInput{Name: "Year Customer", Type: "customer", GSTIN: "27AAPFU0939F1ZV", CreditDays: 15})

	// FY 25-26: sale ₹1,000 (store 1, on credit), rent ₹300, drawings ₹100.
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store, events.InvoicePayload{
		InvoiceID: uuid.NewString(), StoreID: store, DocType: "sale", Number: "S-FY25", InvoiceDate: "2025-06-01", PartyID: cust,
		TaxablePaise: 100000, GrandTotalPaise: 100000})))
	cash := accountID(t, pool, tenant, posting.Cash)
	for code, amt := range map[string]int64{posting.Rent: 30000, posting.Drawings: 10000} {
		if st, body := a.do("POST", "/v1/vouchers", map[string]any{"type": "payment", "date": "2025-07-01",
			"entries": []map[string]any{{"account_id": accountID(t, pool, tenant, code), "debit_paise": amt}, {"account_id": cash, "credit_paise": amt}}}, nil); st != 201 {
			t.Fatalf("payment %d %v", st, body)
		}
	}
	// FY 26-27 sale in store 2.
	mustOK(t, router[events.InvoiceCreated](ctx, envelope(t, events.InvoiceCreated, tenant, store2, events.InvoicePayload{
		InvoiceID: uuid.NewString(), StoreID: store2, DocType: "sale", Number: "S-FY26", InvoiceDate: "2026-05-01",
		TaxablePaise: 50000, GrandTotalPaise: 50000, Tenders: []events.Tender{{Mode: "cash", AmountPaise: 50000}}})))

	if st, _ := a.do("POST", "/v1/year-end/close", map[string]any{"fy": "26-27"}, nil); st != 422 {
		t.Fatal("an open year cannot be closed")
	}
	st, body := a.do("POST", "/v1/year-end/close", map[string]any{"fy": "25-26"}, nil)
	if st != 201 || num(body["net_profit_paise"]) != 70000 || num(body["drawings_closed_paise"]) != 10000 || body["locked_until"] != "2026-03-31" {
		t.Fatalf("close %d %v", st, body)
	}
	// Capital gets profit − drawings; P&L accounts are zero at 31 March.
	if b := balance(t, pool, tenant, posting.Capital); b != -60000 {
		t.Fatalf("capital %d", b)
	}
	st, tb := a.do("GET", "/v1/reports/trial-balance?as_of=2026-03-31", nil, nil)
	for _, r := range items(tb, "rows") {
		if r["group"] == "income" || r["group"] == "expense" || r["code"] == posting.Drawings {
			t.Fatalf("closed TB still has %v", r)
		}
	}
	// The closed year's P&L still reads as earned.
	st, pl := a.do("GET", "/v1/reports/profit-loss?from=2025-04-01&to=2026-03-31", nil, nil)
	if st != 200 || num(pl["net_profit_paise"]) != 70000 {
		t.Fatalf("closed year P&L %v", pl)
	}
	st, bs := a.do("GET", "/v1/reports/balance-sheet?as_of=2026-10-23", nil, nil)
	if st != 200 || bs["balanced"] != true {
		t.Fatalf("balance sheet %v", bs)
	}
	if st, _ := a.do("POST", "/v1/year-end/close", map[string]any{"fy": "25-26"}, nil); st != 409 {
		t.Fatal("closing twice")
	}
	if st, _ := a.do("POST", "/v1/year-end/25-26/reopen", nil, nil); st != 409 {
		t.Fatal("reopening needs the period unlocked")
	}
	a.do("PUT", "/v1/settings/period-lock", map[string]any{"locked_until": ""}, nil)
	if st, body := a.do("POST", "/v1/year-end/25-26/reopen", nil, nil); st != 200 {
		t.Fatalf("reopen %d %v", st, body)
	}
	if b := balance(t, pool, tenant, posting.Capital); b != 0 {
		t.Fatalf("capital after reopen %d", b)
	}
	if st, pl := a.do("GET", "/v1/reports/profit-loss?from=2025-04-01&to=2026-03-31", nil, nil); st != 200 || num(pl["net_profit_paise"]) != 70000 {
		t.Fatalf("P&L after reopen %v", pl)
	}
	if st, body := a.do("GET", "/v1/year-end", nil, nil); st != 200 || len(items(body, "items")) != 1 || items(body, "items")[0]["status"] != "reopened" {
		t.Fatalf("year-end list %v", body)
	}

	// Store-wise P&L and cash book.
	st, body = a.do("GET", "/v1/reports/profit-loss?from=2026-04-01&to=2026-10-23&store_id="+store2, nil, nil)
	if st != 200 || num(body["net_profit_paise"]) != 50000 {
		t.Fatalf("store P&L %v", body)
	}
	st, body = a.do("GET", "/v1/reports/profit-loss/stores?from=2025-04-01&to=2026-10-23", nil, nil)
	if st != 200 || len(items(body, "stores")) != 3 { // store, store2, untagged manual payments
		t.Fatalf("stores %v", body)
	}
	st, body = a.do("GET", "/v1/cashbook?from=2026-04-01&to=2026-10-23&store_id="+store2, nil, nil)
	if st != 200 || num(body["closing_balance_paise"]) != 50000 || len(items(body, "lines")) != 1 {
		t.Fatalf("store cash book %v", body)
	}

	// Tally XML: masters with bill-wise party ledger and vouchers with New Ref.
	code, ctype, x := a.raw("/v1/export/tally?from=2025-04-01&to=2026-10-23&company=Maapan")
	if code != 200 || !strings.HasPrefix(ctype, "application/xml") {
		t.Fatalf("tally %d %s", code, ctype)
	}
	var env tallyEnvelope
	if err := xml.Unmarshal([]byte(x), &env); err != nil {
		t.Fatal(err)
	}
	var party *tallyLedger
	var newRef bool
	for _, m := range env.Body.ImportData.RequestData.Messages {
		if m.Ledger != nil && m.Ledger.Name == "Year Customer" {
			party = m.Ledger
		}
		if m.Voucher != nil && m.Voucher.Reference == "S-FY25" {
			for _, e := range m.Voucher.Entries {
				for _, b := range e.Bills {
					newRef = newRef || (b.BillType == "New Ref" && b.Name == "S-FY25" && b.Amount == "-1000.00")
				}
			}
		}
	}
	if party == nil || party.Parent != "Sundry Debtors" || party.IsBillWise != "Yes" || party.GSTIN != "27AAPFU0939F1ZV" || !newRef {
		t.Fatalf("tally export: %+v newRef=%v\n%s", party, newRef, x[:min(len(x), 2000)])
	}

	// Audit trail: a party edit is captured with before/after and the actor.
	if st, _ := a.do("PATCH", "/v1/parties/"+cust, map[string]any{"credit_days": 45}, nil); st != 200 {
		t.Fatal("patch")
	}
	st, body = a.do("GET", "/v1/audit?from=2000-01-01&to=2100-01-01&entity=parties&entity_id="+cust+"&action=update", nil, nil)
	rows := items(body, "items")
	if st != 200 || len(rows) == 0 || rows[0]["actor"] != "u1" {
		t.Fatalf("audit %d %v", st, body)
	}
	before, _ := rows[0]["before"].(map[string]any)
	after, _ := rows[0]["after"].(map[string]any)
	if num(before["credit_days"]) != 15 || num(after["credit_days"]) != 45 {
		t.Fatalf("audit images %v → %v", before, after)
	}
	trialBalanced(t, pool, tenant)
}

func TestMigrationBackfillsBills(t *testing.T) {
	s, pool := newTestService(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	seedTenant(t, s, tenant, uuid.NewString())
	cust := newParty(t, s, tenant, PartyInput{Name: "Old Customer", Type: "customer", CreditDays: 10,
		OpeningBalancePaise: 25000, OpeningType: "dr", OpeningAsOf: "2026-04-01"})
	// Drop the bill Post raised and re-run the backfill statement of 0003 as an upgrade would.
	mustOK(t, func() error { _, err := pool.Exec(ctx, `DELETE FROM bills WHERE tenant_id=$1`, tenant); return err }())
	_, err := pool.Exec(ctx, `INSERT INTO bills (tenant_id, party_id, voucher_id, side, ref, bill_date, due_date, amount_paise)
		SELECT v.tenant_id, e.party_id, v.id, CASE WHEN sum(e.debit_paise - e.credit_paise) > 0 THEN 'dr' ELSE 'cr' END,
		       COALESCE(v.source_ref, v.number), v.vdate, v.vdate + p.credit_days, abs(sum(e.debit_paise - e.credit_paise))
		  FROM vouchers v JOIN voucher_entries e ON e.voucher_id=v.id AND e.party_id IS NOT NULL JOIN parties p ON p.id=e.party_id
		 WHERE v.vtype IN ('sales','purchase','opening') AND v.status='posted' AND v.reversal_of IS NULL
		 GROUP BY v.tenant_id, v.id, e.party_id, p.credit_days
		HAVING (v.vtype='opening' AND sum(e.debit_paise - e.credit_paise) <> 0)
		ON CONFLICT (tenant_id, voucher_id, party_id) DO NOTHING`)
	mustOK(t, err)
	var due string
	var amt int64
	mustOK(t, pool.QueryRow(ctx, `SELECT due_date::text, amount_paise FROM bills WHERE party_id=$1`, cust).Scan(&due, &amt))
	if due != "2026-04-11" || amt != 25000 {
		t.Fatalf("backfilled opening bill %s %d", due, amt)
	}
}
