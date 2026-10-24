// Package returns builds GSTR-1 and GSTR-3B per GSTIN from aadesh invoices in
// the GSTN offline-tool JSON structure, computes the GSTR-3B ITC set-off
// (Rule 88A) and serves the returns, GSTR-2B and IMS routes. Amounts are
// paise throughout and become rupees only when JSON is written (recon.Rs).
package returns

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/taraju/kar/internal/recon"
	"github.com/taraju/mool/gst"
)

// GSTNVersion is the offline-tool schema version written into GSTR-1 JSON.
const GSTNVersion = "GST3.2.4"

// DefaultB2CLThreshold is the invoice value above which an inter-state sale to
// an unregistered buyer is reported invoice-wise (₹1,00,000 since Aug 2024).
const DefaultB2CLThreshold int64 = 10000000

// Rs is paise written as rupees.
type Rs = recon.Rs

// Heads is tax by head.
type Heads = recon.Heads

// Period parses MMYYYY into its first and last day (YYYY-MM-DD).
func Period(p string) (from, to string, err error) { return recon.ParsePeriod(p) }

// ---------- GSTN offline-tool structures ----------

// ItmDet is the rate-wise detail of a document.
type ItmDet struct {
	Rt    float64 `json:"rt"`
	Txval Rs      `json:"txval"`
	Iamt  Rs      `json:"iamt"`
	Camt  Rs      `json:"camt"`
	Samt  Rs      `json:"samt"`
	Csamt Rs      `json:"csamt"`
}

// Itm numbers a rate-wise detail.
type Itm struct {
	Num    int    `json:"num"`
	ItmDet ItmDet `json:"itm_det"`
}

// B2BInv is a Table 4A/4B/6B/6C invoice.
type B2BInv struct {
	Inum   string `json:"inum"`
	Idt    string `json:"idt"`
	Val    Rs     `json:"val"`
	Pos    string `json:"pos"`
	Rchrg  string `json:"rchrg"`   // Y | N
	InvTyp string `json:"inv_typ"` // R | SEWP | SEWOP | DE
	Itms   []Itm  `json:"itms"`
	iso    string
}

// B2B groups invoices by recipient GSTIN.
type B2B struct {
	Ctin string   `json:"ctin"`
	Inv  []B2BInv `json:"inv"`
}

// B2CLInv is a Table 5 invoice.
type B2CLInv struct {
	Inum string `json:"inum"`
	Idt  string `json:"idt"`
	Val  Rs     `json:"val"`
	Itms []Itm  `json:"itms"`
	iso  string
}

// B2CL groups large inter-state B2C invoices by place of supply.
type B2CL struct {
	Pos string    `json:"pos"`
	Inv []B2CLInv `json:"inv"`
}

// B2CS is a Table 7 row (by place of supply and rate).
type B2CS struct {
	SplyTy string  `json:"sply_ty"` // INTRA | INTER
	Pos    string  `json:"pos"`
	Typ    string  `json:"typ"` // OE (not through an e-commerce operator)
	Rt     float64 `json:"rt"`
	Txval  Rs      `json:"txval"`
	Iamt   Rs      `json:"iamt"`
	Camt   Rs      `json:"camt"`
	Samt   Rs      `json:"samt"`
	Csamt  Rs      `json:"csamt"`
}

// Note is a Table 9B credit/debit note to a registered recipient.
type Note struct {
	Ntty   string `json:"ntty"` // C | D
	NtNum  string `json:"nt_num"`
	NtDt   string `json:"nt_dt"`
	Val    Rs     `json:"val"`
	Pos    string `json:"pos"`
	Rchrg  string `json:"rchrg"`
	InvTyp string `json:"inv_typ"`
	Itms   []Itm  `json:"itms"`
	iso    string
}

// CDNR groups notes by recipient GSTIN.
type CDNR struct {
	Ctin string `json:"ctin"`
	Nt   []Note `json:"nt"`
}

// CDNUR is a Table 9B note to an unregistered recipient (against a B2CL or export invoice).
type CDNUR struct {
	Typ   string `json:"typ"` // B2CL | EXPWP | EXPWOP
	Ntty  string `json:"ntty"`
	NtNum string `json:"nt_num"`
	NtDt  string `json:"nt_dt"`
	Val   Rs     `json:"val"`
	Pos   string `json:"pos,omitempty"`
	Itms  []Itm  `json:"itms"`
	iso   string
}

