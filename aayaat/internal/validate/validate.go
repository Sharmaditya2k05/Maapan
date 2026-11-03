// Package validate turns mapped spreadsheet rows into the bulk payload records
// bahi and bhandar accept, collecting row-level errors, warnings and duplicates.
package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/aayaat/internal/sheet"
	"github.com/maapan/mool/gst"
)

// PartyRecord matches bahi POST /internal/v1/bulk/parties items. PAN and
// ShippingAddress are extras bahi does not accept yet (see clients.Fields).
type PartyRecord struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Phone               string `json:"phone,omitempty"`
	Email               string `json:"email,omitempty"`
	GSTIN               string `json:"gstin,omitempty"`
	StateCode           string `json:"state_code,omitempty"`
	Address             string `json:"address,omitempty"`
	CreditLimitPaise    int64  `json:"credit_limit_paise"`
	CreditDays          int    `json:"credit_days"`
	OpeningBalancePaise int64  `json:"opening_balance_paise"`
	OpeningType         string `json:"opening_type"`
	OpeningAsOf         string `json:"opening_as_of,omitempty"`
	ExternalRef         string `json:"external_ref,omitempty"`
	PAN                 string `json:"pan,omitempty"`
	ShippingAddress     string `json:"shipping_address,omitempty"`
}

// BalanceRecord matches bahi POST /internal/v1/bulk/opening-balances lines.
type BalanceRecord struct {
	AccountCode      string `json:"account_code,omitempty"`
	PartyExternalRef string `json:"party_external_ref,omitempty"`
	AmountPaise      int64  `json:"amount_paise"`
	Type             string `json:"type"`
	ExternalRef      string `json:"external_ref,omitempty"` // extra: stable line key
	Ledger           string `json:"ledger,omitempty"`       // aayaat only: the old system's ledger name
}

// AltUnit is an alternate unit of an item (1 BOX = 48 PCS → {BOX, 48}).
type AltUnit struct {
	Unit   string  `json:"unit"`
	Factor float64 `json:"factor"`
}

// ItemRecord matches bhandar POST /internal/v1/bulk/items items. AltUnits is
// an extra; the opening_* and store fields stay in aayaat and feed an
// opening-stock call after the item is created.
type ItemRecord struct {
	Name               string    `json:"name"`
	SKU                string    `json:"sku,omitempty"`
	Barcode            string    `json:"barcode,omitempty"`
	HSN                string    `json:"hsn,omitempty"`
	Unit               string    `json:"unit"`
	Category           string    `json:"category,omitempty"`
	Brand              string    `json:"brand,omitempty"`
	SalePricePaise     int64     `json:"sale_price_paise"`
	MRPPaise           int64     `json:"mrp_paise"`
	PurchasePricePaise int64     `json:"purchase_price_paise"`
	TaxRateBP          int       `json:"tax_rate_bp"`
	CessRateBP         int       `json:"cess_rate_bp,omitempty"`
	TrackBatches       bool      `json:"track_batches,omitempty"`
	ReorderLevel       float64   `json:"reorder_level,omitempty"`
	ReorderQty         float64   `json:"reorder_qty,omitempty"`
	ExternalRef        string    `json:"external_ref,omitempty"`
	AltUnits           []AltUnit `json:"alt_units,omitempty"`
	OpeningQty         float64   `json:"opening_qty,omitempty"`
	OpeningCostPaise   int64     `json:"opening_cost_paise,omitempty"`
	StoreID            string    `json:"store_id,omitempty"`
}

// StockRecord matches bhandar POST /internal/v1/bulk/opening-stock lines.
// StoreID groups lines into one call per store; MfgDate and MRPPaise are extras.
type StockRecord struct {
	ItemExternalRef string  `json:"item_external_ref,omitempty"`
	ItemID          string  `json:"item_id,omitempty"`
	Qty             float64 `json:"qty"`
	UnitCostPaise   int64   `json:"unit_cost_paise"`
	BatchNo         string  `json:"batch_no,omitempty"`
	ExpiryDate      string  `json:"expiry_date,omitempty"`
	MfgDate         string  `json:"mfg_date,omitempty"`
	MRPPaise        int64   `json:"mrp_paise,omitempty"`
	StoreID         string  `json:"store_id,omitempty"`
}

// BillRecord is one open (unpaid) bill of a party: the bill-by-bill
// outstanding that makes receivables/payables aging match the old books.
type BillRecord struct {
	PartyExternalRef string `json:"party_external_ref"`
	PartyName        string `json:"party_name,omitempty"`
	BillNo           string `json:"bill_no"`
	BillDate         string `json:"bill_date,omitempty"`
	DueDate          string `json:"due_date,omitempty"`
	BillAmountPaise  int64  `json:"bill_amount_paise,omitempty"`
	AmountPaise      int64  `json:"amount_paise"` // still outstanding
	Type             string `json:"type"`         // dr = receivable, cr = payable
	ExternalRef      string `json:"external_ref"`
}

