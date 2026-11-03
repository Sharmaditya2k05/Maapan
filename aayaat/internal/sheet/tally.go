package sheet

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Tally sheet names. A Tally Prime / ERP 9 "Export → XML" of masters and/or
// vouchers is flattened into these tables so the normal preview → mapping →
// validate flow applies unchanged; their headers are chosen to auto-map.
const (
	TallyGroups       = "Groups"
	TallyPartyLedgers = "Party Ledgers"
	TallyOtherLedgers = "Other Ledgers"
	TallyStockItems   = "Stock Items"
	TallyStockBatches = "Stock Batches"
	TallyBills        = "Bills"
	TallyVouchers     = "Vouchers"
)

// Columns of each Tally sheet.
var tallyColumns = map[string][]string{
	TallyGroups: {"Group Name", "Under", "Primary Group", "Nature"},
	TallyPartyLedgers: {"Party Name", "Group", "Mobile", "Email", "GSTIN No", "PAN", "State", "Address", "City",
		"Pincode", "Shipping Address", "Opening Bal", "Credit Limit", "Credit Days"},
	TallyOtherLedgers: {"Ledger Name", "Group", "Primary Group", "Opening Bal"},
	TallyStockItems: {"Item Name", "Item Code", "Category", "Brand", "Unit", "Alt Unit", "Conversion", "HSN Code",
		"GST %", "Cess %", "MRP", "Sale Rate", "Purc Rate", "Batch Wise", "Reorder Level", "Opening Qty", "Opening Rate"},
	TallyStockBatches: {"Item Code", "Item Name", "Godown", "Batch", "Qty", "Rate", "Value", "Mfg Date", "Expiry", "MRP"},
	TallyBills:        {"Party Name", "Bill No", "Bill Date", "Due Date", "Credit Days", "Amount"},
	TallyVouchers: {"Voucher Type", "Voucher No", "Date", "Party Name", "Party GSTIN", "Place of Supply",
		"Taxable Value", "CGST", "SGST", "IGST", "Cess", "Round Off", "Total", "Reference", "Narration"},
}

var tallyOrder = []string{TallyPartyLedgers, TallyOtherLedgers, TallyBills, TallyStockItems, TallyStockBatches, TallyVouchers, TallyGroups}

// tallyBook is a Tally XML export flattened to tables.
type tallyBook struct{ memBook }

func (t *tallyBook) Source() string { return "tally" }

// DefaultSheet picks the table that feeds an import kind.
func (t *tallyBook) DefaultSheet(kind string) string {
	switch kind {
	case "parties":
		return TallyPartyLedgers
	case "opening_balances":
		return TallyOtherLedgers
	case "items":
		return TallyStockItems
	case "opening_stock":
		return TallyStockBatches
	case "open_bills":
		return TallyBills
	case "invoices":
		return TallyVouchers
	}
	return ""
}

// node is a generic XML element.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []*node    `xml:",any"`
}

