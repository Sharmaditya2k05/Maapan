package returns

import (
	"github.com/taraju/kar/internal/recon"
	"github.com/taraju/mool/gst"
)

// Head is a GSTR-3B row with taxable value and tax by head.
type Head struct {
	Txval Rs `json:"txval"`
	Iamt  Rs `json:"iamt"`
	Camt  Rs `json:"camt"`
	Samt  Rs `json:"samt"`
	Csamt Rs `json:"csamt"`
}

func (a amounts) head() Head {
	return Head{Txval: Rs(a.txval), Iamt: Rs(a.iamt), Camt: Rs(a.camt), Samt: Rs(a.samt), Csamt: Rs(a.csamt)}
}

// TaxHead is tax by head without a taxable value.
type TaxHead struct {
	Iamt  Rs `json:"iamt"`
	Camt  Rs `json:"camt"`
	Samt  Rs `json:"samt"`
	Csamt Rs `json:"csamt"`
}

func (a amounts) tax() TaxHead {
	return TaxHead{Iamt: Rs(a.iamt), Camt: Rs(a.camt), Samt: Rs(a.samt), Csamt: Rs(a.csamt)}
}

// ITCRow is one row of Table 4.
type ITCRow struct {
	Ty string `json:"ty"`
	TaxHead
}

// UnregSupply is a Table 3.2 row.
type UnregSupply struct {
	Pos   string `json:"pos"`
	Txval Rs     `json:"txval"`
	Iamt  Rs     `json:"iamt"`
}

// SupDetails is Table 3.1.
type SupDetails struct {
	OsupDet     Head `json:"osup_det"`      // (a) outward taxable (other than zero-rated, nil, exempt)
	OsupZero    Head `json:"osup_zero"`     // (b) zero-rated (exports, SEZ)
	OsupNilExmp Head `json:"osup_nil_exmp"` // (c) nil-rated and exempt
	IsupRev     Head `json:"isup_rev"`      // (d) inward supplies liable to reverse charge
	OsupNongst  Head `json:"osup_nongst"`   // (e) non-GST outward
}

// InterSup is Table 3.2.
type InterSup struct {
	UnregDetails []UnregSupply `json:"unreg_details"`
	CompDetails  []UnregSupply `json:"comp_details"`
	UinDetails   []UnregSupply `json:"uin_details"`
}

// ItcElg is Table 4.
type ItcElg struct {
	ItcAvl   []ITCRow `json:"itc_avl"`   // (A) IMPG, IMPS, ISRC (3), ISD, OTH (5)
	ItcRev   []ITCRow `json:"itc_rev"`   // (B) RUL (1) rules 38/42/43 and Sec 17(5), OTH (2)
	ItcNet   TaxHead  `json:"itc_net"`   // (C) = A − B
	ItcInelg []ITCRow `json:"itc_inelg"` // (D) RUL (1) reclaimed, OTH (2) Sec 16(4)/POS
}

// IsupRow is a Table 5 row.
type IsupRow struct {
	Ty    string `json:"ty"` // GST (composition, exempt, nil) | NONGST
	Inter Rs     `json:"inter"`
	Intra Rs     `json:"intra"`
}

// InwardSup is Table 5.
type InwardSup struct {
	IsupDetails []IsupRow `json:"isup_details"`
}

// ITCBooks breaks the books' purchase tax down by eligibility.
type ITCBooks struct {
	Eligible      TaxHead `json:"eligible"`
	Ineligible175 TaxHead `json:"ineligible_17_5"`
	IneligibleOth TaxHead `json:"ineligible_other"`
	ReverseCharge TaxHead `json:"reverse_charge"` // part of the above on RCM purchases
	// NoSupplierGSTIN is tax on forward-charge bills from suppliers without a
	// GSTIN. It is not claimable (Sec 16(2)(a) needs a registered supplier's
	// tax invoice), so it is left out of Table 4 and of the eligibility split.
	NoSupplierGSTIN TaxHead `json:"no_supplier_gstin"`
	NoGSTINBills    int     `json:"no_supplier_gstin_bills"`
	NetClaimed4C    TaxHead `json:"net_claimed"`
	PurchaseBills   int     `json:"purchase_bills"`
	PurchaseReturn  int     `json:"purchase_returns"`
}