// InvoiceRecord is one past sales / purchase invoice or note, imported as
// history: no stock movement, no re-posting of what the opening balances
// already contain.
type InvoiceRecord struct {
	DocType          string `json:"doc_type"` // sale | purchase | sale_return | purchase_return
	Number           string `json:"number"`
	Date             string `json:"date"`
	PartyExternalRef string `json:"party_external_ref,omitempty"`
	PartyName        string `json:"party_name,omitempty"`
	PartyGSTIN       string `json:"party_gstin,omitempty"`
	PlaceOfSupply    string `json:"place_of_supply,omitempty"`
	TaxablePaise     int64  `json:"taxable_paise"`
	CGSTPaise        int64  `json:"cgst_paise"`
	SGSTPaise        int64  `json:"sgst_paise"`
	IGSTPaise        int64  `json:"igst_paise"`
	CessPaise        int64  `json:"cess_paise"`
	RoundOffPaise    int64  `json:"round_off_paise"`
	TotalPaise       int64  `json:"total_paise"`
	DueDate          string `json:"due_date,omitempty"`
	Reference        string `json:"reference,omitempty"`
	Narration        string `json:"narration,omitempty"`
	StoreID          string `json:"store_id,omitempty"`
	ExternalRef      string `json:"external_ref"`
}

// Options carry the job parameters validation needs.
type Options struct {
	AsOf       string            // opening date YYYY-MM-DD (party opening balances, ledger lines)
	StoreID    string            // default store for stock / items with opening qty
	StoreMap   map[string]string // store / godown text → store id
	AccountMap map[string]string // old ledger or group name → bahi account code
	DocType    string            // invoices: type when the file has no type column
	Side       string            // open_bills: dr | cr when the file does not say
	Today      time.Time         // date item GST rates must be valid on (zero = now)
	// CanAccessStore reports whether the importing user may write to a store
	// (nil = any).
	CanAccessStore func(storeID string) bool
}

// Result is one validated row.
type Result struct {
	Record   any               // one of the *Record types (nil when invalid)
	Raw      map[string]string // mapped field → original text
	Errors   []string
	Warnings []string
	Skip     bool // nothing to import (zero quantity / zero balance); not an error
	// Side/Amount feed the Dr/Cr reconciliation (parties, opening_balances, open_bills).
	Side   string
	Amount int64
}

// Validator validates the rows of one import.
type Validator struct {
	Kind  string
	Index map[string]int // field → column
	Opts  Options
	seen  map[string]int // dedupe key → first row number
	soft  map[string]int // keys that only warn (shared GSTIN)
	stmap map[string]string
	acmap map[string]string
}

// New builds a validator for kind with resolved column indexes.
func New(kind string, index map[string]int, opts ...Options) *Validator {
	v := &Validator{Kind: kind, Index: index, seen: map[string]int{}, soft: map[string]int{},
		stmap: map[string]string{}, acmap: map[string]string{}}
	if len(opts) > 0 {
		v.Opts = opts[0]
	}
	for k, id := range v.Opts.StoreMap {
		v.stmap[mapping.Normalize(k)] = strings.TrimSpace(id)
	}
	for k, code := range v.Opts.AccountMap {
		v.acmap[mapping.Normalize(k)] = strings.TrimSpace(code)
	}
	return v
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
	r := &Result{}
	switch v.Kind {
	case mapping.KindParties:
		v.party(r, rowNo, raw)
	case mapping.KindItems:
		v.item(r, rowNo, raw)
	case mapping.KindOpeningBalances:
		v.balance(r, rowNo, raw)
	case mapping.KindOpeningStock:
		v.stock(r, rowNo, raw)
	case mapping.KindOpenBills:
		v.bill(r, rowNo, raw)
	case mapping.KindInvoices:
		v.invoice(r, rowNo, raw)
	default:
		r.Errors = []string{"unknown import kind"}
	}
	r.Raw = raw
	if len(r.Errors) > 0 {
		r.Record, r.Skip = nil, false
	}
	return *r
}

func (r *Result) add(e string) {
	if e != "" {
		r.Errors = append(r.Errors, e)
	}
}

func (r *Result) warn(w string) {
	if w != "" {
		r.Warnings = append(r.Warnings, w)
	}
}

// ---------- parties ----------

// rateCut2025Before is a day on which the pre-September-2025 slabs (12 %, 28 %) applied.
var rateCut2025Before = time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)

var panPattern = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

