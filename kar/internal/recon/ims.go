package recon

import (
	"fmt"
	"sort"
	"time"
)

// IMS actions on a supplier document.
const (
	ActionAccept  = "accept"
	ActionReject  = "reject"
	ActionPending = "pending"
)

// ValidAction reports whether a is an IMS action.
func ValidAction(a string) bool { return a == ActionAccept || a == ActionReject || a == ActionPending }

// Action is the recipient's IMS decision on a document.
type Action struct {
	Action  string    `json:"action"`
	Remark  string    `json:"remark,omitempty"`
	Actor   string    `json:"actor"`
	ActedAt time.Time `json:"acted_at"`
}

// ITCBucket is ITC by head with a document count.
type ITCBucket struct {
	Docs int `json:"docs"`
	Heads
}

func (b *ITCBucket) add(d Doc) {
	sign := int64(1)
	if d.DocType == DocCreditNote {
		sign = -1
	}
	b.Docs++
	b.Heads = b.Heads.Add(d.Tax(), sign)
}

// ITCSummary is the ITC GSTR-2B offers for the period after IMS actions.
// Under IMS a document with no action is deemed accepted when 2B is generated.
type ITCSummary struct {
	Accepted       ITCBucket `json:"accepted"`        // explicitly accepted
	DeemedAccepted ITCBucket `json:"deemed_accepted"` // no action yet
	Pending        ITCBucket `json:"pending"`
	Rejected       ITCBucket `json:"rejected"`
	NotAvailable   ITCBucket `json:"not_available"` // itcavl = N (Sec 16(4), POS rules, …)
	// Available = accepted + deemed accepted, split as GSTR-3B Table 4(A)(3) and 4(A)(5).
	ReverseCharge Heads `json:"reverse_charge"` // 4(A)(3)
	AllOther      Heads `json:"all_other"`      // 4(A)(5)
	Available     Heads `json:"available"`
}

// SummariseITC totals the period's 2B documents by IMS state.
func SummariseITC(docs []Doc, actions map[string]Action) ITCSummary {
	var s ITCSummary
	for _, d := range docs {
		if d.ITCAvailable == "N" {
			s.NotAvailable.add(d)
			continue
		}
		a, ok := actions[d.Key]
		switch {
		case !ok:
			s.DeemedAccepted.add(d)
		case a.Action == ActionAccept:
			s.Accepted.add(d)
		case a.Action == ActionReject:
			s.Rejected.add(d)
			continue
		default:
			s.Pending.add(d)
			continue
		}
		sign := int64(1)
		if d.DocType == DocCreditNote {
			sign = -1
		}
		if d.ReverseCharge {
			s.ReverseCharge = s.ReverseCharge.Add(d.Tax(), sign)
		} else {
			s.AllOther = s.AllOther.Add(d.Tax(), sign)
		}
	}
	s.Available = s.ReverseCharge.Add(s.AllOther, 1)
	return s
}

// IMSFile is the upload for the portal's IMS "save" action.
//
// NOTE: this follows the published IMS API/offline-tool layout as of 2025
// (rtin, reqtyp, invdata.b2b[] / invdata.cdn[] with action A/R/P and srcform
// R1). It must be checked against the current GSTN IMS schema (and the GSP's
// wrapper) before it is uploaded; field names have changed between releases.
type IMSFile struct {
	Rtin    string     `json:"rtin"`
	ReqTyp  string     `json:"reqtyp"` // SAVE
	Period  string     `json:"rtnprd"`
	InvData IMSInvData `json:"invdata"`
}

// IMSInvData groups the documents by section.
type IMSInvData struct {
	B2B []IMSDoc `json:"b2b"`
	CDN []IMSDoc `json:"cdn"`
}

// IMSDoc is one document with the recipient's action.
type IMSDoc struct {
	Stin    string `json:"stin"`
	InvTyp  string `json:"inv_typ,omitempty"` // R, SEZWP, SEZWOP, DE (b2b)
	Inum    string `json:"inum,omitempty"`
	Idt     string `json:"idt,omitempty"`
	NtTyp   string `json:"ntty,omitempty"` // C | D (cdn)
	NtNum   string `json:"nt_num,omitempty"`
	NtDt    string `json:"nt_dt,omitempty"`
	Val     Rs     `json:"val"`
	Pos     string `json:"pos"`
	Txval   Rs     `json:"txval"`
	Iamt    Rs     `json:"iamt"`
	Camt    Rs     `json:"camt"`
	Samt    Rs     `json:"samt"`
	Cess    Rs     `json:"cess"`
	Action  string `json:"action"` // A accept | R reject | P pending
	SrcForm string `json:"srcform"`
	Remark  string `json:"remarks,omitempty"`
}

var imsCode = map[string]string{ActionAccept: "A", ActionReject: "R", ActionPending: "P"}

// BuildIMSFile lists every document of the period that carries an action.
func BuildIMSFile(gstin, period string, docs []Doc, actions map[string]Action) IMSFile {
	f := IMSFile{Rtin: gstin, ReqTyp: "SAVE", Period: period, InvData: IMSInvData{B2B: []IMSDoc{}, CDN: []IMSDoc{}}}
	sorted := append([]Doc(nil), docs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	for _, d := range sorted {
		a, ok := actions[d.Key]
		if !ok {
			continue
		}
		x := IMSDoc{Stin: d.SupplierGSTIN, Val: d.Value, Pos: d.POS, Txval: d.Taxable, Iamt: d.IGST, Camt: d.CGST,
			Samt: d.SGST, Cess: d.Cess, Action: imsCode[a.Action], SrcForm: "R1", Remark: a.Remark}
		if d.DocType == DocInvoice {
			x.InvTyp, x.Inum, x.Idt = d.SupplyType, d.DocNo, isoToGSTN(d.DocDate)
			f.InvData.B2B = append(f.InvData.B2B, x)
		} else {
			x.NtTyp = "C"
			if d.DocType == DocDebitNote {
				x.NtTyp = "D"
			}
			x.NtNum, x.NtDt = d.DocNo, isoToGSTN(d.DocDate)
			f.InvData.CDN = append(f.InvData.CDN, x)
		}
	}
	return f
}

// String is used in logs.
func (a Action) String() string { return fmt.Sprintf("%s by %s", a.Action, a.Actor) }