func (n *node) attr(name string) string {
	for _, a := range n.Attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// child returns the first direct child with one of the tags.
func (n *node) child(tags ...string) *node {
	for _, c := range n.Children {
		for _, t := range tags {
			if c.XMLName.Local == t {
				return c
			}
		}
	}
	return nil
}

// text returns the trimmed text of the first direct child with one of tags.
func (n *node) text(tags ...string) string {
	if c := n.child(tags...); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

// all returns direct children with the tag.
func (n *node) all(tag string) []*node {
	var out []*node
	for _, c := range n.Children {
		if c.XMLName.Local == tag {
			out = append(out, c)
		}
	}
	return out
}

// find returns the first descendant (depth-first) with one of the tags.
func (n *node) find(tags ...string) *node {
	for _, c := range n.Children {
		for _, t := range tags {
			if c.XMLName.Local == t {
				return c
			}
		}
		if f := c.find(tags...); f != nil {
			return f
		}
	}
	return nil
}

func (n *node) findText(tags ...string) string {
	if f := n.find(tags...); f != nil {
		return strings.TrimSpace(f.Text)
	}
	return ""
}

// walk visits every descendant.
func (n *node) walk(fn func(*node)) {
	for _, c := range n.Children {
		fn(c)
		c.walk(fn)
	}
}

// name is a master's NAME attribute, or its first NAME.LIST/NAME, or <NAME>.
func (n *node) name() string {
	if v := n.attr("NAME"); v != "" {
		return v
	}
	if l := n.find("NAME.LIST", "LANGUAGENAME.LIST"); l != nil {
		if v := l.findText("NAME"); v != "" {
			return v
		}
	}
	return n.text("NAME")
}

// Tally writes control characters as character references (the parent of a
// primary group is "&#4; Primary"); XML 1.0 forbids them, so drop them.
var badCharRef = regexp.MustCompile(`&#(x[0-9A-Fa-f]+|[0-9]+);`)

func cleanXML(b []byte) []byte {
	return badCharRef.ReplaceAllFunc(b, func(m []byte) []byte {
		s := string(m[2 : len(m)-1])
		var v int64
		var err error
		if s[0] == 'x' || s[0] == 'X' {
			v, err = strconv.ParseInt(s[1:], 16, 32)
		} else {
			v, err = strconv.ParseInt(s, 10, 32)
		}
		if err == nil && v < 32 && v != 9 && v != 10 && v != 13 {
			return nil
		}
		return m
	})
}

// masters collected from the export.
type tallyData struct {
	groups   map[string]string // group → parent
	groupOrd []string
	ledgers  []*node
	ledgerGr map[string]string // ledger name → group
	items    []*node
	vouchers []*node
}

// OpenTally parses a Tally XML export (UTF-8 or UTF-16) into tables.
func OpenTally(b []byte) (Workbook, error) {
	b = cleanXML(decodeText(b))
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // already UTF-8
	td := &tallyData{groups: map[string]string{}, ledgerGr: map[string]string{}}
	sawEnvelope := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tally xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "ENVELOPE":
			sawEnvelope = true
		case "GROUP", "LEDGER", "STOCKITEM", "VOUCHER":
			var n node
			if err := d.DecodeElement(&n, &se); err != nil {
				return nil, fmt.Errorf("tally xml %s: %w", se.Name.Local, err)
			}
			switch se.Name.Local {
			case "GROUP":
				name := n.name()
				if _, seen := td.groups[name]; !seen {
					td.groupOrd = append(td.groupOrd, name)
				}
				td.groups[name] = cleanParent(n.text("PARENT"))
			case "LEDGER":
				td.ledgers = append(td.ledgers, &n)
				td.ledgerGr[n.name()] = cleanParent(n.text("PARENT"))
			case "STOCKITEM":
				td.items = append(td.items, &n)
			case "VOUCHER":
				td.vouchers = append(td.vouchers, &n)
			}
		}
	}
	if !sawEnvelope && len(td.ledgers)+len(td.items)+len(td.vouchers) == 0 {
		return nil, fmt.Errorf("tally xml: no ENVELOPE / LEDGER / STOCKITEM / VOUCHER elements; export from Tally with Format = XML")
	}
	return td.book(), nil
}

func cleanParent(p string) string {
	p = strings.TrimSpace(p)
	if strings.EqualFold(p, "Primary") {
		return ""
	}
	return p
}

// Tally's reserved primary groups.
var tallyPrimary = map[string]bool{
	"Capital Account": true, "Loans (Liability)": true, "Current Liabilities": true, "Fixed Assets": true,
	"Investments": true, "Current Assets": true, "Branch / Divisions": true, "Misc. Expenses (ASSET)": true,
	"Suspense A/c": true, "Sales Accounts": true, "Purchase Accounts": true, "Direct Incomes": true,
	"Direct Expenses": true, "Indirect Incomes": true, "Indirect Expenses": true,
}

// chain returns the group and its ancestors, nearest first.
func (td *tallyData) chain(group string) []string {
	var out []string
	seen := map[string]bool{}
	for g := group; g != "" && !seen[g]; g = td.groups[g] {
		seen[g] = true
		out = append(out, g)
	}
	return out
}

func (td *tallyData) under(group string, targets ...string) bool {
	for _, g := range td.chain(group) {
		for _, t := range targets {
			if strings.EqualFold(g, t) {
				return true
			}
		}
	}
	return false
}

// primary is the top-most known group of a group.
func (td *tallyData) primary(group string) string {
	c := td.chain(group)
	for i := len(c) - 1; i >= 0; i-- {
		if tallyPrimary[c[i]] || i == len(c)-1 {
			return c[i]
		}
	}
	return group
}

func (td *tallyData) isParty(ledgerGroup string) bool {
	return td.under(ledgerGroup, "Sundry Debtors", "Sundry Creditors")
}