func (v *Validator) party(r *Result, rowNo int, raw map[string]string) {
	p := PartyRecord{Name: collapse(raw["name"]), ExternalRef: refOrName(raw["code"], raw["name"]), OpeningAsOf: v.Opts.AsOf}
	if p.Name == "" {
		r.add("name is required")
	}
	p.Type = partyType(raw["type"])
	if raw["phone"] != "" {
		ph, warn := Phones(raw["phone"])
		p.Phone = ph
		r.warn(warn)
	}
	if e := strings.TrimSpace(raw["email"]); e != "" {
		if first := firstEmail(e); first != "" {
			p.Email = first
		} else {
			r.warn(fmt.Sprintf("email %q is not an e-mail address; left blank", e))
		}
	}
	if g := gst.Normalize(raw["gstin"]); g != "" && g != "NA" && g != "URP" && g != "UNREGISTERED" {
		if err := gst.ValidateGSTIN(g); err != nil {
			r.add("gstin: " + err.Error())
		}
		p.GSTIN = g
		p.StateCode = gst.StateCode(g)
	}
	if pan := strings.ToUpper(strings.ReplaceAll(raw["pan"], " ", "")); pan != "" && pan != "NA" {
		switch {
		case !panPattern.MatchString(pan):
			r.add(fmt.Sprintf("pan: %q is not a PAN (AAAAA9999A)", raw["pan"]))
		case p.GSTIN != "" && len(p.GSTIN) == 15 && p.GSTIN[2:12] != pan:
			r.warn(fmt.Sprintf("PAN %s differs from the PAN inside GSTIN %s", pan, p.GSTIN))
		}
		p.PAN = pan
	} else if len(p.GSTIN) == 15 {
		p.PAN = p.GSTIN[2:12] // every GSTIN carries the holder's PAN
	}
	if s := raw["state"]; s != "" {
		code := StateCode(s)
		switch {
		case code == "":
			r.add(fmt.Sprintf("state: %q is not a known state", s))
		case p.StateCode != "" && code != p.StateCode:
			r.add(fmt.Sprintf("state %s does not match GSTIN state %s", code, p.StateCode))
		default:
			p.StateCode = code
		}
	}
	p.Address = joinAddress(raw["address"], raw["address2"], raw["city"], raw["pincode"])
	if pin := strings.ReplaceAll(raw["pincode"], " ", ""); pin != "" && !pinPattern.MatchString(pin) {
		r.warn(fmt.Sprintf("pincode %q is not a 6-digit PIN", raw["pincode"]))
	}
	p.ShippingAddress = collapse(raw["shipping_address"])
	if raw["credit_limit"] != "" {
		lim, err := sheet.ParsePaise(raw["credit_limit"])
		if err != nil {
			r.add("credit_limit: " + err.Error())
		}
		p.CreditLimitPaise = abs(lim) // Tally stores limits negative (Dr side)
	}
	if raw["credit_days"] != "" {
		d, err := Days(raw["credit_days"])
		if err != nil {
			r.add("credit_days: " + err.Error())
		}
		p.CreditDays = d
	}
	amt, side, err := sheet.ParseDrCr(raw["opening_balance"])
	if err != nil {
		r.add("opening_balance: " + err.Error())
	}
	if s := sheet.NormalizeSide(raw["dr_cr"]); s != "" {
		side = s
	} else if raw["dr_cr"] != "" {
		r.add(fmt.Sprintf("dr_cr: %q must be Dr or Cr", raw["dr_cr"]))
	}
	if side == "" {
		side = "dr"
		if p.Type == "supplier" {
			side = "cr"
		}
	}
	p.OpeningBalancePaise, p.OpeningType = amt, side
	r.add(v.dup("name:"+mapping.Normalize(p.Name), rowNo, "party name"))
	r.add(v.dup("code:"+strings.TrimSpace(raw["code"]), rowNo, "party code"))
	// Branch ledgers of one business legitimately share a GSTIN (one per state,
	// not per branch): keep them as separate parties, but say so.
	if p.GSTIN != "" {
		if first, ok := v.soft["gstin:"+p.GSTIN]; ok {
			r.warn(fmt.Sprintf("GSTIN %s is also on row %d (branch ledgers sharing a GSTIN are kept as separate parties)", p.GSTIN, first))
		} else {
			v.soft["gstin:"+p.GSTIN] = rowNo
		}
	}
	r.Record, r.Side, r.Amount = p, side, amt
}

var pinPattern = regexp.MustCompile(`^[1-9][0-9]{5}$`)

func joinAddress(line1, line2, city, pin string) string {
	var parts []string
	for _, s := range []string{line1, line2} {
		if c := collapse(s); c != "" {
			parts = append(parts, c)
		}
	}
	c := collapse(city)
	pin = strings.ReplaceAll(pin, " ", "")
	switch {
	case c != "" && pin != "":
		parts = append(parts, c+" - "+pin)
	case c != "":
		parts = append(parts, c)
	case pin != "":
		parts = append(parts, pin)
	}
	return strings.Join(parts, ", ")
}

func firstEmail(s string) string {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '/' }) {
		at := strings.Index(f, "@")
		if at > 0 && strings.Contains(f[at:], ".") && !strings.HasSuffix(f, ".") {
			return strings.ToLower(f)
		}
	}
	return ""
}

// ---------- items ----------

