// Package validate turns mapped spreadsheet rows into the bulk payload records
// bahi and bhandar accept, collecting row-level errors and duplicates.
package validate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/taraju/aayaat/internal/mapping"
	"github.com/taraju/aayaat/internal/sheet"
	"github.com/taraju/mool/gst"
)

// PartyRecord matches bahi POST /internal/v1/bulk/parties items.
type PartyRecord struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Phone               string `json:"phone,omitempty"`
	GSTIN               string `json:"gstin,omitempty"`
	StateCode           string `json:"state_code,omitempty"`
	Address             string `json:"address,omitempty"`
	OpeningBalancePaise int64  `json:"opening_balance_paise"`
	OpeningType         string `json:"opening_type"`
	ExternalRef         string `json:"external_ref,omitempty"`
}

// BalanceRecord matches bahi POST /internal/v1/bulk/opening-balances items.
type BalanceRecord struct {
	AccountCode      string `json:"account_code,omitempty"`
	PartyExternalRef string `json:"party_external_ref,omitempty"`
	AmountPaise      int64  `json:"amount_paise"`
	Type             string `json:"type"`
}

// ItemRecord matches bhandar POST /internal/v1/bulk/items items.
type ItemRecord struct {
	Name               string  `json:"name"`
	SKU                string  `json:"sku,omitempty"`
	Barcode            string  `json:"barcode,omitempty"`
	HSN                string  `json:"hsn,omitempty"`
	Unit               string  `json:"unit"`
	SalePricePaise     int64   `json:"sale_price_paise"`
	MRPPaise           int64   `json:"mrp_paise"`
	PurchasePricePaise int64   `json:"purchase_price_paise"`
	TaxRateBP          int     `json:"tax_rate_bp"`
	ReorderLevel       float64 `json:"reorder_level"`
	ExternalRef        string  `json:"external_ref,omitempty"`
}

// StockRecord matches bhandar POST /internal/v1/bulk/opening-stock lines.
type StockRecord struct {
	ItemExternalRef string  `json:"item_external_ref,omitempty"`
	ItemID          string  `json:"item_id,omitempty"`
	Qty             float64 `json:"qty"`
	UnitCostPaise   int64   `json:"unit_cost_paise"`
	BatchNo         string  `json:"batch_no,omitempty"`
	ExpiryDate      string  `json:"expiry_date,omitempty"`
}

// Result is one validated row.
type Result struct {
	Record any               // one of the *Record types (nil when invalid)
	Raw    map[string]string // mapped field → original text
	Errors []string
	// Side/Amount feed the Dr/Cr reconciliation (parties, opening_balances).
	Side   string
	Amount int64
}

// Validator validates the rows of one import.
type Validator struct {
	Kind  string
	Index map[string]int // field → column
	seen  map[string]int // dedupe key → first row number
}

// New builds a validator for kind with resolved column indexes.
func New(kind string, index map[string]int) *Validator {
	return &Validator{Kind: kind, Index: index, seen: map[string]int{}}
}

func (v *Validator) get(cells []string, field string) string {
	i, ok := v.Index[field]
	if !ok {
		return ""
	}
	return sheet.Cell(cells, i)
}

// dup records key for rowNo and returns an error if an earlier row had it.
func (v *Validator) dup(key string, rowNo int, what string) string {
	if strings.TrimSpace(key) == "" || strings.HasSuffix(key, ":") {
		return ""
	}
	if first, ok := v.seen[key]; ok {
		return fmt.Sprintf("duplicate %s (same as row %d)", what, first)
	}
	v.seen[key] = rowNo
	return ""
}

// Row validates one data row.
func (v *Validator) Row(rowNo int, cells []string) Result {
	raw := map[string]string{}
	for f := range v.Index {
		raw[f] = v.get(cells, f)
	}
	var r Result
	switch v.Kind {
	case mapping.KindParties:
		r = v.party(rowNo, raw)
	case mapping.KindItems:
		r = v.item(rowNo, raw)
	case mapping.KindOpeningBalances:
		r = v.balance(rowNo, raw)
	case mapping.KindOpeningStock:
		r = v.stock(rowNo, raw)
	default:
		r.Errors = []string{"unknown import kind"}
	}
	r.Raw = raw
	if len(r.Errors) > 0 {
		r.Record = nil
	}
	return r
}