// ExpItm is the rate-wise detail of an export invoice.
type ExpItm struct {
	Txval Rs      `json:"txval"`
	Rt    float64 `json:"rt"`
	Iamt  Rs      `json:"iamt"`
	Csamt Rs      `json:"csamt"`
}

// ExpInv is a Table 6A invoice.
type ExpInv struct {
	Inum    string   `json:"inum"`
	Idt     string   `json:"idt"`
	Val     Rs       `json:"val"`
	Sbpcode string   `json:"sbpcode,omitempty"`
	Sbnum   string   `json:"sbnum,omitempty"`
	Sbdt    string   `json:"sbdt,omitempty"`
	Itms    []ExpItm `json:"itms"`
	iso     string
}

// EXP groups exports by type.
type EXP struct {
	ExpTyp string   `json:"exp_typ"` // WPAY | WOPAY
	Inv    []ExpInv `json:"inv"`
}

// NilRow is a Table 8 row.
type NilRow struct {
	SplyTy   string `json:"sply_ty"` // INTRB2B | INTRAB2B | INTRB2C | INTRAB2C
	ExptAmt  Rs     `json:"expt_amt"`
	NilAmt   Rs     `json:"nil_amt"`
	NgsupAmt Rs     `json:"ngsup_amt"`
}

// NilSup is Table 8.
type NilSup struct {
	Inv []NilRow `json:"inv"`
}

// HSNRow is a Table 12 row.
type HSNRow struct {
	Num   int     `json:"num"`
	HsnSc string  `json:"hsn_sc"`
	Desc  string  `json:"desc"`
	Uqc   string  `json:"uqc"`
	Qty   float64 `json:"qty"`
	Rt    float64 `json:"rt"`
	Txval Rs      `json:"txval"`
	Iamt  Rs      `json:"iamt"`
	Camt  Rs      `json:"camt"`
	Samt  Rs      `json:"samt"`
	Csamt Rs      `json:"csamt"`
}

// HSN is Table 12, split by supplies to registered (B2B) and unregistered (B2C, incl. exports) recipients.
type HSN struct {
	B2B []HSNRow `json:"hsn_b2b"`
	B2C []HSNRow `json:"hsn_b2c"`
}

// DocRange is one number series in Table 13.
type DocRange struct {
	Num      int    `json:"num"`
	From     string `json:"from"`
	To       string `json:"to"`
	Totnum   int    `json:"totnum"`
	Cancel   int    `json:"cancel"`
	NetIssue int    `json:"net_issue"`
}

// DocDet is one document type in Table 13.
type DocDet struct {
	DocNum int        `json:"doc_num"` // 1 invoices for outward supply, 5 credit notes
	DocTyp string     `json:"doc_typ"`
	Docs   []DocRange `json:"docs"`
}

// DocIssue is Table 13.
type DocIssue struct {
	DocDet []DocDet `json:"doc_det"`
}

// SectionTotal sums one section for the UI.
type SectionTotal struct {
	Records int `json:"records"`
	Taxable Rs  `json:"taxable"`
	Heads
}

// Summary is for the UI (not part of the GSTN file).
type Summary struct {
	Sections  map[string]*SectionTotal `json:"sections"`
	Documents int                      `json:"documents"` // posted documents included
	Cancelled int                      `json:"cancelled"`
	Excluded  int                      `json:"excluded"` // documents of another GSTIN or without store GSTIN
	// Liability is the outward tax payable by us (credit notes deducted;
	// reverse-charge supplies excluded because the recipient pays).
	Liability Heads `json:"liability"`
}

// GSTR1 is the return in the GSTN offline-tool structure plus summary and warnings.
type GSTR1 struct {
	Gstin    string   `json:"gstin"`
	Fp       string   `json:"fp"`
	Version  string   `json:"version"`
	Hash     string   `json:"hash"`
	B2B      []B2B    `json:"b2b"`
	B2CL     []B2CL   `json:"b2cl"`
	B2CS     []B2CS   `json:"b2cs"`
	CDNR     []CDNR   `json:"cdnr"`
	CDNUR    []CDNUR  `json:"cdnur"`
	EXP      []EXP    `json:"exp"`
	Nil      NilSup   `json:"nil"`
	HSN      HSN      `json:"hsn"`
	DocIssue DocIssue `json:"doc_issue"`
	Summary  *Summary `json:"summary,omitempty"`
	Warnings []string `json:"warnings"`
}