func (v *Validator) item(r *Result, rowNo int, raw map[string]string) {
	it := ItemRecord{Name: collapse(raw["name"]), SKU: strings.TrimSpace(raw["sku"]), Barcode: strings.ReplaceAll(raw["barcode"], " ", ""),
		ExternalRef: refOrName(raw["code"], raw["name"]), Unit: Unit(raw["unit"]),
		Category: collapse(raw["category"]), Brand: collapse(raw["brand"])}
	if it.Name == "" {
		r.add("name is required")
	}
	if it.Unit == "" {
		it.Unit = "PCS"
	}
	if h := strings.ReplaceAll(raw["hsn"], " ", ""); h != "" {
		if err := HSN(h); err != nil {
			r.add("hsn: " + err.Error())
		}
		it.HSN = h
	}
	money := func(field string, dst *int64) {
		p, err := sheet.ParsePaise(raw[field])
		if err != nil {
			r.add(field + ": " + err.Error())
		} else if p < 0 {
			r.add(field + ": cannot be negative")
		}
		*dst = p
	}
	money("sale_price", &it.SalePricePaise)
	money("mrp", &it.MRPPaise)
	money("purchase_price", &it.PurchasePricePaise)
	if it.MRPPaise > 0 && it.SalePricePaise > it.MRPPaise {
		// Legacy exports often carry a stale MRP; bhandar caps the price at MRP.
		r.warn(fmt.Sprintf("sale_price %s is above MRP %s; it will be capped at MRP", rupees(it.SalePricePaise), rupees(it.MRPPaise)))
		it.SalePricePaise = it.MRPPaise
	}
	bp, err := sheet.ParseRateBP(raw["tax_rate"])
	if err != nil {
		r.add("tax_rate: " + err.Error())
	} else if !gst.ValidRate(bp, v.Opts.Today) {
		if gst.ValidRate(bp, rateCut2025Before) {
			r.add(fmt.Sprintf("tax_rate: %.0f%% has not been a GST slab since 22 Sep 2025 (56th GST Council); set the item's current rate", float64(bp)/100))
		} else {
			r.add(fmt.Sprintf("tax_rate: %.2f%% is not a GST slab", float64(bp)/100))
		}
	}
	it.TaxRateBP = bp
	if raw["cess_rate"] != "" {
		c, err := sheet.ParseRateBP(raw["cess_rate"])
		if err != nil {
			r.add("cess_rate: " + err.Error())
		}
		it.CessRateBP = c
	}
	if raw["track_batches"] != "" {
		b, err := Bool(raw["track_batches"])
		if err != nil {
			r.add("track_batches: " + err.Error())
		}
		it.TrackBatches = b
	}
	qty := func(field string, dst *float64) {
		q, err := sheet.ParseQty(raw[field])
		switch {
		case err != nil:
			r.add(field + ": " + err.Error())
		case q < 0:
			r.add(field + ": cannot be negative")
		}
		*dst = q
	}
	qty("reorder_level", &it.ReorderLevel)
	qty("reorder_qty", &it.ReorderQty)
	if alt := Unit(raw["alt_unit"]); alt != "" {
		f, err := Factor(raw["alt_factor"])
		switch {
		case err != nil:
			r.add("alt_factor: " + err.Error())
		case alt == it.Unit:
			r.add(fmt.Sprintf("alt_unit %s is the base unit", alt))
		default:
			it.AltUnits = []AltUnit{{Unit: alt, Factor: f}}
		}
	}
	qty("opening_qty", &it.OpeningQty)
	if it.OpeningQty > 0 {
		cost, err := sheet.ParsePaise(raw["opening_rate"])
		if err != nil {
			r.add("opening_rate: " + err.Error())
		}
		if cost <= 0 {
			cost = it.PurchasePricePaise
		}
		it.OpeningCostPaise = cost
		store, err := v.store(raw["store"])
		switch {
		case err != nil:
			r.add(err.Error())
		case store == "":
			// An item master often carries a closing-stock column; without a
			// store it cannot become opening stock, but the item still imports.
			r.warn("opening_qty ignored: set params.store_id (or map the Store column) to import it as opening stock")
			it.OpeningQty, it.OpeningCostPaise = 0, 0
		}
		it.StoreID = store
	}
	r.add(v.dup("name:"+mapping.Normalize(it.Name), rowNo, "item name"))
	r.add(v.dup("barcode:"+it.Barcode, rowNo, "barcode"))
	r.add(v.dup("code:"+strings.TrimSpace(raw["code"]), rowNo, "item code"))
	r.Record = it
}

// ---------- opening balances (non-party ledgers, or parties by code) ----------

func (v *Validator) balance(r *Result, rowNo int, raw map[string]string) {
	b := BalanceRecord{AccountCode: strings.TrimSpace(raw["account_code"]), PartyExternalRef: strings.TrimSpace(raw["party_ref"]),
		Ledger: collapse(raw["account_name"])}
	amt, side, err := sheet.ParseDrCr(raw["amount"])
	if err != nil {
		r.add("amount: " + err.Error())
	}
	if s := sheet.NormalizeSide(raw["dr_cr"]); s != "" {
		side = s
	} else if raw["dr_cr"] != "" {
		r.add(fmt.Sprintf("dr_cr: %q must be Dr or Cr", raw["dr_cr"]))
	}
	if side == "" {
		side = "dr"
	}
	b.AmountPaise, b.Type = amt, side
	if amt == 0 && len(r.Errors) == 0 {
		// Most ledgers of a Tally / MargBooks chart (sales, expenses, taxes)
		// carry no opening balance; they need no mapping.
		r.Skip = true
		r.warn("zero balance; nothing to import")
		r.Record = b
		return
	}
	if b.PartyExternalRef == "" && raw["party_name"] != "" {
		b.PartyExternalRef = refOrName("", raw["party_name"])
	}
	if b.AccountCode == "" && b.PartyExternalRef == "" {
		code, how := v.account(b.Ledger, raw["group"])
		switch how {
		case accountParty:
			b.PartyExternalRef = refOrName("", b.Ledger)
		case accountSkip:
			r.Skip = true
			r.warn(code)
			code = ""
		case accountMissing:
			if b.Ledger == "" {
				r.add("account_code, account_name or party_ref is required")
			} else {
				r.add(fmt.Sprintf("ledger %q: map it to a Maapan account code in params.account_map", b.Ledger))
			}
			code = ""
		}
		b.AccountCode = code
	}
	key := "acct:" + b.AccountCode
	if b.PartyExternalRef != "" {
		key = "party:" + b.PartyExternalRef
	}
	if !r.Skip {
		// bahi books one opening line per account and as-of date; a second line
		// for the same account would be silently ignored, so refuse it here.
		r.add(v.dup(key, rowNo, "account (two ledgers map to the same account; sum them or map one elsewhere)"))
	}
	b.ExternalRef = "ob:" + v.Opts.AsOf + ":" + key
	r.Record, r.Side, r.Amount = b, side, amt
}