func (v *Validator) party(rowNo int, raw map[string]string) Result {
	var r Result
	errs := &r.Errors
	add := func(e string) {
		if e != "" {
			*errs = append(*errs, e)
		}
	}
	p := PartyRecord{Name: collapse(raw["name"]), Address: collapse(raw["address"]), ExternalRef: refOrName(raw["code"], raw["name"])}
	if p.Name == "" {
		add("name is required")
	}
	p.Type = partyType(raw["type"])
	if raw["phone"] != "" {
		ph, err := Phone(raw["phone"])
		if err != nil {
			add("phone: " + err.Error())
		}
		p.Phone = ph
	}
	if g := gst.Normalize(raw["gstin"]); g != "" && g != "NA" && g != "URP" {
		if err := gst.ValidateGSTIN(g); err != nil {
			add("gstin: " + err.Error())
		}
		p.GSTIN = g
		p.StateCode = gst.StateCode(g)
	}
	if s := raw["state"]; s != "" {
		code := StateCode(s)
		switch {
		case code == "":
			add(fmt.Sprintf("state: %q is not a known state", s))
		case p.StateCode != "" && code != p.StateCode:
			add(fmt.Sprintf("state %s does not match GSTIN state %s", code, p.StateCode))
		default:
			p.StateCode = code
		}
	}
	amt, side, err := sheet.ParseDrCr(raw["opening_balance"])
	if err != nil {
		add("opening_balance: " + err.Error())
	}
	if s := sheet.NormalizeSide(raw["dr_cr"]); s != "" {
		side = s
	} else if raw["dr_cr"] != "" {
		add(fmt.Sprintf("dr_cr: %q must be Dr or Cr", raw["dr_cr"]))
	}
	if side == "" {
		side = "dr"
		if p.Type == "supplier" {
			side = "cr"
		}
	}
	p.OpeningBalancePaise, p.OpeningType = amt, side
	add(v.dup("name:"+mapping.Normalize(p.Name), rowNo, "party name"))
	add(v.dup("gstin:"+p.GSTIN, rowNo, "GSTIN"))
	add(v.dup("code:"+strings.TrimSpace(raw["code"]), rowNo, "party code"))
	r.Record, r.Side, r.Amount = p, side, amt
	return r
}

func (v *Validator) item(rowNo int, raw map[string]string) Result {
	var r Result
	add := func(e string) {
		if e != "" {
			r.Errors = append(r.Errors, e)
		}
	}
	it := ItemRecord{Name: collapse(raw["name"]), SKU: raw["sku"], Barcode: strings.ReplaceAll(raw["barcode"], " ", ""),
		ExternalRef: refOrName(raw["code"], raw["name"]), Unit: strings.ToUpper(raw["unit"])}
	if it.Name == "" {
		add("name is required")
	}
	if it.Unit == "" {
		it.Unit = "PCS"
	}
	if h := strings.ReplaceAll(raw["hsn"], " ", ""); h != "" {
		if err := HSN(h); err != nil {
			add("hsn: " + err.Error())
		}
		it.HSN = h
	}
	money := func(field string, dst *int64) {
		p, err := sheet.ParsePaise(raw[field])
		if err != nil {
			add(field + ": " + err.Error())
		} else if p < 0 {
			add(field + ": cannot be negative")
		}
		*dst = p
	}
	money("sale_price", &it.SalePricePaise)
	money("mrp", &it.MRPPaise)
	money("purchase_price", &it.PurchasePricePaise)
	if it.MRPPaise > 0 && it.SalePricePaise > it.MRPPaise {
		add("sale_price is above MRP")
	}
	bp, err := sheet.ParseRateBP(raw["tax_rate"])
	if err != nil {
		add("tax_rate: " + err.Error())
	} else if !slices.Contains(gst.StandardRatesBP, bp) {
		add(fmt.Sprintf("tax_rate: %.2f%% is not a GST slab", float64(bp)/100))
	}
	it.TaxRateBP = bp
	if q, err := sheet.ParseQty(raw["reorder_level"]); err != nil {
		add("reorder_level: " + err.Error())
	} else {
		it.ReorderLevel = q
	}
	add(v.dup("name:"+mapping.Normalize(it.Name), rowNo, "item name"))
	add(v.dup("barcode:"+it.Barcode, rowNo, "barcode"))
	add(v.dup("code:"+strings.TrimSpace(raw["code"]), rowNo, "item code"))
	r.Record = it
	return r
}

