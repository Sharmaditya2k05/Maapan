package ledger

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// Tally Prime XML export (CA handover): ledger masters with their opening
// balance at `from`, then every voucher of the period with bill-wise
// allocations (New Ref / Agst Ref / On Account), in the "Import Data"
// envelope Tally accepts via Gateway of Tally → Import → Masters/Transactions.
// Tally's sign convention: debit amounts are negative, ISDEEMEDPOSITIVE=Yes.

type tallyEnvelope struct {
	XMLName xml.Name `xml:"ENVELOPE"`
	Header  struct {
		TallyRequest string `xml:"TALLYREQUEST"`
	} `xml:"HEADER"`
	Body struct {
		ImportData struct {
			RequestDesc struct {
				ReportName string `xml:"REPORTNAME"`
				Static     struct {
					Company string `xml:"SVCURRENTCOMPANY,omitempty"`
				} `xml:"STATICVARIABLES"`
			} `xml:"REQUESTDESC"`
			RequestData struct {
				Messages []tallyMessage `xml:"TALLYMESSAGE"`
			} `xml:"REQUESTDATA"`
		} `xml:"IMPORTDATA"`
	} `xml:"BODY"`
}

type tallyMessage struct {
	UDF     string        `xml:"xmlns:UDF,attr"`
	Ledger  *tallyLedger  `xml:"LEDGER,omitempty"`
	Voucher *tallyVoucher `xml:"VOUCHER,omitempty"`
}

type tallyLedger struct {
	Name          string `xml:"NAME,attr"`
	Action        string `xml:"ACTION,attr"`
	NameList      string `xml:"NAME.LIST>NAME"`
	Parent        string `xml:"PARENT"`
	IsBillWise    string `xml:"ISBILLWISEON,omitempty"`
	CreditPeriod  string `xml:"BILLCREDITPERIOD,omitempty"`
	Opening       string `xml:"OPENINGBALANCE"`
	GSTIN         string `xml:"PARTYGSTIN,omitempty"`
	RegType       string `xml:"GSTREGISTRATIONTYPE,omitempty"`
	PAN           string `xml:"INCOMETAXNUMBER,omitempty"`
	Address       string `xml:"ADDRESS.LIST>ADDRESS,omitempty"`
	Phone         string `xml:"LEDGERPHONE,omitempty"`
	Email         string `xml:"EMAIL,omitempty"`
	AffectsStock  string `xml:"AFFECTSSTOCK,omitempty"`
	UseForGSTDuty string `xml:"TAXTYPE,omitempty"`
}

type tallyBill struct {
	Name     string `xml:"NAME"`
	BillType string `xml:"BILLTYPE"`
	Credit   string `xml:"BILLCREDITPERIOD,omitempty"`
	Amount   string `xml:"AMOUNT"`
}

type tallyEntry struct {
	LedgerName string      `xml:"LEDGERNAME"`
	IsDeemed   string      `xml:"ISDEEMEDPOSITIVE"`
	IsParty    string      `xml:"ISPARTYLEDGER"`
	Amount     string      `xml:"AMOUNT"`
	Bills      []tallyBill `xml:"BILLALLOCATIONS.LIST,omitempty"`
}

type tallyVoucher struct {
	VchType   string       `xml:"VCHTYPE,attr"`
	Action    string       `xml:"ACTION,attr"`
	GUID      string       `xml:"GUID"`
	Date      string       `xml:"DATE"`
	TypeName  string       `xml:"VOUCHERTYPENAME"`
	Number    string       `xml:"VOUCHERNUMBER"`
	Reference string       `xml:"REFERENCE,omitempty"`
	Narration string       `xml:"NARRATION,omitempty"`
	PartyName string       `xml:"PARTYLEDGERNAME,omitempty"`
	IsInvoice string       `xml:"ISINVOICE"`
	Entries   []tallyEntry `xml:"ALLLEDGERENTRIES.LIST"`
}

var tallyVchType = map[string]string{
	posting.VSales: "Sales", posting.VPurchase: "Purchase", posting.VReceipt: "Receipt", posting.VPayment: "Payment",
	posting.VJournal: "Journal", posting.VContra: "Contra", posting.VCreditNote: "Credit Note", posting.VDebitNote: "Debit Note",
	posting.VProduction: "Stock Journal", posting.VOpening: "Journal",
}

// tallyAmount renders paise in Tally's sign convention (debit negative).
func tallyAmount(debitMinusCredit int64) string { return paiseStr(-debitMinusCredit) }

type tallyAcct struct {
	id, code, name, grp, parent string
	isGroup, isCash, isBank     bool
	party                       *Party
}