// Portal returns the file to upload to the offline tool / portal: GSTN
// sections only, empty sections left out.
func (g GSTR1) Portal() map[string]any {
	m := map[string]any{"gstin": g.Gstin, "fp": g.Fp, "version": g.Version, "hash": g.Hash}
	put := func(k string, n int, v any) {
		if n > 0 {
			m[k] = v
		}
	}
	put("b2b", len(g.B2B), g.B2B)
	put("b2cl", len(g.B2CL), g.B2CL)
	put("b2cs", len(g.B2CS), g.B2CS)
	put("cdnr", len(g.CDNR), g.CDNR)
	put("cdnur", len(g.CDNUR), g.CDNUR)
	put("exp", len(g.EXP), g.EXP)
	put("nil", len(g.Nil.Inv), g.Nil)
	if len(g.HSN.B2B)+len(g.HSN.B2C) > 0 {
		h := map[string]any{}
		put2 := func(k string, rows []HSNRow) {
			if len(rows) > 0 {
				h[k] = rows
			}
		}
		put2("hsn_b2b", g.HSN.B2B)
		put2("hsn_b2c", g.HSN.B2C)
		m["hsn"] = h
	}
	put("doc_issue", len(g.DocIssue.DocDet), g.DocIssue)
	return m
}

// ---------- builder ----------

// GSTR1Config drives a GSTR-1 build.
type GSTR1Config struct {
	GSTIN         string // our registration
	FP            string // MMYYYY
	State         string // registration state code
	B2CLThreshold int64  // paise; 0 = DefaultB2CLThreshold
	// Original loads the invoice a credit note refers to, to tell a CDNUR
	// (against a B2CL invoice) from a B2CS adjustment. Nil → judged by the note.
	Original func(ctx context.Context, id string) (Invoice, error)
}

type amounts struct{ txval, iamt, camt, samt, csamt int64 }

func (a *amounts) add(l Line, sign int64) {
	a.txval += sign * l.TaxablePaise
	a.iamt += sign * l.IGSTPaise
	a.camt += sign * l.CGSTPaise
	a.samt += sign * l.SGSTPaise
	a.csamt += sign * l.CessPaise
}

func (a amounts) det(rateBP int) ItmDet {
	return ItmDet{Rt: rate(rateBP), Txval: Rs(a.txval), Iamt: Rs(a.iamt), Camt: Rs(a.camt), Samt: Rs(a.samt), Csamt: Rs(a.csamt)}
}

func (a amounts) heads() Heads {
	return Heads{IGST: Rs(a.iamt), CGST: Rs(a.camt), SGST: Rs(a.samt), Cess: Rs(a.csamt)}
}

func rate(bp int) float64 { return float64(bp) / 100 }

type csKey struct {
	pos  string
	rate int
}

type hsnKey struct {
	hsn, uqc string
	rate     int
}

type hsnAcc struct {
	amounts
	desc     string
	qtyMilli int64
}

type series struct {
	docNum           int
	prefix           string
	numeric          bool
	minN, maxN       int64
	minS, maxS       string
	total, cancelled int
}

// GSTR1Builder accumulates a period invoice by invoice.
type GSTR1Builder struct {
	cfg    GSTR1Config
	b2b    map[string]*B2B
	b2cl   map[string]*B2CL
	cdnr   map[string]*CDNR
	cdnur  []CDNUR
	exp    map[string]*EXP
	b2cs   map[csKey]*amounts
	nilAcc map[string]*[3]int64 // expt, nil, ngsup
	hsn    [2]map[hsnKey]*hsnAcc
	docs   map[string]*series
	sum    Summary
	liab   amounts
	warn   warnings
}