type accountHow int

const (
	accountCode accountHow = iota
	accountParty
	accountSkip
	accountMissing
)

// account resolves an old ledger to a bahi account code: params.account_map by
// ledger name, then by group, then the defaults for Tally / MargBooks groups.
func (v *Validator) account(ledger, group string) (string, accountHow) {
	if c, ok := v.acmap[mapping.Normalize(ledger)]; ok && ledger != "" {
		return c, accountCode
	}
	if c, ok := v.acmap[mapping.Normalize(group)]; ok && group != "" {
		return c, accountCode
	}
	g := mapping.Normalize(group)
	l := mapping.Normalize(ledger)
	switch {
	case g == "sundrydebtors" || g == "sundrycreditors":
		return "", accountParty
	case g == "stockinhand" || l == "closingstock" || l == "openingstock":
		return "stock is imported with opening stock (bhandar posts its value); ledger skipped", accountSkip
	case l == "profitlossac" || l == "profitandlossac" || l == "profitloss" || l == "profitandloss":
		return "profit & loss balance is carried by Opening Balance Difference; ledger skipped", accountSkip
	case g == "cashinhand" || l == "cash" || l == "cashinhand":
		return "1100", accountCode
	case g == "bankaccounts" || g == "bankodac" || g == "bankoccac":
		return "1110", accountCode
	case g == "capitalaccount" || l == "capitalaccount" || l == "capital":
		return "3100", accountCode
	}
	return "", accountMissing
}

// ---------- opening stock ----------

func (v *Validator) stock(r *Result, rowNo int, raw map[string]string) {
	s := StockRecord{ItemExternalRef: strings.TrimSpace(raw["item_ref"]), ItemID: strings.TrimSpace(raw["item_id"]),
		BatchNo: strings.TrimSpace(raw["batch_no"])}
	if s.ItemExternalRef == "" && s.ItemID == "" && raw["item_name"] != "" {
		s.ItemExternalRef = refOrName("", raw["item_name"])
	}
	if s.ItemExternalRef == "" && s.ItemID == "" {
		r.add("item_ref, item_name or item_id is required")
	}
	q, err := sheet.ParseQty(raw["qty"])
	switch {
	case err != nil:
		r.add("qty: " + err.Error())
	case q < 0:
		r.add("qty: cannot be negative (fix negative stock in the old books first)")
	case q == 0:
		r.Skip = true
		r.warn("zero quantity; nothing to import")
	}
	s.Qty = q
	if s.UnitCostPaise, err = sheet.ParsePaise(raw["unit_cost"]); err != nil {
		r.add("unit_cost: " + err.Error())
	}
	if s.UnitCostPaise == 0 && raw["value"] != "" && q > 0 {
		val, err := sheet.ParsePaise(raw["value"])
		if err != nil {
			r.add("value: " + err.Error())
		}
		s.UnitCostPaise = int64(float64(abs(val))/q + 0.5)
	}
	if s.UnitCostPaise < 0 {
		r.add("unit_cost: cannot be negative")
	}
	if s.ExpiryDate, err = sheet.ParseDate(raw["expiry_date"]); err != nil {
		r.add("expiry_date: " + err.Error())
	}
	if s.MfgDate, err = sheet.ParseDate(raw["mfg_date"]); err != nil {
		r.add("mfg_date: " + err.Error())
	}
	if s.MfgDate != "" && s.ExpiryDate != "" && s.MfgDate > s.ExpiryDate {
		r.add("mfg_date is after expiry_date")
	}
	if s.MRPPaise, err = sheet.ParsePaise(raw["mrp"]); err != nil {
		r.add("mrp: " + err.Error())
	}
	store, err := v.store(raw["store"])
	switch {
	case err != nil:
		r.add(err.Error())
	case store == "":
		r.add("store is required: set params.store_id or map the Store / Godown column")
	}
	s.StoreID = store
	if !r.Skip {
		r.add(v.dup("stock:"+s.StoreID+"|"+s.ItemExternalRef+s.ItemID+"|"+strings.ToUpper(s.BatchNo), rowNo, "store + item + batch"))
	}
	r.Record = s
}

