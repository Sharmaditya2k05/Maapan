package posting

import (
	"errors"
	"fmt"
	"strings"

	"github.com/taraju/mool/events"
)

// Voucher types.
const (
	VSales      = "sales"
	VPurchase   = "purchase"
	VReceipt    = "receipt"
	VPayment    = "payment"
	VJournal    = "journal"
	VContra     = "contra"
	VCreditNote = "credit_note"
	VDebitNote  = "debit_note"
	VProduction = "production"
	VOpening    = "opening"
)

// Sources (vouchers.source) used for idempotency.
const (
	SrcManual     = "manual"
	SrcInvoice    = "aadesh.invoice"
	SrcCancel     = "aadesh.cancel"
	SrcPayment    = "lenden.payment"
	SrcRefund     = "lenden.refund"
	SrcProduction = "nirmaan.production"
	SrcStockMoved = "bhandar.stock.moved"
	SrcSettlement = "lenden.settlement"
	SrcOpening    = "opening"
	SrcBank       = "bank"
	SrcCheque     = "cheque"
	SrcReversal   = "reversal"
)

// Entry is one debit or credit line. Exactly one of AccountCode, AccountID or
// PartyID identifies the account (PartyID means "this party's ledger").
type Entry struct {
	AccountCode string `json:"account_code,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	PartyID     string `json:"party_id,omitempty"`
	StoreID     string `json:"store_id,omitempty"`
	Debit       int64  `json:"debit_paise"`
	Credit      int64  `json:"credit_paise"`
}

// Voucher is a balanced set of entries ready to post.
type Voucher struct {
	Type      string
	Date      string // YYYY-MM-DD
	Narration string
	Source    string
	SourceID  string
	SourceRef string
	Entries   []Entry
}

// Total is the sum of debits.
func (v Voucher) Total() int64 {
	var t int64
	for _, e := range v.Entries {
		t += e.Debit
	}
	return t
}

// Validate checks double-entry rules.
func (v Voucher) Validate() error {
	if len(v.Entries) < 2 {
		return errors.New("a voucher needs at least two entries")
	}
	var dr, cr int64
	for i, e := range v.Entries {
		if e.Debit < 0 || e.Credit < 0 {
			return fmt.Errorf("entry %d: amounts cannot be negative", i+1)
		}
		if (e.Debit == 0) == (e.Credit == 0) {
			return fmt.Errorf("entry %d: exactly one of debit or credit must be set", i+1)
		}
		n := 0
		for _, s := range []string{e.AccountCode, e.AccountID, e.PartyID} {
			if s != "" {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("entry %d: give exactly one of account_code, account_id, party_id", i+1)
		}
		dr += e.Debit
		cr += e.Credit
	}
	if dr != cr {
		return fmt.Errorf("voucher is not balanced: debit %d, credit %d", dr, cr)
	}
	if dr == 0 {
		return errors.New("voucher total is zero")
	}
	return nil
}

// Swap flips every debit to credit and vice versa (reversals, returns).
func Swap(es []Entry) []Entry {
	out := make([]Entry, len(es))
	for i, e := range es {
		e.Debit, e.Credit = e.Credit, e.Debit
		out[i] = e
	}
	return out
}

type b struct {
	store string
	es    []Entry
}

func (x *b) dr(code, party string, amt int64) {
	x.add(code, party, amt, 0)
}
func (x *b) cr(code, party string, amt int64) {
	x.add(code, party, 0, amt)
}
func (x *b) add(code, party string, d, c int64) {
	if d < 0 || c < 0 { // negative amounts go to the other side
		d, c = -c, -d
	}
	if d == 0 && c == 0 {
		return
	}
	e := Entry{StoreID: x.store, Debit: d, Credit: c}
	if party != "" {
		e.PartyID = party
	} else {
		e.AccountCode = code
	}
	x.es = append(x.es, e)
}

// tenderAccount maps counter tender modes to the ledger they settle into.
func tenderAccount(mode string) string {
	if mode == "cash" {
		return Cash
	}
	return Bank
}

// ReceiptAccount is the asset account an online/manual payment settles into.
// Anything captured by a gateway (razorpay, cashfree, …) sits in Payment
// Gateway Clearing until lenden.settlement.processed moves it to Bank. Manual
// (gateway "manual" or empty) receipts go by method: cash → Cash-in-hand,
// cheque → Cheques in Hand, everything else (bank, neft, rtgs, imps,
// netbanking, upi, card, wallet) → Bank.
func ReceiptAccount(gateway, method string) string {
	switch gw := strings.ToLower(strings.TrimSpace(gateway)); gw {
	case "", "manual":
	default:
		return GatewayClearing
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "cash":
		return Cash
	case "cheque":
		return ChequesInHand
	default:
		return Bank
	}
}

// saleShape builds entries shaped like a sale: revenue and output-side taxes on
// the credit side, money/party on the debit side. Other document types are this
// shape swapped and/or with different accounts.
func saleShape(p events.InvoicePayload, revenue string, taxes [4]string, fallbackParty string) []Entry {
	x := &b{store: p.StoreID}
	x.cr(revenue, "", p.TaxablePaise)
	x.cr(taxes[0], "", p.CGSTPaise)
	x.cr(taxes[1], "", p.SGSTPaise)
	x.cr(taxes[2], "", p.IGSTPaise)
	x.cr(taxes[3], "", p.CessPaise)
	// Positive round-off increases what the customer pays: income side (credit Round Off).
	x.cr(RoundOff, "", p.RoundOffPaise)

	remaining := p.GrandTotalPaise
	for _, t := range p.Tenders {
		if t.Mode == "credit" || remaining <= 0 {
			continue
		}
		// A tender backed by a lenden payment request (counter UPI QR / card link)
		// is booked when lenden.payment.captured arrives — Dr Gateway Clearing,
		// Cr party — so it must stay on the party here or it is counted twice.
		if t.PaymentRequestID != "" {
			continue
		}
		amt := min(t.AmountPaise, remaining) // excess cash is change returned
		x.dr(tenderAccount(t.Mode), "", amt)
		remaining -= amt
	}
	if remaining > 0 {
		if p.PartyID != "" {
			x.dr("", p.PartyID, remaining)
		} else {
			x.dr(fallbackParty, "", remaining)
		}
	}
	return x.es
}

var (
	outputTaxes = [4]string{OutputCGST, OutputSGST, OutputIGST, OutputCess}
	inputTaxes  = [4]string{InputCGST, InputSGST, InputIGST, InputCess}
)

// Invoice turns aadesh.invoice.created / aadesh.note.created into a voucher.
//
//	sale            Dr Cash/Bank/Party        Cr Sales, Output GST, Round Off
//	sale_return     Dr Sales Returns, Output GST   Cr Cash/Bank/Party
//	purchase        Dr Purchases, Input GST   Cr Cash/Bank/Supplier
//	purchase_return Dr Cash/Bank/Supplier     Cr Purchase Returns, Input GST
func Invoice(p events.InvoicePayload) (Voucher, error) {
	v := Voucher{Date: p.InvoiceDate, Source: SrcInvoice, SourceID: p.InvoiceID, SourceRef: p.Number}
	switch p.DocType {
	case "sale":
		v.Type = VSales
		v.Entries = saleShape(p, Sales, outputTaxes, WalkInCustomers)
		v.Narration = "Sales invoice " + p.Number + " — " + p.PartyName
	case "sale_return":
		v.Type = VCreditNote
		v.Entries = Swap(saleShape(p, SalesReturns, outputTaxes, WalkInCustomers))
		v.Narration = "Credit note " + p.Number + " — " + p.PartyName
	case "purchase":
		v.Type = VPurchase
		v.Entries = Swap(saleShape(p, Purchases, inputTaxes, CashSuppliers))
		v.Narration = "Purchase bill " + p.Number + " — " + p.PartyName
	case "purchase_return":
		v.Type = VDebitNote
		v.Entries = saleShape(p, PurchaseReturns, inputTaxes, CashSuppliers)
		v.Narration = "Debit note " + p.Number + " — " + p.PartyName
	default:
		return Voucher{}, fmt.Errorf("unknown doc_type %q", p.DocType)
	}
	return v, v.Validate()
}

// Payment turns lenden.payment.captured (or .refunded) into a receipt (or payment)
// voucher: Dr ReceiptAccount(gateway, method) / Cr party. partyID is the
// resolved party ("" = walk-in). A refund is the same entries swapped, keyed on
// the refund id so every partial refund of one payment gets its own voucher.
func Payment(p events.PaymentPayload, partyID, storeID, date string, refund bool) (Voucher, error) {
	x := &b{store: storeID}
	x.dr(ReceiptAccount(p.Gateway, p.Method), "", p.AmountPaise)
	if partyID != "" {
		x.cr("", partyID, p.AmountPaise)
	} else {
		x.cr(WalkInCustomers, "", p.AmountPaise)
	}
	v := Voucher{Type: VReceipt, Date: date, Source: SrcPayment, SourceID: p.PaymentID, SourceRef: p.GatewayRef,
		Narration: fmt.Sprintf("Online payment %s via %s", p.GatewayRef, p.Method), Entries: x.es}
	if refund {
		v.Type, v.Source, v.Entries = VPayment, SrcRefund, Swap(x.es)
		v.Narration = "Refund of " + p.GatewayRef
		if p.RefundID != "" {
			v.SourceID = p.RefundID
			v.Narration = fmt.Sprintf("Refund %s of %s", p.RefundID, p.GatewayRef)
		}
	}
	return v, v.Validate()
}

// Stock movement reasons (bhandar) that bahi reclassifies.
const (
	reasonSale              = "sale"
	reasonPurchase          = "purchase"
	reasonSaleReturn        = "sale_return"
	reasonPurchaseReturn    = "purchase_return"
	reasonTransferIn        = "transfer_in"
	reasonTransferOut       = "transfer_out"
	reasonAdjustment        = "adjustment"
	reasonOpening           = "opening"
	reasonProductionConsume = "production_consume"
	reasonProductionOutput  = "production_output"
	reasonExpiryWriteoff    = "expiry_writeoff"
)

// StockMoved turns bhandar.stock.moved into a perpetual-inventory
// reclassification. The invoice vouchers stay as they are (Purchases is debited
// on a purchase bill, Sales credited on a sale); this voucher moves cost between
// the P&L and Inventory using bhandar's weighted-average value:
//
//	purchase         Dr Inventory          Cr Purchases          (value, +)
//	purchase_return  Dr Purchase Returns   Cr Inventory          (value, −)
//	sale             Dr Cost of Goods Sold Cr Inventory          (value, −)
//	sale_return      Dr Inventory          Cr Cost of Goods Sold (value, +)
//	adjustment /     qty<0: Dr Stock Loss  Cr Inventory
//	expiry_writeoff  qty>0: Dr Inventory   Cr Stock Gain
//	opening          Dr Inventory          Cr Opening Balance Difference
//
// transfer_in/out move stock between stores of the same tenant — same Inventory
// account, nothing to post. production_consume/output are already booked from
// nirmaan.production.completed (Dr Stock-FG, Cr Stock-RM/WIP), so they are
// skipped too. A reversal payload (invoice cancelled) is just the negative
// movement and follows the same rules. ok=false means there is nothing to post.
func StockMoved(p events.StockMovedPayload, date string) (v Voucher, ok bool, err error) {
	value := p.ValuePaise
	var loss, gain int64
	for _, m := range p.Movements {
		if p.ValuePaise == 0 {
			value += m.ValuePaise
		}
		if m.ValuePaise < 0 {
			loss -= m.ValuePaise
		} else {
			gain += m.ValuePaise
		}
	}
	if len(p.Movements) == 0 {
		if value < 0 {
			loss = -value
		} else {
			gain = value
		}
	}
	x := &b{store: p.StoreID}
	switch p.Reason {
	case reasonPurchase:
		x.dr(Inventory, "", value)
		x.cr(Purchases, "", value)
	case reasonPurchaseReturn:
		x.dr(Inventory, "", value) // value < 0 → credit Inventory, debit Purchase Returns
		x.cr(PurchaseReturns, "", value)
	case reasonSale, reasonSaleReturn:
		x.dr(Inventory, "", value) // sale: value < 0 → credit Inventory, debit COGS
		x.cr(COGS, "", value)
	case reasonAdjustment, reasonExpiryWriteoff:
		x.dr(StockLoss, "", loss)
		x.cr(Inventory, "", loss)
		x.dr(Inventory, "", gain)
		x.cr(StockGain, "", gain)
	case reasonOpening:
		x.dr(Inventory, "", value)
		x.cr(OpeningDiff, "", value)
	case reasonTransferIn, reasonTransferOut, reasonProductionConsume, reasonProductionOutput:
		return Voucher{}, false, nil
	default:
		return Voucher{}, false, fmt.Errorf("unknown stock movement reason %q", p.Reason)
	}
	if len(x.es) == 0 { // zero-value movement (e.g. free goods at nil cost)
		return Voucher{}, false, nil
	}
	sourceID := p.Source + "/" + p.SourceID
	what := "Stock " + p.Reason
	if p.Reversal {
		sourceID += "/rev"
		what += " reversal"
	}
	v = Voucher{Type: VJournal, Date: date, Source: SrcStockMoved, SourceID: sourceID, SourceRef: p.SourceID,
		Narration: fmt.Sprintf("%s — %s %s (%d items)", what, p.Source, p.SourceID, len(p.Movements)), Entries: x.es}
	return v, true, v.Validate()
}

// Settlement turns lenden.settlement.processed into the payout voucher:
//
//	Dr Bank            net
//	Dr Bank Charges    fee
//	Dr Input CGST      ⌈fee tax / 2⌉   (odd paisa on CGST)
//	Dr Input SGST      ⌊fee tax / 2⌋
//	Cr Gateway Clearing gross − refunds
//
// The payload must satisfy gross − refunds = net + fee + fee tax, otherwise it
// is rejected (the caller quarantines it). ok=false means a zero settlement.
func Settlement(p events.SettlementPayload, storeID, date string) (v Voucher, ok bool, err error) {
	if p.SettlementID == "" {
		return Voucher{}, false, errors.New("settlement without settlement_id")
	}
	if p.GrossPaise-p.RefundsPaise != p.NetPaise+p.FeePaise+p.FeeTaxPaise {
		return Voucher{}, false, fmt.Errorf("settlement does not balance: gross %d − refunds %d ≠ net %d + fee %d + fee tax %d",
			p.GrossPaise, p.RefundsPaise, p.NetPaise, p.FeePaise, p.FeeTaxPaise)
	}
	cgst := p.FeeTaxPaise/2 + p.FeeTaxPaise%2
	sgst := p.FeeTaxPaise - cgst
	x := &b{store: storeID}
	x.dr(Bank, "", p.NetPaise)
	x.dr(BankCharges, "", p.FeePaise)
	x.dr(InputCGST, "", cgst)
	x.dr(InputSGST, "", sgst)
	x.cr(GatewayClearing, "", p.GrossPaise-p.RefundsPaise)
	if len(x.es) == 0 {
		return Voucher{}, false, nil
	}
	ref := p.GatewayRef
	if ref == "" {
		ref = p.UTR
	}
	narr := fmt.Sprintf("%s settlement %s", p.Gateway, ref)
	if p.UTR != "" && p.UTR != ref {
		narr += " (UTR " + p.UTR + ")"
	}
	v = Voucher{Type: VReceipt, Date: date, Source: SrcSettlement, SourceID: p.SettlementID, SourceRef: ref,
		Narration: narr, Entries: x.es}
	return v, true, v.Validate()
}

// Production turns nirmaan.production.completed into a stock transfer:
// Dr Finished Goods at total cost, Cr Raw Materials at consumed cost, and any
// difference (labour/overheads absorbed) Cr Work-in-Progress.
func Production(p events.ProductionCompletedPayload, date string) (Voucher, error) {
	var consumed int64
	for _, l := range p.Consumed {
		consumed += lineCost(l)
	}
	total := p.TotalCostPaise
	if total == 0 {
		for _, l := range p.Produced {
			total += lineCost(l)
		}
	}
	if total == 0 {
		total = consumed
	}
	x := &b{store: p.StoreID}
	x.dr(StockFinished, "", total)
	x.cr(StockRaw, "", consumed)
	x.cr(WorkInProgress, "", total-consumed)
	v := Voucher{Type: VProduction, Date: date, Source: SrcProduction, SourceID: p.ProductionRunID,
		Narration: "Production run " + p.ProductionRunID, Entries: x.es}
	return v, v.Validate()
}

func lineCost(l events.ProductionLine) int64 {
	return int64(l.Qty*float64(l.UnitCostPaise) + 0.5)
}

// Opening builds an opening-balance voucher for one account/party against
// Opening Balance Difference. drcr is "dr" or "cr".
func Opening(target Entry, amount int64, drcr, date, sourceID string) (Voucher, error) {
	if amount <= 0 {
		return Voucher{}, errors.New("opening amount must be positive")
	}
	t := target
	t.Debit, t.Credit = 0, 0
	o := Entry{AccountCode: OpeningDiff, StoreID: t.StoreID}
	switch drcr {
	case "dr":
		t.Debit, o.Credit = amount, amount
	case "cr":
		t.Credit, o.Debit = amount, amount
	default:
		return Voucher{}, errors.New(`type must be "dr" or "cr"`)
	}
	v := Voucher{Type: VOpening, Date: date, Source: SrcOpening, SourceID: sourceID,
		Narration: "Opening balance", Entries: []Entry{t, o}}
	return v, v.Validate()
}