// NewGSTR1 starts a build.
func NewGSTR1(cfg GSTR1Config) *GSTR1Builder {
	if cfg.B2CLThreshold <= 0 {
		cfg.B2CLThreshold = DefaultB2CLThreshold
	}
	return &GSTR1Builder{cfg: cfg, b2b: map[string]*B2B{}, b2cl: map[string]*B2CL{}, cdnr: map[string]*CDNR{},
		exp: map[string]*EXP{}, b2cs: map[csKey]*amounts{}, nilAcc: map[string]*[3]int64{},
		hsn: [2]map[hsnKey]*hsnAcc{{}, {}}, docs: map[string]*series{},
		sum: Summary{Sections: map[string]*SectionTotal{}}, warn: newWarnings()}
}

// Line kinds.
const (
	kindTaxed = iota
	kindExempt
	kindNil
	kindNonGST
)

func isExport(cat string) bool {
	return cat == gst.SupplyExportWithPayment || cat == gst.SupplyExportWithoutPayment
}

func isSEZ(cat string) bool {
	return cat == gst.SupplySEZWithPayment || cat == gst.SupplySEZWithoutPayment
}

func zeroRated(cat string) bool { return isExport(cat) || isSEZ(cat) }

// lineKind sorts a line into the taxed sections or Table 8. A 0 % line still
// marked taxable on a domestic supply (bills made before tax_category existed)
// is treated as nil-rated.
func lineKind(cat string, l Line) (kind int, legacyNil bool) {
	switch l.TaxCategory {
	case gst.CatExempt:
		return kindExempt, false
	case gst.CatNilRated:
		return kindNil, false
	case gst.CatNonGST:
		return kindNonGST, false
	}
	if l.TaxRateBP == 0 && l.CessPaise == 0 && !zeroRated(cat) {
		return kindNil, true
	}
	return kindTaxed, false
}

