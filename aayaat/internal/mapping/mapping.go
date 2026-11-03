// Package mapping knows the target fields of each import kind and suggests
// which spreadsheet column feeds which field, by fuzzy-matching headers such
// as MargBooks' "Party Name", "GSTIN No", "Opening Bal", "Purc Rate" and the
// column names aayaat gives the tables of a Tally XML export.
package mapping

import (
	"sort"
	"strings"
	"unicode"
)

// Import kinds.
const (
	KindParties         = "parties"
	KindItems           = "items"
	KindOpeningBalances = "opening_balances"
	KindOpeningStock    = "opening_stock"
	KindOpenBills       = "open_bills" // bill-by-bill outstanding per party
	KindInvoices        = "invoices"   // past sales / purchase invoices (history)
)

// Kinds lists every import kind in display order.
var Kinds = []string{KindParties, KindItems, KindOpeningBalances, KindOpeningStock, KindOpenBills, KindInvoices}

// Field is one target field of an import kind.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Aliases  []string `json:"-"`
	Example  string   `json:"example"`
}

var storeField = Field{Key: "store", Label: "Store", Example: "MAIN",
	Aliases: []string{"store", "storecode", "storename", "storeid", "godown", "godownname", "location", "branch", "outlet"}}

// Fields lists the target fields per kind, in template column order.
var Fields = map[string][]Field{
	KindParties: {
		{Key: "name", Label: "Party Name", Required: true, Example: "Sharma General Store",
			Aliases: []string{"partyname", "name", "accountname", "ledgername", "customername", "suppliername", "party", "account", "ledger", "particulars"}},
		{Key: "code", Label: "Party Code", Example: "C0001",
			Aliases: []string{"code", "partycode", "accountcode", "ledgercode", "alias", "acode", "custcode"}},
		{Key: "type", Label: "Group", Example: "Sundry Debtors",
			Aliases: []string{"group", "accountgroup", "undergroup", "under", "partytype", "type", "groupname"}},
		{Key: "phone", Label: "Mobile", Example: "9876543210",
			Aliases: []string{"mobile", "mobileno", "phone", "phoneno", "contact", "contactno", "mob", "mobilenumber", "telephone", "phone1", "telno"}},
		{Key: "email", Label: "Email", Example: "accounts@sharmastore.in",
			Aliases: []string{"email", "emailid", "mail", "emailaddress", "eml"}},
		{Key: "gstin", Label: "GSTIN No", Example: "27AAPFU0939F1ZV",
			Aliases: []string{"gstin", "gstinno", "gstno", "gst", "gstnumber", "gstinuin", "gstregno"}},
		{Key: "pan", Label: "PAN", Example: "AAPFU0939F",
			Aliases: []string{"pan", "panno", "pannumber", "itpan", "incometaxno", "panitno"}},
		{Key: "state", Label: "State", Example: "Maharashtra",
			Aliases: []string{"state", "statename", "statecode", "st"}},
		{Key: "address", Label: "Address", Example: "12 MG Road",
			Aliases: []string{"address", "address1", "addr", "add1", "add", "fulladdress", "billingaddress", "addressline1"}},
		{Key: "address2", Label: "Address 2", Example: "Near Bus Stand",
			Aliases: []string{"address2", "add2", "addr2", "addressline2", "address3", "area"}},
		{Key: "city", Label: "City", Example: "Pune", Aliases: []string{"city", "town", "cityname", "district"}},
		{Key: "pincode", Label: "Pincode", Example: "411001",
			Aliases: []string{"pincode", "pin", "pinno", "postalcode", "zip", "zipcode"}},
		{Key: "shipping_address", Label: "Shipping Address", Example: "Godown 4, Hadapsar, Pune 411028",
			Aliases: []string{"shippingaddress", "shipto", "deliveryaddress", "shipaddress", "consigneeaddress"}},
		{Key: "credit_limit", Label: "Credit Limit", Example: "50,000",
			Aliases: []string{"creditlimit", "crlimit", "limit", "creditamt", "creditamount"}},
		{Key: "credit_days", Label: "Credit Days", Example: "30",
			Aliases: []string{"creditdays", "crdays", "creditperiod", "duedays", "paymentterms"}},
		{Key: "opening_balance", Label: "Opening Bal", Example: "1,200.50 Dr",
			Aliases: []string{"openingbal", "openingbalance", "opbal", "opening", "balance", "opbalance", "opbalamt", "closingbalance"}},
		{Key: "dr_cr", Label: "Dr/Cr", Example: "Dr",
			Aliases: []string{"drcr", "balancetype", "baltype", "crdr", "side"}},
	},
	KindItems: {
		{Key: "name", Label: "Item Name", Required: true, Example: "Parle-G 100g",
			Aliases: []string{"itemname", "productname", "name", "item", "product", "description", "itemdescription", "particulars", "stockitem"}},
		{Key: "code", Label: "Item Code", Example: "I0001",
			Aliases: []string{"code", "itemcode", "productcode", "alias", "icode"}},
		{Key: "sku", Label: "SKU", Example: "PG-100", Aliases: []string{"sku", "skucode", "partno", "partnumber"}},
		{Key: "barcode", Label: "Barcode", Example: "8901719110016",
			Aliases: []string{"barcode", "ean", "upc", "barcodeno", "eancode"}},
		{Key: "hsn", Label: "HSN Code", Example: "1905",
			Aliases: []string{"hsn", "hsncode", "hsnsac", "hsnsaccode", "sac", "hsnno"}},
		{Key: "category", Label: "Category", Example: "Biscuits",
			Aliases: []string{"category", "itemgroup", "stockgroup", "group", "productgroup", "itemcategory", "department", "dept"}},
		{Key: "brand", Label: "Brand", Example: "Parle",
			Aliases: []string{"brand", "company", "mfr", "manufacturer", "make", "brandname", "companyname"}},
		{Key: "unit", Label: "Unit", Example: "PCS",
			Aliases: []string{"unit", "uom", "units", "unitname", "primaryunit", "baseunit", "baseunits"}},
		{Key: "alt_unit", Label: "Alt Unit", Example: "BOX",
			Aliases: []string{"altunit", "alternateunit", "secondaryunit", "additionalunit", "altuom", "unit2"}},
		{Key: "alt_factor", Label: "Conversion", Example: "48",
			Aliases: []string{"conversion", "conversionfactor", "factor", "altfactor", "packing", "pack", "perbox", "unitconversion", "convfactor"}},
		{Key: "sale_price", Label: "Sale Rate", Example: "10.00",
			Aliases: []string{"salerate", "saleprice", "sellingprice", "rate", "srate", "salesrate", "sprice"}},
		{Key: "mrp", Label: "MRP", Example: "10.00", Aliases: []string{"mrp", "maxretailprice"}},
		{Key: "purchase_price", Label: "Purc Rate", Example: "8.50",
			Aliases: []string{"purcrate", "purchaserate", "purchaseprice", "prate", "costprice", "cost", "pprice", "purrate"}},
		{Key: "tax_rate", Label: "GST %", Example: "5",
			Aliases: []string{"gst", "gstrate", "taxrate", "tax", "igst", "igstrate", "gstpercent", "taxpercent"}},
		{Key: "cess_rate", Label: "Cess %", Example: "0",
			Aliases: []string{"cess", "cessrate", "cesspercent", "cessper"}},
		{Key: "track_batches", Label: "Batch Wise", Example: "No",
			Aliases: []string{"batchwise", "batchwiseon", "batchflag", "maintainbatch", "batchyn", "batchapplicable"}},
		{Key: "reorder_level", Label: "Reorder Level", Example: "24",
			Aliases: []string{"reorderlevel", "minstock", "minimumstock", "reorder", "minqty", "rol"}},
		{Key: "reorder_qty", Label: "Reorder Qty", Example: "96",
			Aliases: []string{"reorderqty", "orderqty", "reorderquantity", "maxstock", "eoq"}},
		{Key: "opening_qty", Label: "Opening Qty", Example: "120",
			Aliases: []string{"openingqty", "opqty", "openingstock", "opstock", "closingstock", "clstock", "stockqty", "qty", "currentstock"}},
		{Key: "opening_rate", Label: "Opening Rate", Example: "8.50",
			Aliases: []string{"openingrate", "oprate", "stockrate", "valuationrate"}},
		storeField,
	},
	KindOpeningBalances: {
		{Key: "account_code", Label: "Account Code", Example: "1100",
			Aliases: []string{"accountcode", "ledgercode", "acccode", "glcode"}},
		{Key: "account_name", Label: "Ledger Name", Example: "Cash",
			Aliases: []string{"ledgername", "accountname", "ledger", "account", "accounthead", "head", "particulars", "name"}},
		{Key: "group", Label: "Group", Example: "Cash-in-Hand",
			Aliases: []string{"group", "undergroup", "under", "groupname", "accountgroup"}},
		{Key: "party_ref", Label: "Party Code", Example: "",
			Aliases: []string{"partycode", "partyref", "code", "party"}},
		{Key: "party_name", Label: "Party Name", Example: "", Aliases: []string{"partyname", "customername", "suppliername"}},
		{Key: "amount", Label: "Opening Bal", Required: true, Example: "1,200.50",
			Aliases: []string{"amount", "balance", "openingbal", "openingbalance", "opbal", "amt", "closingbalance"}},
		{Key: "dr_cr", Label: "Dr/Cr", Example: "Dr", Aliases: []string{"drcr", "balancetype", "baltype", "crdr", "side", "type"}},
	},
	KindOpeningStock: {
		{Key: "item_ref", Label: "Item Code", Example: "I0001",
			Aliases: []string{"itemcode", "code", "productcode", "alias", "icode"}},
		{Key: "item_name", Label: "Item Name", Example: "Parle-G 100g",
			Aliases: []string{"itemname", "item", "productname", "product", "description", "particulars", "stockitem", "name"}},
		{Key: "item_id", Label: "Item ID", Example: "", Aliases: []string{"itemid"}},
		storeField,
		{Key: "qty", Label: "Qty", Required: true, Example: "120",
			Aliases: []string{"qty", "quantity", "stock", "closingstock", "openingstock", "balqty", "stockqty", "clstock", "openingqty"}},
		{Key: "unit_cost", Label: "Purc Rate", Example: "8.50",
			Aliases: []string{"rate", "cost", "purcrate", "unitcost", "purchaserate", "costprice", "openingrate"}},
		{Key: "value", Label: "Value", Example: "1,020.00",
			Aliases: []string{"value", "amount", "stockvalue", "closingvalue", "openingvalue", "amt"}},
		{Key: "batch_no", Label: "Batch", Example: "B2601", Aliases: []string{"batch", "batchno", "lot", "lotno", "batchnumber", "batchname"}},
		{Key: "mfg_date", Label: "Mfg Date", Example: "01/2026",
			Aliases: []string{"mfgdate", "mfg", "manufacturingdate", "mfd", "mfdon", "manufacturedate"}},
		{Key: "expiry_date", Label: "Expiry", Example: "12/27",
			Aliases: []string{"expiry", "exp", "expirydate", "expdate", "expiryperiod"}},
		{Key: "mrp", Label: "MRP", Example: "10.00", Aliases: []string{"mrp", "batchmrp", "maxretailprice"}},
	},
	KindOpenBills: {
		{Key: "party_ref", Label: "Party Code", Example: "C0001",
			Aliases: []string{"partycode", "partyref", "code", "accountcode", "custcode"}},
		{Key: "party_name", Label: "Party Name", Example: "Sharma General Store",
			Aliases: []string{"partyname", "party", "name", "accountname", "ledgername", "customername", "suppliername", "particulars"}},
		{Key: "bill_no", Label: "Bill No", Required: true, Example: "GST/2526/1043",
			Aliases: []string{"billno", "bill", "invoiceno", "invno", "refno", "billnumber", "voucherno", "vchno", "docno", "reference", "ref"}},
		{Key: "bill_date", Label: "Bill Date", Example: "15/02/2026",
			Aliases: []string{"billdate", "date", "invoicedate", "invdate", "vchdate", "docdate"}},
		{Key: "due_date", Label: "Due Date", Example: "17/03/2026", Aliases: []string{"duedate", "due", "dueon", "paymentdue"}},
		{Key: "credit_days", Label: "Credit Days", Example: "30",
			Aliases: []string{"creditdays", "crdays", "creditperiod", "duedays"}},
		{Key: "bill_amount", Label: "Bill Amt", Example: "11,800.00",
			Aliases: []string{"billamt", "billamount", "invoiceamount", "invamt", "originalamount", "totalamount"}},
		{Key: "amount", Label: "Pending Amt", Required: true, Example: "6,800.00 Dr",
			Aliases: []string{"pendingamt", "pendingamount", "pending", "balance", "balanceamount", "outstanding", "outstandingamount",
				"dueamount", "balamt", "amount", "closingbalance", "billbalance", "amt"}},
		{Key: "dr_cr", Label: "Dr/Cr", Example: "Dr", Aliases: []string{"drcr", "balancetype", "baltype", "crdr", "side"}},
	},
	KindInvoices: {
		{Key: "doc_type", Label: "Voucher Type", Example: "Sales",
			Aliases: []string{"vouchertype", "vchtype", "type", "doctype", "billtype", "transactiontype", "trntype"}},
		{Key: "number", Label: "Bill No", Required: true, Example: "GST/2526/1043",
			Aliases: []string{"billno", "invoiceno", "invno", "voucherno", "vchno", "number", "docno", "bill", "invoicenumber", "billnumber"}},
		{Key: "date", Label: "Date", Required: true, Example: "15/02/2026",
			Aliases: []string{"date", "billdate", "invoicedate", "invdate", "vchdate", "voucherdate", "docdate"}},
		{Key: "party_ref", Label: "Party Code", Example: "C0001", Aliases: []string{"partycode", "partyref", "accountcode", "custcode"}},
		{Key: "party_name", Label: "Party Name", Example: "Sharma General Store",
			Aliases: []string{"partyname", "party", "name", "customername", "suppliername", "accountname", "particulars", "ledgername"}},
		{Key: "party_gstin", Label: "GSTIN", Example: "27AAPFU0939F1ZV",
			Aliases: []string{"gstin", "partygstin", "gstinno", "gstno", "gstinuin", "customergstin"}},
		{Key: "place_of_supply", Label: "Place of Supply", Example: "27-Maharashtra",
			Aliases: []string{"placeofsupply", "pos", "state", "supplystate", "statename"}},
		{Key: "taxable", Label: "Taxable Amt", Example: "10,000.00",
			Aliases: []string{"taxable", "taxableamt", "taxableamount", "taxablevalue", "assessablevalue", "goodsvalue", "basicamount", "grossamount"}},
		{Key: "cgst", Label: "CGST", Example: "900.00", Aliases: []string{"cgst", "cgstamt", "cgstamount", "centraltax"}},
		{Key: "sgst", Label: "SGST", Example: "900.00", Aliases: []string{"sgst", "sgstamt", "sgstamount", "utgst", "statetax", "sgstutgst"}},
		{Key: "igst", Label: "IGST", Example: "0.00", Aliases: []string{"igst", "igstamt", "igstamount", "integratedtax"}},
		{Key: "cess", Label: "Cess", Example: "0.00", Aliases: []string{"cess", "cessamt", "cessamount"}},
		{Key: "round_off", Label: "R/Off", Example: "0.00", Aliases: []string{"roff", "roundoff", "rounding", "roundedoff", "round"}},
		{Key: "total", Label: "Net Amt", Example: "11,800.00",
			Aliases: []string{"netamt", "netamount", "total", "billamount", "billamt", "invoiceamount", "invoicevalue", "grandtotal", "amount", "nettotal"}},
		{Key: "due_date", Label: "Due Date", Example: "17/03/2026", Aliases: []string{"duedate", "due", "dueon"}},
		{Key: "reference", Label: "Reference", Example: "PO-118", Aliases: []string{"reference", "refno", "supplierinvoiceno", "partybillno", "orderno"}},
		{Key: "narration", Label: "Narration", Example: "", Aliases: []string{"narration", "remarks", "remark", "note", "notes"}},
		storeField,
	},
}

