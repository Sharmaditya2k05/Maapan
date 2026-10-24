package returns

// Head indexes in set-off arrays.
const (
	hIGST = iota
	hCGST
	hSGST
	hCess
)

var headName = [4]string{"igst", "cgst", "sgst", "cess"}

// Utilisation is one credit-to-liability transfer.
type Utilisation struct {
	From   string `json:"from"` // credit head
	To     string `json:"to"`   // liability head
	Amount Rs     `json:"amount"`
}

// PayRow is one line of the GSTR-3B Table 6.1 payment table.
type PayRow struct {
	Head      string `json:"head"`
	Liability Rs     `json:"liability"`
	PaidIGST  Rs     `json:"paid_igst"`
	PaidCGST  Rs     `json:"paid_cgst"`
	PaidSGST  Rs     `json:"paid_sgst"`
	PaidCess  Rs     `json:"paid_cess"`
	Cash      Rs     `json:"cash"`
}

// Taxes is an amount per head.
type Taxes struct {
	IGST Rs `json:"igst"`
	CGST Rs `json:"cgst"`
	SGST Rs `json:"sgst"`
	Cess Rs `json:"cess"`
}

func taxes(v [4]int64) Taxes {
	return Taxes{IGST: Rs(v[hIGST]), CGST: Rs(v[hCGST]), SGST: Rs(v[hSGST]), Cess: Rs(v[hCess])}
}

// SetOff is the result of settling forward-charge liability against ITC.
type SetOff struct {
	Liability    Taxes         `json:"liability"`
	ITCAvailable Taxes         `json:"itc_available"`
	ITCUtilised  Taxes         `json:"itc_utilised"` // by credit head
	CashPayable  Taxes         `json:"cash_payable"` // by liability head
	ITCCarryFwd  Taxes         `json:"itc_carry_forward"`
	Utilisation  []Utilisation `json:"utilisation"`
	Payable      []PayRow      `json:"payable"`
}

// setOff settles liability with credit per Sec 49/49A and Rule 88A:
//
//  1. IGST credit pays IGST liability first.
//  2. What is left of IGST credit must be used up (Sec 49A) before CGST/SGST
//     credit is touched; it goes first to the CGST/SGST liability that the
//     head's own credit cannot cover, so no cash is paid while credit sits
//     idle on the other head, then to the rest of CGST/SGST.
//  3. CGST credit pays CGST, then IGST; SGST credit pays SGST, then IGST.
//     CGST and SGST credit never cross.
//  4. Cess credit pays only cess.
//
// Inputs and outputs are paise; index order IGST, CGST, SGST, cess.
func setOff(liab, cred [4]int64) SetOff {
	l, c := liab, cred
	for i := range l {
		l[i] = max(l[i], 0)
		c[i] = max(c[i], 0)
	}
	start, avail := l, c
	var used [4]int64
	var paid [4][4]int64 // [liability][credit]
	var util []Utilisation
	use := func(from, to int, limit int64) {
		u := min(c[from], l[to], limit)
		if u <= 0 {
			return
		}
		c[from] -= u
		l[to] -= u
		used[from] += u
		paid[to][from] += u
		util = append(util, Utilisation{From: headName[from], To: headName[to], Amount: Rs(u)})
	}
	const all = int64(1) << 62
	use(hIGST, hIGST, all)
	needC := max(l[hCGST]-c[hCGST], 0)
	needS := max(l[hSGST]-c[hSGST], 0)
	use(hIGST, hCGST, needC)
	use(hIGST, hSGST, needS)
	use(hIGST, hCGST, all)
	use(hIGST, hSGST, all)
	use(hCGST, hCGST, all)
	use(hCGST, hIGST, all)
	use(hSGST, hSGST, all)
	use(hSGST, hIGST, all)
	use(hCess, hCess, all)

	s := SetOff{Liability: taxes(start), ITCAvailable: taxes(avail), ITCUtilised: taxes(used), CashPayable: taxes(l),
		ITCCarryFwd: taxes(c), Utilisation: util}
	if s.Utilisation == nil {
		s.Utilisation = []Utilisation{}
	}
	for h := 0; h < 4; h++ {
		s.Payable = append(s.Payable, PayRow{Head: headName[h], Liability: Rs(start[h]), PaidIGST: Rs(paid[h][hIGST]),
			PaidCGST: Rs(paid[h][hCGST]), PaidSGST: Rs(paid[h][hSGST]), PaidCess: Rs(paid[h][hCess]), Cash: Rs(l[h])})
	}
	return s
}