func invTyp(cat string) string {
	switch cat {
	case gst.SupplySEZWithPayment:
		return "SEWP"
	case gst.SupplySEZWithoutPayment:
		return "SEWOP"
	case gst.SupplyDeemedExport:
		return "DE"
	}
	return "R"
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// GSTNDate turns YYYY-MM-DD into dd-mm-yyyy.
func GSTNDate(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return t.Format("02-01-2006")
}

func (b *GSTR1Builder) section(name string) *SectionTotal {
	s := b.sum.Sections[name]
	if s == nil {
		s = &SectionTotal{}
		b.sum.Sections[name] = s
	}
	return s
}

func (b *GSTR1Builder) tally(name string, lines []Line) {
	s := b.section(name)
	s.Records++
	for _, l := range lines {
		s.Taxable += Rs(l.TaxablePaise)
		s.Heads = s.Heads.Add(Heads{IGST: Rs(l.IGSTPaise), CGST: Rs(l.CGSTPaise), SGST: Rs(l.SGSTPaise), Cess: Rs(l.CessPaise)}, 1)
	}
}

// Add classifies one document. Purchases are ignored.
func (b *GSTR1Builder) Add(ctx context.Context, inv Invoice) error {
	if inv.DocType != "sale" && inv.DocType != "sale_return" {
		return nil
	}
	if gst.Normalize(inv.StoreGSTIN) != b.cfg.GSTIN {
		b.sum.Excluded++
		return nil
	}
	b.trackDoc(inv)
	if inv.Status != "posted" {
		if inv.Status == "cancelled" {
			b.sum.Cancelled++
		}
		return nil
	}
	b.sum.Documents++
	state := b.cfg.State
	cat := inv.SupplyCategory
	pos := inv.PlaceOfSupply
	if pos == "" {
		pos = state
	}
	ctin := gst.Normalize(inv.PartyGSTIN)
	reg := ctin != ""
	if (isSEZ(cat) || cat == gst.SupplyDeemedExport) && !reg {
		b.warn.add("sez", "%s is a %s supply without the recipient's GSTIN; reported as a B2C supply — add the GSTIN to report it in B2B", inv.Number, cat)
	}
	inter := pos != state || isExport(cat) || isSEZ(cat)
	sign := int64(1)
	note := inv.DocType == "sale_return"
	if note {
		sign = -1
	}

	var taxed []Line
	for _, l := range inv.Lines {
		kind, legacy := lineKind(cat, l)
		if legacy {
			b.warn.add("legacy-nil", "%s line %d is 0%% but not marked nil-rated/exempt; reported as nil-rated in Table 8", inv.Number, l.LineNo)
		}
		b.addHSN(inv, l, reg && !isExport(cat), sign)
		if kind == kindTaxed {
			taxed = append(taxed, l)
			continue
		}
		b.addNil(inter, reg && !isExport(cat), kind, l.TaxablePaise*sign)
	}
	if len(taxed) == 0 {
		return nil
	}
	if !inv.ReverseCharge {
		for _, l := range taxed {
			b.liab.add(l, sign)
		}
	}
	val := Rs(inv.GrandTotalPaise)
	date := GSTNDate(inv.InvoiceDate)
	switch {
	case !note && isExport(cat):
		typ := "WOPAY"
		if cat == gst.SupplyExportWithPayment {
			typ = "WPAY"
		}
		e := b.exp[typ]
		if e == nil {
			e = &EXP{ExpTyp: typ}
			b.exp[typ] = e
		}
		ei := ExpInv{Inum: inv.Number, Idt: date, Val: val, Sbpcode: inv.PortCode, Sbnum: inv.ShippingBillNo, Itms: expItems(taxed), iso: inv.InvoiceDate}
		if inv.ShippingBillDate != "" {
			ei.Sbdt = GSTNDate(inv.ShippingBillDate)
		}
		if inv.ShippingBillNo == "" {
			b.warn.add("sb", "export invoice %s has no shipping bill number/port code; complete them in the offline tool before filing (or file them later by amendment)", inv.Number)
		}
		e.Inv = append(e.Inv, ei)
		b.tally("exp", taxed)
	case !note && reg:
		e := b.b2b[ctin]
		if e == nil {
			e = &B2B{Ctin: ctin}
			b.b2b[ctin] = e
		}
		e.Inv = append(e.Inv, B2BInv{Inum: inv.Number, Idt: date, Val: val, Pos: pos, Rchrg: yn(inv.ReverseCharge),
			InvTyp: invTyp(cat), Itms: rateItems(taxed), iso: inv.InvoiceDate})
		b.tally("b2b", taxed)
	case !note && inter && inv.GrandTotalPaise > b.cfg.B2CLThreshold:
		e := b.b2cl[pos]
		if e == nil {
			e = &B2CL{Pos: pos}
			b.b2cl[pos] = e
		}
		e.Inv = append(e.Inv, B2CLInv{Inum: inv.Number, Idt: date, Val: val, Itms: rateItems(taxed), iso: inv.InvoiceDate})
		b.tally("b2cl", taxed)
	case note && reg:
		e := b.cdnr[ctin]
		if e == nil {
			e = &CDNR{Ctin: ctin}
			b.cdnr[ctin] = e
		}
		e.Nt = append(e.Nt, Note{Ntty: "C", NtNum: inv.Number, NtDt: date, Val: val, Pos: pos, Rchrg: yn(inv.ReverseCharge),
			InvTyp: invTyp(cat), Itms: rateItems(taxed), iso: inv.InvoiceDate})
		b.tally("cdnr", taxed)
	case note && isExport(cat):
		typ := "EXPWOP"
		if cat == gst.SupplyExportWithPayment {
			typ = "EXPWP"
		}
		b.cdnur = append(b.cdnur, CDNUR{Typ: typ, Ntty: "C", NtNum: inv.Number, NtDt: date, Val: val, Itms: rateItems(taxed), iso: inv.InvoiceDate})
		b.tally("cdnur", taxed)
	case note && inter && b.againstB2CL(ctx, inv):
		b.cdnur = append(b.cdnur, CDNUR{Typ: "B2CL", Ntty: "C", NtNum: inv.Number, NtDt: date, Val: val, Pos: pos,
			Itms: rateItems(taxed), iso: inv.InvoiceDate})
		b.tally("cdnur", taxed)
	default:
		// B2C small: invoices and their credit notes net by place of supply and rate.
		for _, l := range taxed {
			k := csKey{pos, l.TaxRateBP}
			a := b.b2cs[k]
			if a == nil {
				a = &amounts{}
				b.b2cs[k] = a
			}
			a.add(l, sign)
		}
		name := "b2cs"
		if note {
			name = "b2cs_credit_notes"
		}
		b.tally(name, taxed)
	}
	return nil
}

// againstB2CL reports whether an unregistered inter-state credit note refers to
// an invoice that was reported in B2CL.
func (b *GSTR1Builder) againstB2CL(ctx context.Context, n Invoice) bool {
	if n.RefInvoiceID != "" && b.cfg.Original != nil {
		orig, err := b.cfg.Original(ctx, n.RefInvoiceID)
		if err == nil {
			return orig.GrandTotalPaise > b.cfg.B2CLThreshold
		}
		b.warn.add("orig", "could not load the original invoice of credit note %s (%v); classified by the note's own value", n.Number, err)
	}
	return n.GrandTotalPaise > b.cfg.B2CLThreshold
}

func (b *GSTR1Builder) addNil(inter, reg bool, kind int, paise int64) {
	k := "INTRA"
	if inter {
		k = "INTR"
	}
	if reg {
		k += "B2B"
	} else {
		k += "B2C"
	}
	a := b.nilAcc[k]
	if a == nil {
		a = &[3]int64{}
		b.nilAcc[k] = a
	}
	switch kind {
	case kindExempt:
		a[0] += paise
	case kindNil:
		a[1] += paise
	case kindNonGST:
		a[2] += paise
	}
}

func (b *GSTR1Builder) addHSN(inv Invoice, l Line, b2b bool, sign int64) {
	code := strings.ReplaceAll(strings.TrimSpace(l.HSN), " ", "")
	switch {
	case code == "":
		b.warn.add("hsn", "%s line %d has no HSN/SAC; reported under 'NA' — fix the item master", inv.Number, l.LineNo)
		code = "NA"
	case len(code) < 4:
		b.warn.add("hsn4", "%s line %d: HSN %s has fewer than 4 digits (GSTR-1 needs 4, or 6 above ₹5 crore turnover)", inv.Number, l.LineNo, code)
	}
	r := l.TaxRateBP
	if k, _ := lineKind(inv.SupplyCategory, l); k != kindTaxed {
		r = 0
	}
	service := strings.HasPrefix(code, "99")
	u := UQC(l.Unit)
	if service {
		u = "NA"
	}
	key := hsnKey{code, u, r}
	i := 1
	if b2b {
		i = 0
	}
	a := b.hsn[i][key]
	if a == nil {
		a = &hsnAcc{desc: truncate(l.Name, 30)}
		b.hsn[i][key] = a
	}
	a.add(l, sign)
	if !service {
		a.qtyMilli += sign * int64(l.Qty*1000+0.5)
	}
}

// splitNumber splits "INV/26-27/00042" into the series "INV/26-27/" and 42.
func splitNumber(num string) (prefix string, n int64, ok bool) {
	i := len(num)
	for i > 0 && num[i-1] >= '0' && num[i-1] <= '9' {
		i--
	}
	if i == len(num) {
		return num, 0, false
	}
	v, err := strconv.ParseInt(num[i:], 10, 64)
	if err != nil {
		return num, 0, false
	}
	return num[:i], v, true
}

func (b *GSTR1Builder) trackDoc(inv Invoice) {
	if inv.Status != "posted" && inv.Status != "cancelled" {
		return
	}
	docNum := 1
	if inv.DocType == "sale_return" {
		docNum = 5
	}
	prefix, n, ok := splitNumber(inv.Number)
	sk := fmt.Sprintf("%d|%s|%t", docNum, prefix, ok)
	s := b.docs[sk]
	if s == nil {
		s = &series{docNum: docNum, prefix: prefix, numeric: ok, minN: n, maxN: n, minS: inv.Number, maxS: inv.Number}
		b.docs[sk] = s
	}
	s.total++
	if inv.Status == "cancelled" {
		s.cancelled++
	}
	if !ok {
		if inv.Number < s.minS {
			s.minS = inv.Number
		}
		if inv.Number > s.maxS {
			s.maxS = inv.Number
		}
		return
	}
	if n < s.minN {
		s.minN, s.minS = n, inv.Number
	}
	if n > s.maxN {
		s.maxN, s.maxS = n, inv.Number
	}
}

func rateItems(lines []Line) []Itm {
	byRate := map[int]*amounts{}
	for _, l := range lines {
		a := byRate[l.TaxRateBP]
		if a == nil {
			a = &amounts{}
			byRate[l.TaxRateBP] = a
		}
		a.add(l, 1)
	}
	rates := sortedInts(byRate)
	out := make([]Itm, 0, len(rates))
	for i, r := range rates {
		out = append(out, Itm{Num: i + 1, ItmDet: byRate[r].det(r)})
	}
	return out
}

func expItems(lines []Line) []ExpItm {
	out := []ExpItm{}
	for _, it := range rateItems(lines) {
		d := it.ItmDet
		out = append(out, ExpItm{Txval: d.Txval, Rt: d.Rt, Iamt: d.Iamt, Csamt: d.Csamt})
	}
	return out
}

// Build finishes the return.
func (b *GSTR1Builder) Build() GSTR1 {
	g := GSTR1{Gstin: b.cfg.GSTIN, Fp: b.cfg.FP, Version: GSTNVersion, Hash: "hash",
		B2B: []B2B{}, B2CL: []B2CL{}, B2CS: []B2CS{}, CDNR: []CDNR{}, CDNUR: []CDNUR{}, EXP: []EXP{},
		Nil: NilSup{Inv: []NilRow{}}, HSN: HSN{B2B: []HSNRow{}, B2C: []HSNRow{}}, DocIssue: DocIssue{DocDet: []DocDet{}}}

	for _, k := range sortedKeys(b.b2b) {
		e := *b.b2b[k]
		sort.SliceStable(e.Inv, func(i, j int) bool { return lessDoc(e.Inv[i].iso, e.Inv[i].Inum, e.Inv[j].iso, e.Inv[j].Inum) })
		g.B2B = append(g.B2B, e)
	}
	for _, k := range sortedKeys(b.b2cl) {
		e := *b.b2cl[k]
		sort.SliceStable(e.Inv, func(i, j int) bool { return lessDoc(e.Inv[i].iso, e.Inv[i].Inum, e.Inv[j].iso, e.Inv[j].Inum) })
		g.B2CL = append(g.B2CL, e)
	}
	for _, k := range sortedKeys(b.cdnr) {
		e := *b.cdnr[k]
		sort.SliceStable(e.Nt, func(i, j int) bool { return lessDoc(e.Nt[i].iso, e.Nt[i].NtNum, e.Nt[j].iso, e.Nt[j].NtNum) })
		g.CDNR = append(g.CDNR, e)
	}
	g.CDNUR = append(g.CDNUR, b.cdnur...)
	sort.SliceStable(g.CDNUR, func(i, j int) bool {
		return lessDoc(g.CDNUR[i].iso, g.CDNUR[i].NtNum, g.CDNUR[j].iso, g.CDNUR[j].NtNum)
	})
	for _, k := range sortedKeys(b.exp) {
		e := *b.exp[k]
		sort.SliceStable(e.Inv, func(i, j int) bool { return lessDoc(e.Inv[i].iso, e.Inv[i].Inum, e.Inv[j].iso, e.Inv[j].Inum) })
		g.EXP = append(g.EXP, e)
	}

	csKeys := make([]csKey, 0, len(b.b2cs))
	for k := range b.b2cs {
		csKeys = append(csKeys, k)
	}
	sort.Slice(csKeys, func(i, j int) bool {
		if csKeys[i].pos != csKeys[j].pos {
			return csKeys[i].pos < csKeys[j].pos
		}
		return csKeys[i].rate < csKeys[j].rate
	})
	for _, k := range csKeys {
		a := *b.b2cs[k]
		if a == (amounts{}) {
			continue
		}
		if a.txval < 0 {
			b.warn.add("b2cs-neg", "B2CS for place of supply %s at %s%% is negative (credit notes exceed sales); the portal rejects negative B2CS — adjust it against the original month", k.pos, strconv.FormatFloat(rate(k.rate), 'f', -1, 64))
		}
		d := a.det(k.rate)
		sply := "INTRA"
		if k.pos != b.cfg.State {
			sply = "INTER"
		}
		g.B2CS = append(g.B2CS, B2CS{SplyTy: sply, Pos: k.pos, Typ: "OE", Rt: d.Rt, Txval: d.Txval,
			Iamt: d.Iamt, Camt: d.Camt, Samt: d.Samt, Csamt: d.Csamt})
	}
	for _, k := range []string{"INTRB2B", "INTRAB2B", "INTRB2C", "INTRAB2C"} {
		if a := b.nilAcc[k]; a != nil && *a != ([3]int64{}) {
			g.Nil.Inv = append(g.Nil.Inv, NilRow{SplyTy: k, ExptAmt: Rs(a[0]), NilAmt: Rs(a[1]), NgsupAmt: Rs(a[2])})
		}
	}
	g.HSN.B2B = hsnRows(b.hsn[0])
	g.HSN.B2C = hsnRows(b.hsn[1])

	sks := sortedKeys(b.docs)
	sort.SliceStable(sks, func(i, j int) bool { return b.docs[sks[i]].docNum < b.docs[sks[j]].docNum })
	byDoc := map[int][]DocRange{}
	for _, k := range sks {
		s := b.docs[k]
		r := DocRange{From: s.minS, To: s.maxS, Totnum: s.total, Cancel: s.cancelled}
		if s.numeric {
			if span := int(s.maxN - s.minN + 1); span > s.total {
				gaps := span - s.total
				b.warn.add("gaps", "series %s: %d number(s) between %s and %s were never issued in this period; Table 13 counts them as cancelled — check unused offline number blocks", s.prefix, gaps, s.minS, s.maxS)
				r.Totnum, r.Cancel = span, s.cancelled+gaps
			}
		}
		r.NetIssue = r.Totnum - r.Cancel
		r.Num = len(byDoc[s.docNum]) + 1
		byDoc[s.docNum] = append(byDoc[s.docNum], r)
	}
	for _, n := range []int{1, 5} {
		if len(byDoc[n]) > 0 {
			typ := "Invoices for outward supply"
			if n == 5 {
				typ = "Credit Note"
			}
			g.DocIssue.DocDet = append(g.DocIssue.DocDet, DocDet{DocNum: n, DocTyp: typ, Docs: byDoc[n]})
		}
	}

	sum := b.sum
	sum.Liability = b.liab.heads()
	for _, name := range []string{"b2b", "b2cl", "b2cs", "cdnr", "cdnur", "exp"} {
		b.section(name)
	}
	g.Summary = &sum
	g.Warnings = b.warn.list()
	return g
}

func hsnRows(m map[hsnKey]*hsnAcc) []HSNRow {
	keys := make([]hsnKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.hsn != b.hsn {
			return a.hsn < b.hsn
		}
		if a.rate != b.rate {
			return a.rate < b.rate
		}
		return a.uqc < b.uqc
	})
	out := make([]HSNRow, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		if a.amounts == (amounts{}) && a.qtyMilli == 0 {
			continue // fully reversed by credit notes
		}
		d := a.det(k.rate)
		out = append(out, HSNRow{Num: len(out) + 1, HsnSc: k.hsn, Desc: a.desc, Uqc: k.uqc,
			Qty: math.Round(float64(a.qtyMilli)/10) / 100, Rt: d.Rt, Txval: d.Txval, Iamt: d.Iamt, Camt: d.Camt, Samt: d.Samt, Csamt: d.Csamt})
	}
	return out
}

func lessDoc(isoA, numA, isoB, numB string) bool {
	if isoA != isoB {
		return isoA < isoB
	}
	pa, na, oka := splitNumber(numA)
	pb, nb, okb := splitNumber(numB)
	if oka && okb && pa == pb {
		return na < nb
	}
	return numA < numB
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedInts[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// warnings collects messages, keeping at most 20 per kind.
type warnings struct {
	order  []string
	byKind map[string][]string
	extra  map[string]int
}

func newWarnings() warnings {
	return warnings{byKind: map[string][]string{}, extra: map[string]int{}}
}

func (w *warnings) add(kind, format string, a ...any) {
	if _, ok := w.byKind[kind]; !ok {
		w.order = append(w.order, kind)
	}
	if len(w.byKind[kind]) >= 20 {
		w.extra[kind]++
		return
	}
	w.byKind[kind] = append(w.byKind[kind], fmt.Sprintf(format, a...))
}

func (w *warnings) list() []string {
	out := []string{}
	for _, k := range w.order {
		out = append(out, w.byKind[k]...)
		if n := w.extra[k]; n > 0 {
			out = append(out, fmt.Sprintf("… and %d more like the above", n))
		}
	}
	return out
}