func (v *Validator) balance(rowNo int, raw map[string]string) Result {
	var r Result
	add := func(e string) {
		if e != "" {
			r.Errors = append(r.Errors, e)
		}
	}
	b := BalanceRecord{AccountCode: raw["account_code"], PartyExternalRef: raw["party_ref"]}
	if b.AccountCode == "" && b.PartyExternalRef == "" {
		add("account_code or party_ref is required")
	}
	amt, side, err := sheet.ParseDrCr(raw["amount"])
	if err != nil {
		add("amount: " + err.Error())
	}
	if s := sheet.NormalizeSide(raw["dr_cr"]); s != "" {
		side = s
	} else if raw["dr_cr"] != "" {
		add(fmt.Sprintf("dr_cr: %q must be Dr or Cr", raw["dr_cr"]))
	}
	if side == "" {
		side = "dr"
	}
	b.AmountPaise, b.Type = amt, side
	add(v.dup("acct:"+b.AccountCode+"|"+b.PartyExternalRef, rowNo, "account"))
	r.Record, r.Side, r.Amount = b, side, amt
	return r
}

func (v *Validator) stock(rowNo int, raw map[string]string) Result {
	var r Result
	add := func(e string) {
		if e != "" {
			r.Errors = append(r.Errors, e)
		}
	}
	s := StockRecord{ItemExternalRef: raw["item_ref"], ItemID: raw["item_id"], BatchNo: raw["batch_no"]}
	if s.ItemExternalRef == "" && s.ItemID == "" {
		add("item_ref or item_id is required")
	}
	q, err := sheet.ParseQty(raw["qty"])
	switch {
	case err != nil:
		add("qty: " + err.Error())
	case q < 0:
		add("qty: cannot be negative")
	}
	s.Qty = q
	if s.UnitCostPaise, err = sheet.ParsePaise(raw["unit_cost"]); err != nil {
		add("unit_cost: " + err.Error())
	}
	if s.ExpiryDate, err = sheet.ParseDate(raw["expiry_date"]); err != nil {
		add("expiry_date: " + err.Error())
	}
	add(v.dup("stock:"+s.ItemExternalRef+s.ItemID+"|"+s.BatchNo, rowNo, "item + batch"))
	r.Record = s
	return r
}

// ---------- field helpers ----------

// Phone normalises an Indian mobile number to +91XXXXXXXXXX.
func Phone(s string) (string, error) {
	var d strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d.WriteRune(r)
		}
	}
	n := d.String()
	n = strings.TrimPrefix(n, "0")
	if len(n) == 12 && strings.HasPrefix(n, "91") {
		n = n[2:]
	}
	if len(n) != 10 || n[0] < '6' {
		return "", fmt.Errorf("%q is not a 10-digit Indian mobile number", s)
	}
	return "+91" + n, nil
}

// HSN checks an HSN/SAC code: digits only, 4, 6 or 8 long.
func HSN(h string) error {
	for _, r := range h {
		if r < '0' || r > '9' {
			return fmt.Errorf("%q must contain digits only", h)
		}
	}
	if l := len(h); l != 4 && l != 6 && l != 8 {
		return fmt.Errorf("%q must be 4, 6 or 8 digits", h)
	}
	return nil
}

// StateCode resolves "27", "Maharashtra" or "27-Maharashtra" to a GST state code.
func StateCode(s string) string {
	t := strings.TrimSpace(s)
	if len(t) >= 2 && gst.ValidState(t[:2]) && (len(t) == 2 || !isDigit(t[2])) {
		return t[:2]
	}
	if len(t) == 1 && isDigit(t[0]) {
		t = "0" + t
		if gst.ValidState(t) {
			return t
		}
	}
	n := mapping.Normalize(t)
	for code, name := range gst.States {
		if mapping.Normalize(name) == n {
			return code
		}
	}
	return ""
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func partyType(group string) string {
	g := strings.ToLower(group)
	switch {
	case strings.Contains(g, "both"):
		return "both"
	case strings.Contains(g, "creditor"), strings.Contains(g, "supplier"), strings.Contains(g, "vendor"):
		return "supplier"
	}
	return "customer"
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// refOrName is the external_ref sent to bahi/bhandar: the MargBooks code when the
// file has one, otherwise the normalised name, so re-importing the same file
// updates rows instead of creating duplicates.
func refOrName(code, name string) string {
	if c := strings.TrimSpace(code); c != "" {
		return c
	}
	if n := strings.ToLower(collapse(name)); n != "" {
		return "name:" + n
	}
	return ""
}