// store resolves a row's store text to a store id.
func (v *Validator) store(text string) (string, error) {
	t := strings.TrimSpace(text)
	id := v.Opts.StoreID
	if t != "" {
		switch mapped, ok := v.stmap[mapping.Normalize(t)]; {
		case ok:
			id = mapped
		case uuidPattern.MatchString(strings.ToLower(t)):
			id = strings.ToLower(t)
		case v.Opts.StoreID != "" && len(v.stmap) == 0:
			// Single-store import: a godown column ("Main Location") with no
			// store_map means everything goes to params.store_id.
		default:
			return "", fmt.Errorf("store %q is not mapped; add it to params.store_map", t)
		}
	}
	if id != "" && v.Opts.CanAccessStore != nil && !v.Opts.CanAccessStore(id) {
		return "", fmt.Errorf("no access to store %s", id)
	}
	return id, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ---------- open bills ----------

func (v *Validator) bill(r *Result, rowNo int, raw map[string]string) {
	b := BillRecord{PartyExternalRef: strings.TrimSpace(raw["party_ref"]), PartyName: collapse(raw["party_name"]),
		BillNo: collapse(raw["bill_no"])}
	if b.PartyExternalRef == "" {
		b.PartyExternalRef = refOrName("", b.PartyName)
	}
	if b.PartyExternalRef == "" {
		r.add("party_ref or party_name is required")
	}
	if b.BillNo == "" {
		r.add("bill_no is required")
	}
	var err error
	if b.BillDate, err = sheet.ParseDate(raw["bill_date"]); err != nil {
		r.add("bill_date: " + err.Error())
	}
	if b.DueDate, err = sheet.ParseDate(raw["due_date"]); err != nil {
		r.add("due_date: " + err.Error())
	}
	if b.DueDate == "" && b.BillDate != "" && raw["credit_days"] != "" {
		d, err := Days(raw["credit_days"])
		if err != nil {
			r.add("credit_days: " + err.Error())
		} else if t, perr := time.Parse(time.DateOnly, b.BillDate); perr == nil {
			b.DueDate = t.AddDate(0, 0, d).Format(time.DateOnly)
		}
	}
	if b.DueDate != "" && b.BillDate != "" && b.DueDate < b.BillDate {
		r.add("due_date is before bill_date")
	}
	if b.BillDate != "" && v.Opts.AsOf != "" && b.BillDate > v.Opts.AsOf {
		r.warn(fmt.Sprintf("bill dated %s is after the opening date %s; import it as an invoice instead", b.BillDate, v.Opts.AsOf))
	}
	if raw["bill_amount"] != "" {
		ba, _, err := sheet.ParseDrCr(raw["bill_amount"])
		if err != nil {
			r.add("bill_amount: " + err.Error())
		}
		b.BillAmountPaise = ba
	}
	amt, side, err := sheet.ParseDrCr(raw["amount"])
	if err != nil {
		r.add("amount: " + err.Error())
	}
	if s := sheet.NormalizeSide(raw["dr_cr"]); s != "" {
		side = s
	} else if raw["dr_cr"] != "" {
		r.add(fmt.Sprintf("dr_cr: %q must be Dr or Cr", raw["dr_cr"]))
	}
	if side == "" {
		side = v.Opts.Side
	}
	if side == "" {
		side = "dr"
	}
	if amt == 0 && len(r.Errors) == 0 {
		r.Skip = true
		r.warn("bill fully settled; nothing outstanding")
	}
	if b.BillAmountPaise > 0 && amt > b.BillAmountPaise {
		r.warn("pending amount is larger than the bill amount")
	}
	b.AmountPaise, b.Type = amt, side
	b.ExternalRef = "bill:" + b.PartyExternalRef + "/" + strings.ToUpper(b.BillNo)
	if !r.Skip {
		r.add(v.dup(b.ExternalRef, rowNo, "bill for this party"))
	}
	r.Record, r.Side, r.Amount = b, side, amt
}

// ---------- invoice history ----------

func (v *Validator) invoice(r *Result, rowNo int, raw map[string]string) {
	in := InvoiceRecord{Number: collapse(raw["number"]), PartyExternalRef: strings.TrimSpace(raw["party_ref"]),
		PartyName: collapse(raw["party_name"]), Reference: collapse(raw["reference"]), Narration: collapse(raw["narration"])}
	dt := raw["doc_type"]
	if dt == "" {
		dt = v.Opts.DocType
	}
	in.DocType = DocType(dt)
	if in.DocType == "" {
		r.add(fmt.Sprintf("doc_type: %q is not sale, purchase, credit note or debit note", dt))
	}
	if in.Number == "" {
		r.add("number is required")
	}
	var err error
	if in.Date, err = sheet.ParseDate(raw["date"]); err != nil {
		r.add("date: " + err.Error())
	} else if in.Date == "" {
		r.add("date is required")
	}
	if in.DueDate, err = sheet.ParseDate(raw["due_date"]); err != nil {
		r.add("due_date: " + err.Error())
	}
	if in.PartyExternalRef == "" && in.PartyName != "" && !isCashParty(in.PartyName) {
		in.PartyExternalRef = refOrName("", in.PartyName)
	}
	if g := gst.Normalize(raw["party_gstin"]); g != "" && g != "NA" && g != "URP" {
		if err := gst.ValidateGSTIN(g); err != nil {
			r.add("party_gstin: " + err.Error())
		}
		in.PartyGSTIN = g
	}
	if p := raw["place_of_supply"]; p != "" {
		if in.PlaceOfSupply = StateCode(p); in.PlaceOfSupply == "" {
			r.add(fmt.Sprintf("place_of_supply: %q is not a known state", p))
		}
	} else if len(in.PartyGSTIN) == 15 {
		in.PlaceOfSupply = in.PartyGSTIN[:2]
	}
	amounts := map[string]*int64{"taxable": &in.TaxablePaise, "cgst": &in.CGSTPaise, "sgst": &in.SGSTPaise,
		"igst": &in.IGSTPaise, "cess": &in.CessPaise, "round_off": &in.RoundOffPaise, "total": &in.TotalPaise}
	for _, f := range []string{"taxable", "cgst", "sgst", "igst", "cess", "round_off", "total"} {
		p, err := sheet.ParsePaise(raw[f])
		if err != nil {
			r.add(f + ": " + err.Error())
		}
		*amounts[f] = p
	}
	// Returns are often exported with negative amounts; history stores magnitudes.
	if in.TotalPaise < 0 || (in.TotalPaise == 0 && in.TaxablePaise < 0) {
		for _, f := range []string{"taxable", "cgst", "sgst", "igst", "cess", "total"} {
			*amounts[f] = -*amounts[f]
		}
		in.RoundOffPaise = -in.RoundOffPaise
	}
	for _, f := range []string{"taxable", "cgst", "sgst", "igst", "cess"} {
		if *amounts[f] < 0 {
			r.add(f + ": cannot be negative")
		}
	}
	tax := in.CGSTPaise + in.SGSTPaise + in.IGSTPaise + in.CessPaise
	_, haveTaxable := v.Index["taxable"]
	_, haveTotal := v.Index["total"]
	_, haveRound := v.Index["round_off"]
	switch {
	case !haveTotal || raw["total"] == "":
		in.TotalPaise = in.TaxablePaise + tax + in.RoundOffPaise
	case !haveTaxable || raw["taxable"] == "":
		in.TaxablePaise = in.TotalPaise - tax - in.RoundOffPaise
	default:
		diff := in.TotalPaise - (in.TaxablePaise + tax + in.RoundOffPaise)
		switch {
		case diff == 0:
		case !haveRound && abs(diff) <= 100:
			in.RoundOffPaise = diff // the register left out its round-off column
		default:
			r.add(fmt.Sprintf("taxable + taxes + round off = %s but total is %s",
				rupees(in.TaxablePaise+tax+in.RoundOffPaise), rupees(in.TotalPaise)))
		}
	}
	if in.TotalPaise <= 0 && len(r.Errors) == 0 {
		r.add("total must be positive")
	}
	if in.IGSTPaise != 0 && (in.CGSTPaise != 0 || in.SGSTPaise != 0) {
		r.add("an invoice has either IGST or CGST + SGST, not both")
	}
	if d := in.CGSTPaise - in.SGSTPaise; abs(d) > 1 {
		r.warn("CGST and SGST differ")
	}
	if abs(in.RoundOffPaise) > 100 {
		r.warn(fmt.Sprintf("round off %s is more than ₹1", rupees(in.RoundOffPaise)))
	}
	store, err := v.store(raw["store"])
	if err != nil {
		r.add(err.Error())
	}
	in.StoreID = store
	// Supplier bill numbers repeat across suppliers; our own series do not.
	key := "inv:" + in.DocType + ":" + strings.ToUpper(in.Number) + ":" + in.Date
	if in.DocType == "purchase" || in.DocType == "purchase_return" {
		key += ":" + in.PartyExternalRef
	}
	in.ExternalRef = key
	r.add(v.dup(key, rowNo, "invoice"))
	r.Record = in
}

func isCashParty(name string) bool {
	switch mapping.Normalize(name) {
	case "cash", "cashsale", "cashsales", "cashinhand", "walkin", "walkincustomer", "counter", "cashpurchase":
		return true
	}
	return false
}

// DocType normalises a voucher / bill type to sale | purchase | sale_return | purchase_return.
func DocType(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	switch {
	case l == "sale", l == "purchase", l == "sale_return", l == "purchase_return":
		return l
	case strings.Contains(l, "credit note"), strings.Contains(l, "sales return"), strings.Contains(l, "sale return"), l == "cn", l == "sr":
		return "sale_return"
	case strings.Contains(l, "debit note"), strings.Contains(l, "purchase return"), strings.Contains(l, "purc return"), l == "dn", l == "pr":
		return "purchase_return"
	case strings.Contains(l, "purc"), strings.Contains(l, "purchase"), l == "p", l == "pb":
		return "purchase"
	case strings.Contains(l, "sale"), strings.Contains(l, "invoice"), strings.Contains(l, "bill"), l == "s", l == "si":
		return "sale"
	}
	return ""
}

// ---------- field helpers ----------

// Phone normalises one Indian phone number: a mobile (+91XXXXXXXXXX) or a
// landline with its STD code ("022-2345 6789" → +912223456789).
func Phone(s string) (string, error) {
	var d strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d.WriteRune(r)
		}
	}
	n := d.String()
	switch {
	case len(n) == 12 && strings.HasPrefix(n, "91"):
		n = n[2:]
	case len(n) == 13 && strings.HasPrefix(n, "091"):
		n = n[3:]
	case len(n) == 14 && strings.HasPrefix(n, "0091"):
		n = n[4:]
	case len(n) == 11 && n[0] == '0':
		n = n[1:]
	}
	if len(n) != 10 || n[0] < '2' {
		return "", fmt.Errorf("%q is not a 10-digit Indian mobile or an STD-code landline", s)
	}
	return "+91" + n, nil
}

