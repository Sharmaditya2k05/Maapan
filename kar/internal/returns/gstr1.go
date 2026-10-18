// Package returns builds GSTR-1 and GSTR-3B from a period's invoices in the
// structure of the GSTN offline tool JSON. Amounts are accumulated in paise and
// converted to rupees only at the end, so totals never drift.
package returns

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/compliance"
)

// Period parses MMYYYY into the first and last day (YYYY-MM-DD).
func Period(p string) (from, to string, err error) {
	t, err := time.Parse("012006", p)
	if err != nil || len(p) != 6 {
		return "", "", fmt.Errorf("period must be MMYYYY, e.g. 102026")
	}
	return t.Format(time.DateOnly), t.AddDate(0, 1, -1).Format(time.DateOnly), nil
}

// amounts is an internal paise accumulator.
type amounts struct{ txval, iamt, camt, samt, csamt int64 }

func (a *amounts) add(l clients.Line, sign int64) {
	a.txval += sign * l.TaxablePaise
	a.iamt += sign * l.IGSTPaise
	a.camt += sign * l.CGSTPaise
	a.samt += sign * l.SGSTPaise
	a.csamt += sign * l.CessPaise
}

// ItemDet is the GSTN rate-wise detail.
type ItemDet struct {
	Rt    float64 `json:"rt"`
	Txval float64 `json:"txval"`
	Iamt  float64 `json:"iamt"`
	Camt  float64 `json:"camt"`
	Samt  float64 `json:"samt"`
	Csamt float64 `json:"csamt"`
}

func (a amounts) det(rateBP int) ItemDet {
	r := compliance.Rupees
	return ItemDet{Rt: compliance.RateFromBP(rateBP), Txval: r(a.txval), Iamt: r(a.iamt), Camt: r(a.camt), Samt: r(a.samt), Csamt: r(a.csamt)}
}

type Itm struct {
	Num    int     `json:"num"`
	ItmDet ItemDet `json:"itm_det"`
}

type B2BInv struct {
	Inum   string  `json:"inum"`
	Idt    string  `json:"idt"`
	Val    float64 `json:"val"`
	Pos    string  `json:"pos"`
	Rchrg  string  `json:"rchrg"`
	InvTyp string  `json:"inv_typ"`
	Itms   []Itm   `json:"itms"`
}

type B2B struct {
	Ctin string   `json:"ctin"`
	Inv  []B2BInv `json:"inv"`
}

type B2CLInv struct {
	Inum string  `json:"inum"`
	Idt  string  `json:"idt"`
	Val  float64 `json:"val"`
	Itms []Itm   `json:"itms"`
}

type B2CL struct {
	Pos string    `json:"pos"`
	Inv []B2CLInv `json:"inv"`
}

type B2CS struct {
	SplyTy string  `json:"sply_ty"` // INTRA | INTER
	Pos    string  `json:"pos"`
	Typ    string  `json:"typ"` // OE
	Rt     float64 `json:"rt"`
	Txval  float64 `json:"txval"`
	Iamt   float64 `json:"iamt"`
	Camt   float64 `json:"camt"`
	Samt   float64 `json:"samt"`
	Csamt  float64 `json:"csamt"`
}

type Note struct {
	Ntty   string  `json:"ntty"` // C credit | D debit
	NtNum  string  `json:"nt_num"`
	NtDt   string  `json:"nt_dt"`
	Val    float64 `json:"val"`
	Pos    string  `json:"pos"`
	Rchrg  string  `json:"rchrg"`
	InvTyp string  `json:"inv_typ"`
	Itms   []Itm   `json:"itms"`
}

type CDNR struct {
	Ctin string `json:"ctin"`
	Nt   []Note `json:"nt"`
}

type HSNRow struct {
	Num   int     `json:"num"`
	HsnSc string  `json:"hsn_sc"`
	Desc  string  `json:"desc"`
	Uqc   string  `json:"uqc"`
	Qty   float64 `json:"qty"`
	Rt    float64 `json:"rt"`
	Txval float64 `json:"txval"`
	Iamt  float64 `json:"iamt"`
	Camt  float64 `json:"camt"`
	Samt  float64 `json:"samt"`
	Csamt float64 `json:"csamt"`
}

type DocRange struct {
	Num      int    `json:"num"`
	From     string `json:"from"`
	To       string `json:"to"`
	Totnum   int    `json:"totnum"`
	Cancel   int    `json:"cancel"`
	NetIssue int    `json:"net_issue"`
}

type DocDet struct {
	DocNum int        `json:"doc_num"` // 1 outward invoices, 5 credit notes
	Docs   []DocRange `json:"docs"`
}