// tallyParent maps a bahi account to a Tally primary group.
func tallyParent(a *tallyAcct, byID map[string]*tallyAcct) string {
	codes := map[string]bool{}
	for p := byID[a.parent]; p != nil; p = byID[p.parent] {
		codes[p.code] = true
	}
	switch {
	case codes[posting.SundryDebtors]:
		return "Sundry Debtors"
	case codes[posting.SundryCreditors]:
		return "Sundry Creditors"
	case a.isCash:
		return "Cash-in-Hand"
	case a.isBank:
		return "Bank Accounts"
	case codes[posting.InputTaxes] || codes[posting.OutputTaxes]:
		return "Duties & Taxes"
	case codes[posting.StockInHand]:
		return "Stock-in-Hand"
	case codes[posting.FixedAssets]:
		return "Fixed Assets"
	case a.code == posting.SecuredLoans:
		return "Secured Loans"
	case a.code == posting.UnsecuredLoans:
		return "Unsecured Loans"
	case codes[posting.Loans]:
		return "Loans (Liability)"
	case a.grp == "equity":
		return "Capital Account"
	case a.code == posting.Sales || a.code == posting.SalesReturns:
		return "Sales Accounts"
	case a.code == posting.Purchases || a.code == posting.PurchaseReturns:
		return "Purchase Accounts"
	case codes[posting.DirectExpenses]:
		return "Direct Expenses"
	case a.grp == "expense":
		return "Indirect Expenses"
	case a.grp == "income":
		return "Indirect Incomes"
	case a.grp == "liability":
		return "Current Liabilities"
	}
	return "Current Assets"
}