// Phones picks the first usable number from a cell that may hold several
// ("98765 43210 / 022-23456789"), preferring a mobile. An unusable cell never
// fails the row: the phone is left blank with a warning.
func Phones(s string) (phone, warning string) {
	var landline string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '/' || r == ';' || r == '|' || r == '&' }) {
		p, err := Phone(part)
		if err != nil {
			continue
		}
		if p[3] >= '6' {
			return p, ""
		}
		if landline == "" {
			landline = p
		}
	}
	if landline != "" {
		return landline, ""
	}
	return "", fmt.Sprintf("phone %q is not a usable Indian number; left blank", s)
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

// Bool reads Yes/No style flags.
func Bool(s string) (bool, error) {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), ".")) {
	case "", "no", "n", "false", "0", "off", "na":
		return false, nil
	case "yes", "y", "true", "1", "on", "batch", "batchwise":
		return true, nil
	}
	return false, fmt.Errorf("%q must be Yes or No", s)
}

// Days reads a whole number of days ("30", "30 Days").
func Days(s string) (int, error) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return 0, nil
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n < 0 || n > 3650 {
		return 0, fmt.Errorf("%q is not a number of days", s)
	}
	return n, nil
}

var numberIn = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// Factor reads an alternate-unit conversion: "48", "48 PCS", "1 BOX = 48 PCS",
// "2 BOX = 96 PCS" → base units per alternate unit.
func Factor(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("give the conversion (base units per alternate unit) for the alt unit")
	}
	left, right, eq := strings.Cut(s, "=")
	var num, den float64 = 0, 1
	if eq {
		l, r := numberIn.FindString(left), numberIn.FindString(right)
		if r == "" {
			return 0, fmt.Errorf("%q is not a conversion", s)
		}
		num, _ = strconv.ParseFloat(r, 64)
		if l != "" {
			den, _ = strconv.ParseFloat(l, 64)
		}
	} else {
		n := numberIn.FindString(s)
		if n == "" {
			return 0, fmt.Errorf("%q is not a conversion", s)
		}
		num, _ = strconv.ParseFloat(n, 64)
	}
	if num <= 0 || den <= 0 {
		return 0, fmt.Errorf("%q must be positive", s)
	}
	return num / den, nil
}