func (td *tallyData) book() *tallyBook {
	tb := &tallyBook{}
	sheets := map[string][][]string{}
	for k, cols := range tallyColumns {
		sheets[k] = [][]string{cols}
	}
	for _, g := range td.groupOrd {
		sheets[TallyGroups] = append(sheets[TallyGroups], []string{g, td.groups[g], td.primary(g), groupNature(td.primary(g))})
	}
	for _, l := range td.ledgers {
		name, group := l.name(), cleanParent(l.text("PARENT"))
		if td.isParty(group) {
			sheets[TallyPartyLedgers] = append(sheets[TallyPartyLedgers], td.partyRow(l, name, group))
			for _, b := range l.all("BILLALLOCATIONS.LIST") {
				if row := billRow(name, b); row != nil {
					sheets[TallyBills] = append(sheets[TallyBills], row)
				}
			}
			continue
		}
		ob := l.text("OPENINGBALANCE")
		sheets[TallyOtherLedgers] = append(sheets[TallyOtherLedgers], []string{name, group, td.primary(group), drCrText(ob)})
	}
	for _, it := range td.items {
		row, batches := itemRows(it)
		sheets[TallyStockItems] = append(sheets[TallyStockItems], row)
		sheets[TallyStockBatches] = append(sheets[TallyStockBatches], batches...)
	}
	for _, v := range td.vouchers {
		if row := td.voucherRow(v); row != nil {
			sheets[TallyVouchers] = append(sheets[TallyVouchers], row)
		}
	}
	for _, s := range tallyOrder {
		tb.add(s, sheets[s])
	}
	return tb
}

func groupNature(primary string) string {
	switch primary {
	case "Capital Account", "Loans (Liability)", "Current Liabilities":
		return "Liabilities"
	case "Fixed Assets", "Investments", "Current Assets", "Misc. Expenses (ASSET)":
		return "Assets"
	case "Sales Accounts", "Direct Incomes", "Indirect Incomes":
		return "Income"
	case "Purchase Accounts", "Direct Expenses", "Indirect Expenses":
		return "Expenses"
	}
	return ""
}

func (td *tallyData) partyRow(l *node, name, group string) []string {
	var addr []string
	mailing := l.find("LEDMAILINGDETAILS.LIST") // Tally Prime 3+
	src := l
	if mailing != nil && mailing.find("ADDRESS") != nil {
		src = mailing
	}
	if al := src.find("ADDRESS.LIST"); al != nil {
		for _, a := range al.all("ADDRESS") {
			if t := strings.TrimSpace(a.Text); t != "" {
				addr = append(addr, t)
			}
		}
	}
	state := l.text("LEDSTATENAME", "STATENAME")
	pin := l.text("PINCODE")
	if mailing != nil {
		state = firstNonEmpty(mailing.text("STATE"), state)
		pin = firstNonEmpty(mailing.text("PINCODE"), pin)
	}
	gstin := firstNonEmpty(l.text("PARTYGSTIN"), l.findText("GSTIN"))
	ship := ""
	if sh := l.find("SHIPPINGADDRESS.LIST", "LEDMULTIADDRESSLIST.LIST"); sh != nil {
		var parts []string
		sh.walk(func(n *node) {
			if n.XMLName.Local == "ADDRESS" && strings.TrimSpace(n.Text) != "" {
				parts = append(parts, strings.TrimSpace(n.Text))
			}
		})
		ship = strings.Join(parts, ", ")
	}
	limit := strings.TrimPrefix(strings.TrimSpace(l.text("CREDITLIMIT")), "-")
	return []string{name, group, firstNonEmpty(l.text("LEDGERMOBILE"), l.text("LEDGERPHONE")), l.text("EMAIL"),
		gstin, l.text("INCOMETAXNUMBER"), state, strings.Join(addr, ", "), "", pin, ship,
		drCrText(l.text("OPENINGBALANCE")), limit, creditDays(l.text("BILLCREDITPERIOD"))}
}

// billRow is one opening bill allocation of a party ledger (bill-wise on).
func billRow(party string, b *node) []string {
	no := b.text("NAME")
	amt := b.text("OPENINGBALANCE", "AMOUNT")
	if no == "" || amt == "" {
		return nil
	}
	days := creditDays(b.text("BILLCREDITPERIOD"))
	return []string{party, no, b.text("BILLDATE"), "", days, drCrText(amt)}
}