// ValidKind reports whether kind is supported.
func ValidKind(kind string) bool { _, ok := Fields[kind]; return ok }

// Normalize lower-cases and strips everything but letters and digits.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// score rates how well a header matches a field (0..1).
func score(header string, f Field) float64 {
	h := Normalize(header)
	if h == "" {
		return 0
	}
	best := 0.0
	for _, a := range append([]string{Normalize(f.Key), Normalize(f.Label)}, f.Aliases...) {
		var s float64
		switch {
		case h == a:
			s = 1
		case len(a) >= 4 && strings.HasPrefix(h, a), len(h) >= 4 && strings.HasPrefix(a, h):
			s = 0.85
		case len(a) >= 4 && strings.Contains(h, a):
			s = 0.75
		default:
			s = similarity(h, a)
		}
		if s > best {
			best = s
		}
	}
	return best
}

// Suggest maps field keys to header text. Each column feeds at most one field;
// the strongest matches win. Matches below 0.7 are left unmapped.
func Suggest(kind string, headers []string) map[string]string {
	type cand struct {
		field string
		col   int
		s     float64
	}
	var cands []cand
	for _, f := range Fields[kind] {
		for i, h := range headers {
			if s := score(h, f); s >= 0.7 {
				cands = append(cands, cand{f.Key, i, s})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].s > cands[j].s })
	out := map[string]string{}
	usedCol := map[int]bool{}
	for _, c := range cands {
		if _, done := out[c.field]; done || usedCol[c.col] {
			continue
		}
		out[c.field] = headers[c.col]
		usedCol[c.col] = true
	}
	return out
}

