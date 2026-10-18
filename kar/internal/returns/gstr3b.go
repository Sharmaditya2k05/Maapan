package returns

import (
	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/compliance"
)

// Head is one row of tax amounts in rupees.
type Head struct {
	Txval float64 `json:"txval,omitempty"`
	Iamt  float64 `json:"iamt"`
	Camt  float64 `json:"camt"`
	Samt  float64 `json:"samt"`
	Csamt float64 `json:"csamt"`
}

func (a amounts) head() Head {
	r := compliance.Rupees
	return Head{Txval: r(a.txval), Iamt: r(a.iamt), Camt: r(a.camt), Samt: r(a.samt), Csamt: r(a.csamt)}
}

type UnregSupply struct {
	Pos   string  `json:"pos"`
	Txval float64 `json:"txval"`
	Iamt  float64 `json:"iamt"`
}

type ITCRow struct {
	Ty string `json:"ty"`
	Head
}

// Taxes are per-head amounts used in the set-off computation (paise inside, rupees out).
type Taxes struct {
	IGST float64 `json:"igst"`
	CGST float64 `json:"cgst"`
	SGST float64 `json:"sgst"`
	Cess float64 `json:"cess"`
}

// Computation shows how output tax is settled against ITC.
type Computation struct {
	Liability    Taxes `json:"liability"`
	ITCAvailable Taxes `json:"itc_available"`
	ITCUtilised  Taxes `json:"itc_utilised"` // by credit head
	CashPayable  Taxes `json:"cash_payable"` // by liability head
	ITCCarryFwd  Taxes `json:"itc_carry_forward"`
}

// GSTR3B is the summary return.
type GSTR3B struct {
	Gstin      string `json:"gstin"`
	RetPeriod  string `json:"ret_period"`
	SupDetails struct {
		OsupDet     Head `json:"osup_det"`      // 3.1(a) outward taxable
		OsupZero    Head `json:"osup_zero"`     // 3.1(b) zero rated
		OsupNilExmp Head `json:"osup_nil_exmp"` // 3.1(c) nil/exempt
		IsupRev     Head `json:"isup_rev"`      // 3.1(d) inward reverse charge
		OsupNongst  Head `json:"osup_nongst"`   // 3.1(e) non-GST
	} `json:"sup_details"`
	InterSup struct {
		UnregDetails []UnregSupply `json:"unreg_details"` // 3.2
	} `json:"inter_sup"`
	ItcElg struct {
		ItcAvl []ITCRow `json:"itc_avl"`
		ItcRev []ITCRow `json:"itc_rev"`
		ItcNet Head     `json:"itc_net"`
	} `json:"itc_elg"`
	Computation Computation `json:"computation"`
}

// BuildGSTR3B summarises posted invoices: sales and credit notes for outward
// tax, purchases and debit notes (purchase returns) for ITC.
func BuildGSTR3B(gstin, period, sellerState string, invs []clients.Invoice) GSTR3B {
	var out, nil0, itc amounts
	unreg := map[string]*amounts{}
	for _, inv := range invs {
		if inv.Status != "posted" {
			continue
		}
		switch inv.DocType {
		case "sale", "sale_return":
			sign := int64(1)
			if inv.DocType == "sale_return" {
				sign = -1
			}
			for _, l := range inv.Lines {
				if l.TaxRateBP == 0 {
					nil0.add(l, sign)
				} else {
					out.add(l, sign)
				}
			}
			if inv.PartyGSTIN == "" && inv.PlaceOfSupply != "" && inv.PlaceOfSupply != sellerState {
				a := unreg[inv.PlaceOfSupply]
				if a == nil {
					a = &amounts{}
					unreg[inv.PlaceOfSupply] = a
				}
				for _, l := range inv.Lines {
					a.add(l, sign)
				}
			}
		case "purchase", "purchase_return":
			sign := int64(1)
			if inv.DocType == "purchase_return" {
				sign = -1
			}
			for _, l := range inv.Lines {
				itc.add(l, sign)
			}
		}
	}

	g := GSTR3B{Gstin: gstin, RetPeriod: period}
	g.SupDetails.OsupDet = out.head()
	g.SupDetails.OsupNilExmp = Head{Txval: compliance.Rupees(nil0.txval)}
	g.InterSup.UnregDetails = []UnregSupply{}
	for _, pos := range sortedKeys(unreg) {
		a := unreg[pos]
		g.InterSup.UnregDetails = append(g.InterSup.UnregDetails, UnregSupply{Pos: pos, Txval: compliance.Rupees(a.txval), Iamt: compliance.Rupees(a.iamt)})
	}
	itcHead := itc.head()
	itcHead.Txval = 0
	g.ItcElg.ItcAvl = []ITCRow{{Ty: "OTH", Head: itcHead}}
	g.ItcElg.ItcRev = []ITCRow{}
	g.ItcElg.ItcNet = itcHead

	g.Computation = setOff(
		[4]int64{out.iamt, out.camt, out.samt, out.csamt},
		[4]int64{max(itc.iamt, 0), max(itc.camt, 0), max(itc.samt, 0), max(itc.csamt, 0)},
	)
	return g
}

// setOff applies the statutory order of ITC utilisation (sec. 49 / rule 88A):
// IGST credit → IGST, then CGST, then SGST liability; CGST credit → CGST, then
// IGST; SGST credit → SGST, then IGST. CGST and SGST never cross. Cess → cess.
// Index order: 0 IGST, 1 CGST, 2 SGST, 3 cess.
func setOff(liab, cred [4]int64) Computation {
	l, c := liab, cred
	for i := range l {
		l[i] = max(l[i], 0)
	}
	var used [4]int64
	use := func(from, to int) {
		u := min(c[from], l[to])
		c[from] -= u
		l[to] -= u
		used[from] += u
	}
	use(0, 0)
	use(0, 1)
	use(0, 2)
	use(1, 1)
	use(1, 0)
	use(2, 2)
	use(2, 0)
	use(3, 3)
	t := func(v [4]int64) Taxes {
		r := compliance.Rupees
		return Taxes{IGST: r(v[0]), CGST: r(v[1]), SGST: r(v[2]), Cess: r(v[3])}
	}
	return Computation{Liability: t(liab), ITCAvailable: t(cred), ITCUtilised: t(used), CashPayable: t(l), ITCCarryFwd: t(c)}
}
