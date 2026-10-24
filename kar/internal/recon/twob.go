package recon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Document types of a 2B document.
const (
	DocInvoice    = "INV"
	DocCreditNote = "CN"
	DocDebitNote  = "DN"
)

// Doc is one supplier document from GSTR-2B.
type Doc struct {
	Key             string `json:"doc_key"`
	Section         string `json:"section"` // b2b | b2ba | cdnr | cdnra
	SupplierGSTIN   string `json:"supplier_gstin"`
	SupplierName    string `json:"supplier_name"`
	DocType         string `json:"doc_type"` // INV | CN | DN
	DocNo           string `json:"doc_no"`
	DocDate         string `json:"doc_date"`    // YYYY-MM-DD
	SupplyType      string `json:"supply_type"` // R, SEZWP, SEZWOP, DE, CBW
	POS             string `json:"pos"`
	ReverseCharge   bool   `json:"reverse_charge"`
	Value           Rs     `json:"value"`
	Taxable         Rs     `json:"taxable"`
	IGST            Rs     `json:"igst"`
	CGST            Rs     `json:"cgst"`
	SGST            Rs     `json:"sgst"`
	Cess            Rs     `json:"cess"`
	ITCAvailable    string `json:"itc_available"` // Y | N | T (temporarily unavailable)
	Reason          string `json:"reason,omitempty"`
	SupplierPeriod  string `json:"supplier_period,omitempty"`
	SupplierFiledOn string `json:"supplier_filed_on,omitempty"` // YYYY-MM-DD
	IRN             string `json:"irn,omitempty"`
}

// Tax returns the document's tax by head.
func (d Doc) Tax() Heads { return Heads{IGST: d.IGST, CGST: d.CGST, SGST: d.SGST, Cess: d.Cess} }

// Statement is a parsed GSTR-2B file.
type Statement struct {
	GSTIN       string   `json:"gstin"`
	Period      string   `json:"period"`
	GeneratedOn string   `json:"generated_on,omitempty"` // YYYY-MM-DD
	Version     string   `json:"version,omitempty"`
	Docs        []Doc    `json:"docs"`
	Skipped     []string `json:"skipped,omitempty"` // sections present but not reconciled (impg, isd, …)
}

type twoBDoc struct {
	Inum   string `json:"inum"`
	Ntnum  string `json:"ntnum"`
	Typ    string `json:"typ"`
	Suptyp string `json:"suptyp"`
	Dt     string `json:"dt"`
	Val    Rs     `json:"val"`
	Pos    string `json:"pos"`
	Rev    string `json:"rev"`
	Itcavl string `json:"itcavl"`
	Rsn    string `json:"rsn"`
	Txval  Rs     `json:"txval"`
	Igst   Rs     `json:"igst"`
	Cgst   Rs     `json:"cgst"`
	Sgst   Rs     `json:"sgst"`
	Cess   Rs     `json:"cess"`
	Irn    string `json:"irn"`
}

type twoBSupplier struct {
	Ctin     string    `json:"ctin"`
	Trdnm    string    `json:"trdnm"`
	Supfildt string    `json:"supfildt"`
	Supprd   string    `json:"supprd"`
	Inv      []twoBDoc `json:"inv"`
	Nt       []twoBDoc `json:"nt"`
}

type twoBData struct {
	Gstin   string                     `json:"gstin"`
	RtnPrd  string                     `json:"rtnprd"`
	GenDt   string                     `json:"gendt"`
	Version string                     `json:"version"`
	DocData map[string]json.RawMessage `json:"docdata"`
}

// ParseGSTR2B reads the portal's GSTR-2B JSON (with or without the outer
// {"data": …} wrapper some GSPs strip). It reconciles b2b, b2ba, cdnr and
// cdnra; other sections (impg, isd, …) are listed in Skipped. Missing
// sections are fine.
func ParseGSTR2B(raw []byte) (Statement, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	var top struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return Statement{}, fmt.Errorf("not a GSTR-2B JSON file: %w", err)
	}
	body := raw
	if t := bytes.TrimSpace(top.Data); len(t) > 0 && t[0] == '{' {
		body = t
	}
	var d twoBData
	if err := json.Unmarshal(body, &d); err != nil {
		return Statement{}, fmt.Errorf("not a GSTR-2B JSON file: %w", err)
	}
	if d.DocData == nil && d.Gstin == "" {
		return Statement{}, fmt.Errorf("not a GSTR-2B JSON file: no gstin or docdata")
	}
	st := Statement{GSTIN: strings.ToUpper(strings.TrimSpace(d.Gstin)), Period: strings.TrimSpace(d.RtnPrd),
		GeneratedOn: gstnToISO(d.GenDt), Version: d.Version, Docs: []Doc{}}
	names := make([]string, 0, len(d.DocData))
	for k := range d.DocData {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, sec := range names {
		switch sec {
		case "b2b", "b2ba", "cdnr", "cdnra":
		default:
			st.Skipped = append(st.Skipped, sec)
			continue
		}
		var sups []twoBSupplier
		if err := json.Unmarshal(d.DocData[sec], &sups); err != nil {
			return Statement{}, fmt.Errorf("section %s: %w", sec, err)
		}
		for _, s := range sups {
			docs := s.Inv
			if strings.HasPrefix(sec, "cdnr") {
				docs = s.Nt
			}
			for _, x := range docs {
				doc := Doc{Section: sec, SupplierGSTIN: strings.ToUpper(strings.TrimSpace(s.Ctin)), SupplierName: s.Trdnm,
					DocDate: gstnToISO(x.Dt), POS: x.Pos, ReverseCharge: strings.EqualFold(x.Rev, "Y"),
					Value: x.Val, Taxable: x.Txval, IGST: x.Igst, CGST: x.Cgst, SGST: x.Sgst, Cess: x.Cess,
					ITCAvailable: strings.ToUpper(x.Itcavl), Reason: x.Rsn, SupplierPeriod: s.Supprd,
					SupplierFiledOn: gstnToISO(s.Supfildt), IRN: x.Irn}
				if strings.HasPrefix(sec, "cdnr") {
					doc.DocNo, doc.SupplyType = x.Ntnum, x.Suptyp
					doc.DocType = DocCreditNote
					if strings.EqualFold(x.Typ, "D") {
						doc.DocType = DocDebitNote
					}
				} else {
					doc.DocNo, doc.SupplyType, doc.DocType = x.Inum, x.Typ, DocInvoice
				}
				if doc.ITCAvailable == "" {
					doc.ITCAvailable = "Y"
				}
				if doc.SupplierGSTIN == "" || doc.DocNo == "" {
					return Statement{}, fmt.Errorf("section %s: document without supplier GSTIN or number", sec)
				}
				doc.Key = DocKey(doc.SupplierGSTIN, doc.DocType, doc.DocNo, doc.DocDate)
				st.Docs = append(st.Docs, doc)
			}
		}
	}
	// An amendment (b2ba/cdnra) supersedes the original with the same key.
	byKey := map[string]int{}
	out := st.Docs[:0]
	for _, doc := range st.Docs {
		if i, ok := byKey[doc.Key]; ok {
			if strings.HasSuffix(doc.Section, "a") {
				out[i] = doc
			}
			continue
		}
		byKey[doc.Key] = len(out)
		out = append(out, doc)
	}
	st.Docs = out
	return st, nil
}