var unitAliases = map[string]string{
	"NOS": "PCS", "NO": "PCS", "NUMBERS": "PCS", "PIECES": "PCS", "PIECE": "PCS", "PC": "PCS", "PCS.": "PCS", "PCS": "PCS",
	"KGS": "KG", "KILOGRAM": "KG", "KILO": "KG", "GMS": "GM", "GRAM": "GM", "GRAMS": "GM", "GRM": "GM",
	"LTRS": "LTR", "LITRE": "LTR", "LITRES": "LTR", "LT": "LTR", "L": "LTR", "MLS": "ML",
	"DOZEN": "DOZ", "DZN": "DOZ", "BOXES": "BOX", "BX": "BOX", "MTRS": "MTR", "METER": "MTR", "METRE": "MTR",
}

// Unit upper-cases a unit and maps common spellings to bhandar's default units.
func Unit(s string) string {
	u := strings.ToUpper(strings.TrimSpace(s))
	if m, ok := unitAliases[u]; ok {
		return m
	}
	return u
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
	case strings.Contains(g, "creditor"), strings.Contains(g, "supplier"), strings.Contains(g, "vendor"), strings.Contains(g, "payable"):
		return "supplier"
	}
	return "customer"
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func rupees(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	return fmt.Sprintf("%s₹%d.%02d", sign, p/100, p%100)
}

// RefOrName is the external_ref sent to bahi/bhandar: the old system's code
// when the file has one, otherwise the normalised name, so re-importing the
// same file updates rows instead of creating duplicates, and other files
// (opening stock, bills, invoices) can point at the record by name.
func RefOrName(code, name string) string { return refOrName(code, name) }

func refOrName(code, name string) string {
	if c := strings.TrimSpace(code); c != "" {
		return c
	}
	if n := strings.ToLower(collapse(name)); n != "" {
		return "name:" + n
	}
	return ""
}