// ITCComparison sets the books' ITC beside what GSTR-2B (after IMS) allows.
type ITCComparison struct {
	Books      Heads             `json:"books"`      // Table 4(C) from purchase bills
	GSTR2B     Heads             `json:"gstr2b"`     // accepted + deemed-accepted 2B documents
	Difference Heads             `json:"difference"` // books − 2B; positive = claiming more than 2B supports
	Detail     *recon.ITCSummary `json:"detail,omitempty"`
	Imported   bool              `json:"imported"`
	Note       string            `json:"note,omitempty"`
}

// Computation is the payable table (not part of the GSTN file).
type Computation struct {
	SetOff
	OpeningITC    Taxes `json:"opening_itc"`
	CurrentITC    Taxes `json:"current_itc"`         // Table 4(C), negative heads moved to liability
	ReverseCharge Taxes `json:"reverse_charge_cash"` // 3.1(d): payable in cash only (Sec 49(4))
	TotalCash     Taxes `json:"total_cash"`          // cash_payable + reverse_charge_cash
}

// Summary3B is for the UI.
type Summary3B struct {
	Documents  int  `json:"documents"`
	Excluded   int  `json:"excluded"`
	OutwardRCM Head `json:"outward_reverse_charge"` // our supplies taxed in the recipient's hands (not in 3.1)
}

// GSTR3B is the summary return in the GSTN 3B JSON structure plus the payable table.
type GSTR3B struct {
	Gstin       string         `json:"gstin"`
	RetPeriod   string         `json:"ret_period"`
	SupDetails  SupDetails     `json:"sup_details"`
	InterSup    InterSup       `json:"inter_sup"`
	ItcElg      ItcElg         `json:"itc_elg"`
	InwardSup   InwardSup      `json:"inward_sup"`
	Computation Computation    `json:"computation"`
	ITCBooks    ITCBooks       `json:"itc_books"`
	ITC2B       *ITCComparison `json:"itc_2b,omitempty"`
	Summary     Summary3B      `json:"summary"`
	Warnings    []string       `json:"warnings"`
}

// Portal returns the GSTN 3B JSON sections only.
func (g GSTR3B) Portal() map[string]any {
	return map[string]any{"gstin": g.Gstin, "ret_period": g.RetPeriod, "sup_details": g.SupDetails,
		"inter_sup": g.InterSup, "itc_elg": g.ItcElg, "inward_sup": g.InwardSup}
}

// GSTR3BBuilder accumulates a period.
type GSTR3BBuilder struct {
	gstin, period, state string

	osup, zero, nilExmp, rev, nongst, outRCM amounts
	unreg                                    map[string]*amounts
	isrc, oth, rul                           amounts
	elig, inelg175, inelgOth, rcmTax         amounts
	noGSTIN                                  amounts // forward-charge tax from unregistered suppliers
	noGSTINBills                             int
	inward                                   [2][2]int64 // [GST|NONGST][inter|intra]
	sum                                      Summary3B
	bills, returns                           int
	warn                                     warnings
}

// NewGSTR3B starts a build for gstin (state = its state code) and period MMYYYY.
func NewGSTR3B(gstin, period, state string) *GSTR3BBuilder {
	return &GSTR3BBuilder{gstin: gstin, period: period, state: state, unreg: map[string]*amounts{}, warn: newWarnings()}
}