// DetectHeaderRow finds the header in the first rows of a sheet: MargBooks
// exports put the company name and report title above it. It returns the
// 1-based row number with the most recognised headers (needs at least 2),
// falling back to the first non-empty row.
func DetectHeaderRow(kind string, rows [][]string) int {
	best, bestN, firstNonEmpty := 0, 1, 0
	for i, r := range rows {
		nonEmpty := 0
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				nonEmpty++
			}
		}
		if nonEmpty == 0 {
			continue
		}
		if firstNonEmpty == 0 {
			firstNonEmpty = i + 1
		}
		if n := len(Suggest(kind, r)); n > bestN {
			best, bestN = i+1, n
		}
	}
	if best == 0 {
		return max(firstNonEmpty, 1)
	}
	return best
}

// Resolve turns a field→header mapping into field→column index for the header row.
// Unknown headers are reported.
func Resolve(m map[string]string, headers []string) (map[string]int, []string) {
	idx := map[string]int{}
	byNorm := map[string]int{}
	for i, h := range headers {
		if n := Normalize(h); n != "" {
			if _, dup := byNorm[n]; !dup {
				byNorm[n] = i
			}
		}
	}
	var missing []string
	for field, h := range m {
		if strings.TrimSpace(h) == "" {
			continue
		}
		if i, ok := byNorm[Normalize(h)]; ok {
			idx[field] = i
		} else {
			missing = append(missing, h)
		}
	}
	sort.Strings(missing)
	return idx, missing
}

// MissingRequired lists required fields absent from the mapping.
func MissingRequired(kind string, m map[string]string) []string {
	var out []string
	for _, f := range Fields[kind] {
		if f.Required && strings.TrimSpace(m[f.Key]) == "" {
			out = append(out, f.Key)
		}
	}
	return out
}

// similarity is 1 - levenshtein/maxLen.
func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}
