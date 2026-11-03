package posting

import (
	"errors"
	"fmt"
	"strings"
)

// GST heads in set-off order.
var GSTHeads = []string{"igst", "cgst", "sgst", "cess"}

var (
	outputByHead = map[string]string{"igst": OutputIGST, "cgst": OutputCGST, "sgst": OutputSGST, "cess": OutputCess}
	inputByHead  = map[string]string{"igst": InputIGST, "cgst": InputCGST, "sgst": InputSGST, "cess": InputCess}
	cashByHead   = map[string]string{"igst": ElectronicCashIGST, "cgst": ElectronicCashCGST, "sgst": ElectronicCashSGST, "cess": ElectronicCashCess}
)

// Utilisation is one credit-to-liability transfer of a GSTR-3B set-off
// (kar's computation.utilisation), in paise.
type Utilisation struct {
	From        string `json:"from"` // credit head
	To          string `json:"to"`   // liability head
	AmountPaise int64  `json:"amount_paise"`
}

// SetOffInput is the month's set-off: ITC utilisation per Rule 88A plus the
// forward-charge liability left for cash, by head.
type SetOffInput struct {
	GSTIN       string
	Period      string // MMYYYY
	Date        string // voucher date
	Utilisation []Utilisation
	Cash        map[string]int64 // liability head → cash paid from the electronic cash ledger
}

// allowedCredit is Sec 49 / 49A / Rule 88A: which credit head may pay which
// liability head. CGST and SGST never cross; cess pays only cess.
var allowedCredit = map[string]map[string]bool{
	"igst": {"igst": true, "cgst": true, "sgst": true},
	"cgst": {"cgst": true, "igst": true},
	"sgst": {"sgst": true, "igst": true},
	"cess": {"cess": true},
}

func normHead(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

// SetOff builds the set-off journal:
//
//	Dr Output <liability head>   Cr Input <credit head>           ITC utilised
//	Dr Output <head>             Cr Electronic Cash Ledger <head> cash portion
//
// Utilisation that breaks Rule 88A ordering of heads (CGST↔SGST, cess) is
// rejected. ok=false means there is nothing to set off.
func SetOff(in SetOffInput) (v Voucher, ok bool, err error) {
	x := &b{}
	for i, u := range in.Utilisation {
		from, to := normHead(u.From), normHead(u.To)
		if !allowedCredit[from][to] {
			return Voucher{}, false, fmt.Errorf("utilisation %d: %s credit cannot pay %s liability", i+1, u.From, u.To)
		}
		if u.AmountPaise < 0 {
			return Voucher{}, false, fmt.Errorf("utilisation %d: negative amount", i+1)
		}
		x.dr(outputByHead[to], "", u.AmountPaise)
		x.cr(inputByHead[from], "", u.AmountPaise)
	}
	for _, h := range GSTHeads {
		amt := in.Cash[h]
		if amt < 0 {
			return Voucher{}, false, fmt.Errorf("cash %s: negative amount", h)
		}
		x.dr(outputByHead[h], "", amt)
		x.cr(cashByHead[h], "", amt)
	}
	for h := range in.Cash {
		if _, known := cashByHead[h]; !known {
			return Voucher{}, false, fmt.Errorf("unknown GST head %q", h)
		}
	}
	if len(x.es) == 0 {
		return Voucher{}, false, nil
	}
	if in.Period == "" {
		return Voucher{}, false, errors.New("period is required")
	}
	v = Voucher{Type: VJournal, Date: in.Date, Source: SrcGSTSetOff, SourceID: strings.ToUpper(in.GSTIN) + "/" + in.Period,
		SourceRef: "GSTR-3B " + in.Period, Narration: fmt.Sprintf("GST set-off %s %s (Rule 88A)", in.GSTIN, in.Period), Entries: x.es}
	return v, true, v.Validate()
}

// GSTCashDeposit is the PMT-06 challan: Dr Electronic Cash Ledger per head,
// Cr the bank account the challan was paid from.
func GSTCashDeposit(gstin, period, date, bankAccountID string, cash map[string]int64) (Voucher, bool, error) {
	x := &b{}
	var total int64
	for _, h := range GSTHeads {
		if cash[h] < 0 {
			return Voucher{}, false, fmt.Errorf("cash %s: negative amount", h)
		}
		x.dr(cashByHead[h], "", cash[h])
		total += cash[h]
	}
	if total == 0 {
		return Voucher{}, false, nil
	}
	x.es = append(x.es, Entry{AccountID: bankAccountID, Credit: total})
	v := Voucher{Type: VPayment, Date: date, Source: SrcGSTCash, SourceID: strings.ToUpper(gstin) + "/" + period,
		SourceRef: "PMT-06 " + period, Narration: fmt.Sprintf("GST cash deposit %s %s", gstin, period), Entries: x.es}
	return v, true, v.Validate()
}