func (s *Service) handleTallyExport(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	env, err := s.tallyXML(r.Context(), p.TenantID, from, to, httpx.Query(r, "company"))
	if err != nil {
		return err
	}
	out, err := xml.MarshalIndent(env, "", " ")
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="tally-%s-%s.xml"`, from, to))
	_, err = w.Write(append([]byte(xml.Header), out...))
	return err
}

func (s *Service) tallyXML(ctx context.Context, tenantID, from, to, company string) (*tallyEnvelope, error) {
	env := &tallyEnvelope{}
	env.Header.TallyRequest = "Import Data"
	env.Body.ImportData.RequestDesc.ReportName = "All Masters"
	env.Body.ImportData.RequestDesc.Static.Company = company

	rows, err := s.DB.Query(ctx, `SELECT id, code, name, grp, COALESCE(parent_id::text,''), is_group, is_cash, is_bank FROM accounts
		WHERE tenant_id=$1 ORDER BY code`, tenantID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*tallyAcct{}
	var order []*tallyAcct
	for rows.Next() {
		a := &tallyAcct{}
		if err := rows.Scan(&a.id, &a.code, &a.name, &a.grp, &a.parent, &a.isGroup, &a.isCash, &a.isBank); err != nil {
			rows.Close()
			return nil, err
		}
		byID[a.id] = a
		order = append(order, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prow, err := s.DB.Query(ctx, `SELECT `+partyCols+` FROM parties p WHERE p.tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		pt, err := scanParty(prow)
		if err != nil {
			prow.Close()
			return nil, err
		}
		if a := byID[pt.AccountID]; a != nil {
			a.party = pt
		}
	}
	prow.Close()
	if err := prow.Err(); err != nil {
		return nil, err
	}
	// Opening balances at `from` and which ledgers the period uses.
	opening := map[string]int64{}
	used := map[string]bool{}
	orow, err := s.DB.Query(ctx, `SELECT account_id, COALESCE(sum(debit_paise - credit_paise) FILTER (WHERE vdate < $2),0),
			bool_or(vdate BETWEEN $2 AND $3)
		FROM voucher_entries WHERE tenant_id=$1 AND vdate <= $3 GROUP BY account_id`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	for orow.Next() {
		var id string
		var bal int64
		var inPeriod bool
		if err := orow.Scan(&id, &bal, &inPeriod); err != nil {
			orow.Close()
			return nil, err
		}
		opening[id], used[id] = bal, inPeriod || bal != 0
	}
	orow.Close()
	if err := orow.Err(); err != nil {
		return nil, err
	}
	// Unique ledger names (Tally names are unique and case-insensitive).
	name := map[string]string{}
	taken := map[string]bool{}
	for _, a := range order {
		n := strings.TrimSpace(a.name)
		if taken[strings.ToLower(n)] {
			n = fmt.Sprintf("%s (%s)", n, a.code)
		}
		taken[strings.ToLower(n)] = true
		name[a.id] = n
	}
	msgs := []tallyMessage{}
	for _, a := range order {
		if a.isGroup || !used[a.id] {
			continue
		}
		l := &tallyLedger{Name: name[a.id], Action: "Create", NameList: name[a.id], Parent: tallyParent(a, byID),
			Opening: tallyAmount(opening[a.id])}
		if pt := a.party; pt != nil {
			l.IsBillWise = "Yes"
			if pt.CreditDays > 0 {
				l.CreditPeriod = fmt.Sprintf("%d Days", pt.CreditDays)
			}
			l.GSTIN, l.PAN, l.Address, l.Phone, l.Email = pt.GSTIN, pt.PAN, pt.Address, pt.Phone, pt.Email
			l.RegType = "Unregistered/Consumer"
			if pt.GSTIN != "" {
				l.RegType = "Regular"
			}
		} else if l.Parent == "Duties & Taxes" {
			l.UseForGSTDuty = "GST"
			switch a.code {
			case posting.TDSPayable, posting.TDSReceivable:
				l.UseForGSTDuty = "TDS"
			case posting.TCSPayable, posting.TCSReceivable:
				l.UseForGSTDuty = "TCS"
			}
		}
		msgs = append(msgs, tallyMessage{UDF: "TallyUDF", Ledger: l})
	}

	// Bill references for bill-wise allocation lines.
	type billRef struct {
		ref, party string
		days       int
	}
	billOf := map[string]billRef{} // voucher_id|party_id → bill raised by that voucher
	brow, err := s.DB.Query(ctx, `SELECT b.voucher_id, b.party_id, b.ref, (b.due_date - b.bill_date) FROM bills b
		JOIN vouchers v ON v.id=b.voucher_id WHERE b.tenant_id=$1 AND v.vdate BETWEEN $2 AND $3`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	for brow.Next() {
		var vid, pid, ref string
		var days int
		if err := brow.Scan(&vid, &pid, &ref, &days); err != nil {
			brow.Close()
			return nil, err
		}
		billOf[vid+"|"+pid] = billRef{ref: ref, party: pid, days: days}
	}
	brow.Close()
	type alloc struct {
		ref string
		amt int64
	}
	allocsOf := map[string][]alloc{}
	arow, err := s.DB.Query(ctx, `SELECT a.voucher_id, a.party_id, b.ref, a.amount_paise FROM bill_allocations a JOIN bills b ON b.id=a.bill_id
		WHERE a.tenant_id=$1 AND a.alloc_date BETWEEN $2 AND $3 ORDER BY a.created_at`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	for arow.Next() {
		var vid, pid, ref string
		var amt int64
		if err := arow.Scan(&vid, &pid, &ref, &amt); err != nil {
			arow.Close()
			return nil, err
		}
		allocsOf[vid+"|"+pid] = append(allocsOf[vid+"|"+pid], alloc{ref, amt})
	}
	arow.Close()

	vrows, err := s.DB.Query(ctx, `SELECT `+voucherCols+` FROM vouchers v WHERE v.tenant_id=$1 AND v.vdate BETWEEN $2 AND $3
		ORDER BY v.vdate, v.created_at`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	var vs []*VoucherView
	var ids []string
	for vrows.Next() {
		v, err := scanVoucher(vrows)
		if err != nil {
			vrows.Close()
			return nil, err
		}
		vs, ids = append(vs, v), append(ids, v.ID)
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}
	es, err := loadEntries(ctx, s.DB, ids)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		d, _ := time.Parse(time.DateOnly, v.Date)
		tv := &tallyVoucher{VchType: tallyVchType[v.Type], Action: "Create", GUID: "bahi-" + v.ID, Date: d.Format("20060102"),
			TypeName: tallyVchType[v.Type], Number: v.Number, Reference: v.SourceRef, Narration: v.Narration, IsInvoice: "No"}
		// net per party in this voucher, for the bill lines
		for _, e := range es[v.ID] {
			amt := e.DebitPaise - e.CreditPaise
			te := tallyEntry{LedgerName: name[e.AccountID], IsDeemed: "No", IsParty: "No", Amount: tallyAmount(amt)}
			if amt > 0 {
				te.IsDeemed = "Yes"
			}
			if e.PartyID != "" {
				te.IsParty = "Yes"
				if tv.PartyName == "" {
					tv.PartyName = name[e.AccountID]
				}
				key := v.ID + "|" + e.PartyID
				if b, ok := billOf[key]; ok {
					tb := tallyBill{Name: b.ref, BillType: "New Ref", Amount: tallyAmount(amt)}
					if b.days > 0 {
						tb.Credit = fmt.Sprintf("%d Days", b.days)
					}
					te.Bills = append(te.Bills, tb)
				} else {
					sign := int64(1)
					if amt < 0 {
						sign = -1
					}
					left := amt * sign
					for _, a := range allocsOf[key] {
						use := min(a.amt, left)
						if use <= 0 {
							break
						}
						te.Bills = append(te.Bills, tallyBill{Name: a.ref, BillType: "Agst Ref", Amount: tallyAmount(use * sign)})
						left -= use
					}
					allocsOf[key] = nil // one line per party carries all its allocations
					if left > 0 {
						te.Bills = append(te.Bills, tallyBill{Name: v.Number, BillType: "On Account", Amount: tallyAmount(left * sign)})
					}
				}
			}
			tv.Entries = append(tv.Entries, te)
		}
		msgs = append(msgs, tallyMessage{UDF: "TallyUDF", Voucher: tv})
	}
	env.Body.ImportData.RequestData.Messages = msgs
	return env, nil
}