// Add takes one document of any type.
func (b *GSTR3BBuilder) Add(inv Invoice) {
	if gst.Normalize(inv.StoreGSTIN) != b.gstin {
		b.sum.Excluded++
		return
	}
	if inv.Status != "posted" {
		return
	}
	b.sum.Documents++
	sign := int64(1)
	if inv.DocType == "sale_return" || inv.DocType == "purchase_return" {
		sign = -1
	}
	cat := inv.SupplyCategory
	switch inv.DocType {
	case "sale", "sale_return":
		pos := inv.PlaceOfSupply
		if pos == "" {
			pos = b.state
		}
		unregInter := gst.Normalize(inv.PartyGSTIN) == "" && !zeroRated(cat) && !inv.ReverseCharge && pos != b.state
		for _, l := range inv.Lines {
			kind, _ := lineKind(cat, l)
			switch {
			case kind == kindExempt || kind == kindNil:
				b.nilExmp.txval += sign * l.TaxablePaise
			case kind == kindNonGST:
				b.nongst.txval += sign * l.TaxablePaise
			case inv.ReverseCharge:
				b.outRCM.add(l, sign)
			case zeroRated(cat):
				b.zero.add(l, sign)
			default:
				b.osup.add(l, sign)
				if unregInter {
					a := b.unreg[pos]
					if a == nil {
						a = &amounts{}
						b.unreg[pos] = a
					}
					a.add(l, sign)
				}
			}
		}
	case "purchase", "purchase_return":
		if sign > 0 {
			b.bills++
		} else {
			b.returns++
		}
		inter := inv.InterState || inv.IGSTPaise != 0 || (inv.PartyState != "" && inv.PartyState != b.state)
		unregSupplier := !inv.ReverseCharge && gst.Normalize(inv.PartyGSTIN) == ""
		counted := false
		col := 1
		if inter {
			col = 0
		}
		for _, l := range inv.Lines {
			kind, _ := lineKind(cat, l)
			switch kind {
			case kindExempt, kindNil:
				b.inward[0][col] += sign * l.TaxablePaise
				continue
			case kindNonGST:
				b.inward[1][col] += sign * l.TaxablePaise
				continue
			}
			tax := l
			tax.TaxablePaise = 0
			if unregSupplier {
				if l.CGSTPaise != 0 || l.SGSTPaise != 0 || l.IGSTPaise != 0 || l.CessPaise != 0 {
					b.noGSTIN.add(tax, sign)
					if !counted {
						b.noGSTINBills++
						counted = true
					}
				}
				continue
			}
			if inv.ReverseCharge {
				b.rev.add(l, sign)
				b.isrc.add(tax, sign)
				b.rcmTax.add(tax, sign)
			} else {
				b.oth.add(tax, sign)
			}
			switch l.ITCEligibility {
			case "", "eligible":
				b.elig.add(tax, sign)
			case "ineligible_17_5":
				b.inelg175.add(tax, sign)
				b.rul.add(tax, sign)
			default:
				b.inelgOth.add(tax, sign)
				b.rul.add(tax, sign)
			}
		}
	}
}