func itemRows(it *node) ([]string, [][]string) {
	name := it.name()
	alias := ""
	if l := it.find("NAME.LIST"); l != nil {
		if names := l.all("NAME"); len(names) > 1 {
			alias = strings.TrimSpace(names[1].Text)
		}
	}
	base := it.text("BASEUNITS")
	altUnit, conv := it.text("ADDITIONALUNITS"), ""
	if altUnit != "" && !strings.EqualFold(altUnit, "Not Applicable") {
		// Tally stores "<DENOMINATOR> base = <CONVERSION> alt"; factor = base units per alt unit.
		den, _ := strconv.ParseFloat(strings.TrimSpace(firstWord(it.text("DENOMINATOR"))), 64)
		cv, _ := strconv.ParseFloat(strings.TrimSpace(firstWord(it.text("CONVERSION"))), 64)
		if den > 0 && cv > 0 {
			conv = strconv.FormatFloat(den/cv, 'f', -1, 64)
		}
	} else {
		altUnit = ""
	}
	hsn, igst, cess := "", "", ""
	if g := it.find("GSTDETAILS.LIST"); g != nil {
		hsn = g.findText("HSNCODE")
		g.walk(func(n *node) {
			if n.XMLName.Local != "RATEDETAILS.LIST" {
				return
			}
			head := strings.ToLower(n.text("GSTRATEDUTYHEAD"))
			rate := n.text("GSTRATE")
			switch {
			case (head == "igst" || strings.Contains(head, "integrated")) && igst == "":
				igst = rate
			case strings.Contains(head, "cess") && cess == "" && rate != "" && rate != "0":
				cess = rate
			}
		})
	}
	if hsn == "" {
		hsn = it.findText("HSNCODE")
	}
	mrp := ""
	if m := it.find("MRPRATEDETAILS.LIST"); m != nil {
		mrp = rateText(m.text("MRPRATE"))
	}
	sale, purc := "", ""
	if p := it.find("STANDARDPRICELIST.LIST"); p != nil {
		sale = rateText(p.text("RATE"))
	}
	if p := it.find("STANDARDCOSTLIST.LIST"); p != nil {
		purc = rateText(p.text("RATE"))
	}
	batchWise := it.text("ISBATCHWISEON")
	reorder := firstWord(it.text("REORDERBASE", "REORDERLEVEL"))
	openQty := qtyText(it.text("OPENINGBALANCE"))
	openRate := rateText(it.text("OPENINGRATE"))
	row := []string{name, alias, cleanParent(it.text("PARENT")), it.text("CATEGORY"), base, altUnit, conv, hsn, igst, cess,
		mrp, sale, purc, batchWise, reorder, openQty, openRate}

	var batches [][]string
	for _, b := range it.all("BATCHALLOCATIONS.LIST") {
		qty := qtyText(b.text("OPENINGBALANCE", "ACTUALQTY", "BILLEDQTY"))
		if qty == "" || qty == "0" {
			continue
		}
		batch := b.text("BATCHNAME")
		if strings.EqualFold(batch, "Primary Batch") {
			batch = ""
		}
		batches = append(batches, []string{alias, name, firstNonEmpty(b.text("GODOWNNAME"), "Main Location"), batch, qty,
			rateText(b.text("OPENINGRATE", "RATE")), absText(b.text("OPENINGVALUE", "AMOUNT")),
			b.text("MFDON", "MFGDATE"), b.text("EXPIRYPERIOD", "EXPIRYDATE"), rateText(b.text("MRPRATE"))})
	}
	if len(batches) == 0 && openQty != "" && openQty != "0" {
		batches = append(batches, []string{alias, name, "Main Location", "", openQty, openRate, absText(it.text("OPENINGVALUE")), "", "", ""})
	}
	return row, batches
}