// GSTR1 is the return in GSTN offline-tool structure plus warnings.
type GSTR1 struct {
	Gstin string `json:"gstin"`
	Fp    string `json:"fp"`
	B2B   []B2B  `json:"b2b"`
	B2CL  []B2CL `json:"b2cl"`
	B2CS  []B2CS `json:"b2cs"`
	CDNR  []CDNR `json:"cdnr"`
	HSN   struct {
		Data []HSNRow `json:"data"`
	} `json:"hsn"`
	DocIssue struct {
		DocDet []DocDet `json:"doc_det"`
	} `json:"doc_issue"`
	Warnings []string `json:"warnings,omitempty"`
}

// rateItems groups an invoice's lines by tax rate.
func rateItems(lines []clients.Line) []Itm {
	byRate := map[int]*amounts{}
	for _, l := range lines {
		a := byRate[l.TaxRateBP]
		if a == nil {
			a = &amounts{}
			byRate[l.TaxRateBP] = a
		}
		a.add(l, 1)
	}
	rates := make([]int, 0, len(byRate))
	for r := range byRate {
		rates = append(rates, r)
	}
	sort.Ints(rates)
	out := make([]Itm, 0, len(rates))
	for i, r := range rates {
		out = append(out, Itm{Num: i + 1, ItmDet: byRate[r].det(r)})
	}
	return out
}