// Build finishes the return. opening is the electronic credit ledger balance
// brought in; twoB (optional) is the ITC GSTR-2B allows after IMS actions.
func (b *GSTR3BBuilder) Build(opening Taxes, twoB *recon.ITCSummary) GSTR3B {
	g := GSTR3B{Gstin: b.gstin, RetPeriod: b.period}
	g.SupDetails = SupDetails{OsupDet: b.osup.head(), OsupZero: b.zero.head(), OsupNilExmp: Head{Txval: Rs(b.nilExmp.txval)},
		IsupRev: b.rev.head(), OsupNongst: Head{Txval: Rs(b.nongst.txval)}}
	g.InterSup = InterSup{UnregDetails: []UnregSupply{}, CompDetails: []UnregSupply{}, UinDetails: []UnregSupply{}}
	for _, pos := range sortedKeys(b.unreg) {
		a := b.unreg[pos]
		if a.txval == 0 && a.iamt == 0 {
			continue
		}
		g.InterSup.UnregDetails = append(g.InterSup.UnregDetails, UnregSupply{Pos: pos, Txval: Rs(a.txval), Iamt: Rs(a.iamt)})
	}
	var net amounts
	net.iamt = b.isrc.iamt + b.oth.iamt - b.rul.iamt
	net.camt = b.isrc.camt + b.oth.camt - b.rul.camt
	net.samt = b.isrc.samt + b.oth.samt - b.rul.samt
	net.csamt = b.isrc.csamt + b.oth.csamt - b.rul.csamt
	g.ItcElg = ItcElg{
		ItcAvl: []ITCRow{{Ty: "IMPG"}, {Ty: "IMPS"}, {Ty: "ISRC", TaxHead: b.isrc.tax()}, {Ty: "ISD"}, {Ty: "OTH", TaxHead: b.oth.tax()}},
		ItcRev: []ITCRow{{Ty: "RUL", TaxHead: b.rul.tax()}, {Ty: "OTH"}},
		ItcNet: net.tax(),
		// 4(D): 16(4)/POS-restricted credit and reclaims are not tracked per bill yet.
		ItcInelg: []ITCRow{{Ty: "RUL"}, {Ty: "OTH"}},
	}
	g.InwardSup = InwardSup{IsupDetails: []IsupRow{
		{Ty: "GST", Inter: Rs(b.inward[0][0]), Intra: Rs(b.inward[0][1])},
		{Ty: "NONGST", Inter: Rs(b.inward[1][0]), Intra: Rs(b.inward[1][1])},
	}}
	g.ITCBooks = ITCBooks{Eligible: b.elig.tax(), Ineligible175: b.inelg175.tax(), IneligibleOth: b.inelgOth.tax(),
		ReverseCharge: b.rcmTax.tax(), NetClaimed4C: net.tax(), PurchaseBills: b.bills, PurchaseReturn: b.returns,
		NoSupplierGSTIN: b.noGSTIN.tax(), NoGSTINBills: b.noGSTINBills}
	if b.noGSTINBills > 0 {
		t := b.noGSTIN
		b.warn.add("no-gstin", "%d purchase document(s) from suppliers without a GSTIN carry tax of ₹%s; no ITC is claimed on it (a registered supplier's tax invoice is required). Add the supplier's GSTIN if they are registered, or record the bill without GST",
			b.noGSTINBills, Rs(t.iamt+t.camt+t.samt+t.csamt))
	}

	// Set-off: forward-charge output tax (3.1(a) + 3.1(b) with payment) against
	// opening + current ITC. Reverse-charge tax (3.1(d)) is cash only.
	liab := [4]int64{b.osup.iamt + b.zero.iamt, b.osup.camt + b.zero.camt, b.osup.samt + b.zero.samt, b.osup.csamt + b.zero.csamt}
	for h, name := range headName {
		if liab[h] < 0 {
			b.warn.add("neg-liab", "%s output tax is negative this period (credit notes exceed sales); taken as zero — carry the excess to the next return", name)
			liab[h] = 0
		}
	}
	cur := [4]int64{net.iamt, net.camt, net.samt, net.csamt}
	for h, name := range headName {
		if cur[h] < 0 {
			b.warn.add("neg-itc", "net %s ITC is negative (purchase returns/reversals exceed credit); the excess is added to the %s liability", name, name)
			liab[h] -= cur[h]
			cur[h] = 0
		}
	}
	open := [4]int64{int64(opening.IGST), int64(opening.CGST), int64(opening.SGST), int64(opening.Cess)}
	var cred [4]int64
	for h := range cred {
		cred[h] = cur[h] + max(open[h], 0)
	}
	so := setOff(liab, cred)
	rcm := [4]int64{max(b.rev.iamt, 0), max(b.rev.camt, 0), max(b.rev.samt, 0), max(b.rev.csamt, 0)}
	cash := [4]int64{int64(so.CashPayable.IGST) + rcm[0], int64(so.CashPayable.CGST) + rcm[1], int64(so.CashPayable.SGST) + rcm[2], int64(so.CashPayable.Cess) + rcm[3]}
	g.Computation = Computation{SetOff: so, OpeningITC: taxes(open), CurrentITC: taxes(cur), ReverseCharge: taxes(rcm), TotalCash: taxes(cash)}

	if b.outRCM != (amounts{}) {
		b.warn.add("out-rcm", "outward supplies under reverse charge (taxable %s) are reported in GSTR-1 with rchrg=Y; the recipient pays that tax, so it is not in Table 3.1 or the set-off", Rs(b.outRCM.txval))
	}
	if b.inelg175 != (amounts{}) || b.inelgOth != (amounts{}) {
		b.warn.add("blocked", "ITC on purchase lines marked ineligible is shown in 4(A) and reversed in 4(B)(1), so it is never claimed (4(C) excludes it)")
	}

	books := Heads{IGST: Rs(net.iamt), CGST: Rs(net.camt), SGST: Rs(net.samt), Cess: Rs(net.csamt)}
	if twoB != nil {
		d := *twoB
		g.ITC2B = &ITCComparison{Books: books, GSTR2B: d.Available, Difference: books.Add(d.Available, -1), Detail: &d, Imported: true}
		if diff := g.ITC2B.Difference; diff.IGST > 0 || diff.CGST > 0 || diff.SGST > 0 || diff.Cess > 0 {
			g.ITC2B.Note = "the books claim more ITC than GSTR-2B supports for this period; Sec 16(2)(aa) allows only credit reflected in 2B — check the GSTR-2B reconciliation"
			b.warn.add("2b", "%s", g.ITC2B.Note)
		}
	} else {
		g.ITC2B = &ITCComparison{Books: books, Imported: false, Note: "no GSTR-2B imported for this period; import it to compare the books' ITC with the portal"}
	}
	g.Summary = b.sum
	g.Summary.OutwardRCM = b.outRCM.head()
	g.Warnings = b.warn.list()
	return g
}