// voucherRow summarises a sales / purchase / credit note / debit note voucher.
// Amounts in Tally XML are signed: negative = debit.
func (td *tallyData) voucherRow(v *node) []string {
	if strings.EqualFold(v.text("ISCANCELLED"), "Yes") || strings.EqualFold(v.text("ISOPTIONAL"), "Yes") ||
		strings.EqualFold(v.text("ISDELETED"), "Yes") {
		return nil
	}
	vtype := firstNonEmpty(v.text("VOUCHERTYPENAME"), v.attr("VCHTYPE"))
	docType := voucherDocType(vtype)
	if docType == "" {
		return nil // receipts, payments, journals, contra, stock journals are not invoice history
	}
	party := firstNonEmpty(v.text("PARTYLEDGERNAME"), v.text("PARTYNAME"))
	var entries []*node
	for _, tag := range []string{"ALLLEDGERENTRIES.LIST", "LEDGERENTRIES.LIST"} {
		entries = append(entries, v.all(tag)...)
	}
	var total, cgst, sgst, igst, cess, round int64
	partyFound := false
	for _, e := range entries {
		ledger := e.text("LEDGERNAME")
		amt := paiseOrZero(e.text("AMOUNT"))
		low := strings.ToLower(ledger)
		switch {
		case strings.EqualFold(ledger, party) && !partyFound:
			total, partyFound = abs64(amt), true
		case strings.Contains(low, "cgst") || strings.Contains(low, "central tax"):
			cgst += amt
		case strings.Contains(low, "sgst") || strings.Contains(low, "utgst") || strings.Contains(low, "state tax"):
			sgst += amt
		case strings.Contains(low, "igst") || strings.Contains(low, "integrated tax"):
			igst += amt
		case strings.Contains(low, "cess"):
			cess += amt
		case strings.Contains(low, "round"):
			round += amt
		}
	}
	if !partyFound {
		return nil
	}
	// On a sale or debit note the party is debited (negative) and taxes and a
	// positive round-off are credited (positive); on a purchase or credit note
	// it is the other way round. Normalise so "adds to the invoice total" is
	// positive.
	sign := int64(1)
	if docType == "purchase" || docType == "sale_return" {
		sign = -1
	}
	cgst, sgst, igst, cess, round = cgst*sign, sgst*sign, igst*sign, cess*sign, round*sign
	taxable := total - cgst - sgst - igst - cess - round
	partyName := party
	if !td.isParty(td.ledgerGr[party]) && len(td.ledgerGr) > 0 {
		if _, known := td.ledgerGr[party]; known {
			partyName = "" // cash / bank ledger: a counter sale or cash purchase
		}
	}
	return []string{docType, v.text("VOUCHERNUMBER"), v.text("DATE"), partyName,
		firstNonEmpty(v.text("PARTYGSTIN"), v.findText("PARTYGSTIN")), v.text("PLACEOFSUPPLY", "STATENAME"),
		paiseText(taxable), paiseText(cgst), paiseText(sgst), paiseText(igst), paiseText(cess), paiseText(round),
		paiseText(total), v.text("REFERENCE"), v.text("NARRATION")}
}

func voucherDocType(t string) string {
	l := strings.ToLower(t)
	switch {
	case strings.Contains(l, "credit note"), strings.Contains(l, "sales return"):
		return "sale_return"
	case strings.Contains(l, "debit note"), strings.Contains(l, "purchase return"):
		return "purchase_return"
	case strings.Contains(l, "sales"), strings.Contains(l, "sale"), strings.Contains(l, "invoice"):
		return "sale"
	case strings.Contains(l, "purchase"):
		return "purchase"
	}
	return ""
}

// drCrText turns a Tally signed amount (negative = Dr) into "1200.00 Dr".
func drCrText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	p, err := ParsePaise(s)
	if err != nil {
		return s
	}
	switch {
	case p < 0:
		return paiseText(-p) + " Dr"
	case p > 0:
		return paiseText(p) + " Cr"
	}
	return "0"
}

func paiseOrZero(s string) int64 {
	p, err := ParsePaise(rateText(s))
	if err != nil {
		return 0
	}
	return p
}

func paiseText(p int64) string {
	neg := p < 0
	if neg {
		p = -p
	}
	s := fmt.Sprintf("%d.%02d", p/100, p%100)
	if neg {
		s = "-" + s
	}
	return s
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func absText(s string) string {
	s = rateText(s)
	if s == "" {
		return ""
	}
	return strings.TrimPrefix(s, "-")
}

// rateText strips Tally's "/unit" suffix and currency text: "8.50/Pcs" → "8.50".
func rateText(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// qtyText keeps the number of " 100 Pcs" / "100.000 Nos".
func qtyText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	f := firstWord(s)
	if v, err := strconv.ParseFloat(strings.ReplaceAll(f, ",", ""), 64); err == nil {
		return strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	}
	return f
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// creditDays reads "30 Days" / "30".
func creditDays(s string) string {
	w := firstWord(s)
	if _, err := strconv.Atoi(w); err == nil {
		return w
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