// BuildGSTR1 classifies outward supplies. sellerState is the registration's
// state; b2clThreshold is the invoice value above which an unregistered
// inter-state sale is reported invoice-wise (₹1,00,000 today).
func BuildGSTR1(gstin, fp, sellerState string, b2clThreshold int64, invs []clients.Invoice) GSTR1 {
	g := GSTR1{Gstin: gstin, Fp: fp, B2B: []B2B{}, B2CL: []B2CL{}, B2CS: []B2CS{}, CDNR: []CDNR{}}
	b2b := map[string]*B2B{}
	b2cl := map[string]*B2CL{}
	cdnr := map[string]*CDNR{}
	type csKey struct {
		pos  string
		rate int
	}
	b2cs := map[csKey]*amounts{}
	type hsnKey struct {
		hsn  string
		rate int
	}
	type hsnAcc struct {
		amounts
		desc, uqc string
		qtyMilli  int64
	}
	hsn := map[hsnKey]*hsnAcc{}
	type series struct {
		docNum           int
		prefix           string
		from, to         string
		total, cancelled int
	}
	docs := map[string]*series{}

	for _, inv := range invs {
		if inv.DocType != "sale" && inv.DocType != "sale_return" {
			continue
		}
		docNum := 1
		if inv.DocType == "sale_return" {
			docNum = 5
		}
		prefix := inv.Number
		if i := strings.LastIndex(inv.Number, "/"); i >= 0 {
			prefix = inv.Number[:i]
		}
		sk := fmt.Sprintf("%d|%s", docNum, prefix)
		s := docs[sk]
		if s == nil {
			s = &series{docNum: docNum, prefix: prefix, from: inv.Number, to: inv.Number}
			docs[sk] = s
		}
		s.total++
		if inv.Number < s.from {
			s.from = inv.Number
		}
		if inv.Number > s.to {
			s.to = inv.Number
		}
		if inv.Status != "posted" {
			s.cancelled++
			continue
		}

		pos := inv.PlaceOfSupply
		if pos == "" {
			pos = sellerState
		}
		registered := inv.PartyGSTIN != ""
		sign := int64(1)
		if inv.DocType == "sale_return" {
			sign = -1
		}

		switch {
		case inv.DocType == "sale" && registered:
			e := b2b[inv.PartyGSTIN]
			if e == nil {
				e = &B2B{Ctin: inv.PartyGSTIN}
				b2b[inv.PartyGSTIN] = e
			}
			e.Inv = append(e.Inv, B2BInv{Inum: inv.Number, Idt: compliance.GSTNDate(inv.InvoiceDate),
				Val: compliance.Rupees(inv.GrandTotalPaise), Pos: pos, Rchrg: "N", InvTyp: "R", Itms: rateItems(inv.Lines)})
		case inv.DocType == "sale" && pos != sellerState && inv.GrandTotalPaise > b2clThreshold:
			e := b2cl[pos]
			if e == nil {
				e = &B2CL{Pos: pos}
				b2cl[pos] = e
			}
			e.Inv = append(e.Inv, B2CLInv{Inum: inv.Number, Idt: compliance.GSTNDate(inv.InvoiceDate),
				Val: compliance.Rupees(inv.GrandTotalPaise), Itms: rateItems(inv.Lines)})
		case inv.DocType == "sale_return" && registered:
			e := cdnr[inv.PartyGSTIN]
			if e == nil {
				e = &CDNR{Ctin: inv.PartyGSTIN}
				cdnr[inv.PartyGSTIN] = e
			}
			e.Nt = append(e.Nt, Note{Ntty: "C", NtNum: inv.Number, NtDt: compliance.GSTNDate(inv.InvoiceDate),
				Val: compliance.Rupees(inv.GrandTotalPaise), Pos: pos, Rchrg: "N", InvTyp: "R", Itms: rateItems(inv.Lines)})
		default:
			// Unregistered B2C (and their credit notes, netted) go to B2CS by POS and rate.
			for _, l := range inv.Lines {
				k := csKey{pos, l.TaxRateBP}
				a := b2cs[k]
				if a == nil {
					a = &amounts{}
					b2cs[k] = a
				}
				a.add(l, sign)
			}
			if inv.DocType == "sale_return" {
				g.Warnings = append(g.Warnings, fmt.Sprintf("credit note %s to an unregistered buyer was netted in B2CS", inv.Number))
			}
		}

		for _, l := range inv.Lines {
			code := strings.TrimSpace(l.HSN)
			if code == "" {
				g.Warnings = append(g.Warnings, fmt.Sprintf("%s line %d has no HSN; reported under 'NA'", inv.Number, l.LineNo))
				code = "NA"
			}
			k := hsnKey{code, l.TaxRateBP}
			a := hsn[k]
			if a == nil {
				a = &hsnAcc{desc: l.Name, uqc: compliance.UQC(l.Unit)}
				hsn[k] = a
			}
			a.add(l, sign)
			a.qtyMilli += sign * int64(l.Qty*1000+0.5)
		}
	}

	for _, k := range sortedKeys(b2b) {
		g.B2B = append(g.B2B, *b2b[k])
	}
	for _, k := range sortedKeys(b2cl) {
		g.B2CL = append(g.B2CL, *b2cl[k])
	}
	for _, k := range sortedKeys(cdnr) {
		g.CDNR = append(g.CDNR, *cdnr[k])
	}
	csKeys := make([]csKey, 0, len(b2cs))
	for k := range b2cs {
		csKeys = append(csKeys, k)
	}
	sort.Slice(csKeys, func(i, j int) bool {
		if csKeys[i].pos != csKeys[j].pos {
			return csKeys[i].pos < csKeys[j].pos
		}
		return csKeys[i].rate < csKeys[j].rate
	})
	for _, k := range csKeys {
		a := b2cs[k]
		d := a.det(k.rate)
		sply := "INTRA"
		if k.pos != sellerState {
			sply = "INTER"
		}
		g.B2CS = append(g.B2CS, B2CS{SplyTy: sply, Pos: k.pos, Typ: "OE", Rt: d.Rt, Txval: d.Txval,
			Iamt: d.Iamt, Camt: d.Camt, Samt: d.Samt, Csamt: d.Csamt})
	}
	hKeys := make([]hsnKey, 0, len(hsn))
	for k := range hsn {
		hKeys = append(hKeys, k)
	}
	sort.Slice(hKeys, func(i, j int) bool {
		if hKeys[i].hsn != hKeys[j].hsn {
			return hKeys[i].hsn < hKeys[j].hsn
		}
		return hKeys[i].rate < hKeys[j].rate
	})
	for i, k := range hKeys {
		a := hsn[k]
		d := a.det(k.rate)
		g.HSN.Data = append(g.HSN.Data, HSNRow{Num: i + 1, HsnSc: k.hsn, Desc: a.desc, Uqc: a.uqc,
			Qty: float64(a.qtyMilli) / 1000, Rt: d.Rt, Txval: d.Txval, Iamt: d.Iamt, Camt: d.Camt, Samt: d.Samt, Csamt: d.Csamt})
	}
	byDoc := map[int][]DocRange{}
	for _, k := range sortedKeys(docs) {
		s := docs[k]
		byDoc[s.docNum] = append(byDoc[s.docNum], DocRange{Num: len(byDoc[s.docNum]) + 1, From: s.from, To: s.to,
			Totnum: s.total, Cancel: s.cancelled, NetIssue: s.total - s.cancelled})
	}
	for _, n := range []int{1, 5} {
		if len(byDoc[n]) > 0 {
			g.DocIssue.DocDet = append(g.DocIssue.DocDet, DocDet{DocNum: n, Docs: byDoc[n]})
		}
	}
	return g
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
