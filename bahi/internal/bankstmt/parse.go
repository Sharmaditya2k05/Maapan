// Package bankstmt reads bank statements exported by Indian banks (CSV or
// XLSX) into normalised lines: date, description, reference, withdrawal,
// deposit, balance. Column mapping presets cover SBI, HDFC, ICICI, Axis and
// Kotak; "generic" recognises the usual header names and a custom Mapping
// overrides any column. Amounts are parsed to paise without float.
package bankstmt

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/mool/money"
)

// Line is one statement row.
type Line struct {
	Row         int    `json:"row"` // 1-based row in the file
	Date        string `json:"date"`
	ValueDate   string `json:"value_date,omitempty"`
	Description string `json:"description"`
	Reference   string `json:"reference,omitempty"`
	Withdrawal  int64  `json:"withdrawal_paise"`
	Deposit     int64  `json:"deposit_paise"`
	Balance     *int64 `json:"balance_paise,omitempty"`
}

// Mapping names the header of each column (case, spaces and punctuation are
// ignored). Empty fields fall back to the preset.
type Mapping struct {
	Date        string `json:"date,omitempty"`
	ValueDate   string `json:"value_date,omitempty"`
	Description string `json:"description,omitempty"`
	Reference   string `json:"reference,omitempty"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Amount      string `json:"amount,omitempty"` // single signed/flagged amount column
	DrCr        string `json:"drcr,omitempty"`   // "Dr"/"Cr" indicator column used with Amount
	Balance     string `json:"balance,omitempty"`
	DateFormat  string `json:"date_format,omitempty"` // e.g. dd/mm/yyyy, dd-mon-yy; empty = auto
}

// Preset is a bank's export layout.
type Preset struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Date        []string `json:"date"`
	ValueDate   []string `json:"value_date"`
	Description []string `json:"description"`
	Reference   []string `json:"reference"`
	Debit       []string `json:"debit"`
	Credit      []string `json:"credit"`
	Amount      []string `json:"amount,omitempty"`
	DrCr        []string `json:"drcr,omitempty"`
	Balance     []string `json:"balance"`
}

var generic = Preset{Code: "generic", Name: "Generic (auto-detect headers)",
	Date:        []string{"txndate", "transactiondate", "trandate", "date", "postingdate", "valuedate"},
	ValueDate:   []string{"valuedate", "valuedt"},
	Description: []string{"description", "narration", "particulars", "transactionremarks", "remarks", "transactiondetails", "details"},
	Reference:   []string{"refnochequeno", "chqrefno", "chqrefnumber", "chequenumber", "chequeno", "chqno", "refno", "reference", "utr", "instrumentno"},
	Debit:       []string{"withdrawalamountinr", "withdrawalamt", "withdrawalamount", "withdrawaldr", "withdrawals", "withdrawal", "debitamount", "debit", "dr"},
	Credit:      []string{"depositamountinr", "depositamt", "depositamount", "depositcr", "deposits", "deposit", "creditamount", "credit", "cr"},
	Amount:      []string{"amount", "transactionamount", "amountinr"},
	DrCr:        []string{"drcr", "crdr", "drcrindicator", "type"},
	Balance:     []string{"closingbalance", "balanceinr", "balance", "runningbalance", "bal"},
}

// Presets by code. Each lists the bank's own headers first; the generic
// aliases are tried after them.
var Presets = map[string]Preset{
	"generic": generic,
	"sbi": {Code: "sbi", Name: "State Bank of India", Date: []string{"txndate"}, ValueDate: []string{"valuedate"},
		Description: []string{"description"}, Reference: []string{"refnochequeno"}, Debit: []string{"debit"},
		Credit: []string{"credit"}, Balance: []string{"balance"}},
	"hdfc": {Code: "hdfc", Name: "HDFC Bank", Date: []string{"date"}, ValueDate: []string{"valuedt"},
		Description: []string{"narration"}, Reference: []string{"chqrefno"}, Debit: []string{"withdrawalamt"},
		Credit: []string{"depositamt"}, Balance: []string{"closingbalance"}},
	"icici": {Code: "icici", Name: "ICICI Bank", Date: []string{"transactiondate", "valuedate"}, ValueDate: []string{"valuedate"},
		Description: []string{"transactionremarks", "particulars"}, Reference: []string{"chequenumber", "chequeno"},
		Debit: []string{"withdrawalamountinr", "withdrawalamount", "withdrawals"}, Credit: []string{"depositamountinr", "depositamount", "deposits"},
		Balance: []string{"balanceinr", "balance"}},
	"axis": {Code: "axis", Name: "Axis Bank", Date: []string{"trandate"}, ValueDate: []string{"valuedate"},
		Description: []string{"particulars"}, Reference: []string{"chqno"}, Debit: []string{"dr"}, Credit: []string{"cr"},
		Balance: []string{"bal"}},
	"kotak": {Code: "kotak", Name: "Kotak Mahindra Bank", Date: []string{"transactiondate", "date"}, ValueDate: []string{"valuedate"},
		Description: []string{"description", "narration"}, Reference: []string{"chqrefno", "chqrefnumber"},
		Debit: []string{"withdrawaldr", "debit"}, Credit: []string{"depositcr", "credit"}, Amount: []string{"amount"},
		DrCr: []string{"drcr"}, Balance: []string{"balance"}},
}

// PresetList returns the presets sorted by code, generic first.
func PresetList() []Preset {
	out := make([]Preset, 0, len(Presets))
	for _, p := range Presets {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Code == "generic") != (out[j].Code == "generic") {
			return out[i].Code == "generic"
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func norm(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Parse reads a CSV or XLSX statement (by file name extension or content).
func Parse(data []byte, fileName, preset string, m Mapping) ([]Line, error) {
	p, ok := Presets[strings.ToLower(orDefault(preset, "generic"))]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", preset)
	}
	var rows [][]string
	var err error
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".xlsx") || bytes.HasPrefix(data, []byte("PK\x03\x04")):
		rows, err = readXLSX(data)
	case strings.HasSuffix(lower, ".xls"):
		return nil, errors.New("old .xls files are not supported; save the statement as .xlsx or .csv")
	default:
		rows, err = readCSV(data)
	}
	if err != nil {
		return nil, err
	}
	return parseRows(rows, p, m)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func readCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	delim := ','
	head := string(data[:min(len(data), 4096)])
	if strings.Count(head, "\t") > strings.Count(head, ",") {
		delim = '\t'
	} else if strings.Count(head, ";") > strings.Count(head, ",") {
		delim = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = delim != '\t' // a tab is whitespace: trimming would merge empty cells
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	return rows, nil
}

type cols struct{ date, vdate, desc, ref, debit, credit, amount, drcr, bal int }

func find(header []string, want string, aliases ...[]string) int {
	if want != "" {
		w := norm(want)
		for i, h := range header {
			if norm(h) == w {
				return i
			}
		}
		return -2 // explicitly mapped but absent
	}
	for _, as := range aliases {
		for _, a := range as {
			for i, h := range header {
				if norm(h) == a {
					return i
				}
			}
		}
	}
	return -1
}

func locate(header []string, p Preset, m Mapping) (cols, bool) {
	g := generic
	c := cols{
		date:   find(header, m.Date, p.Date, g.Date),
		vdate:  find(header, m.ValueDate, p.ValueDate, g.ValueDate),
		desc:   find(header, m.Description, p.Description, g.Description),
		ref:    find(header, m.Reference, p.Reference, g.Reference),
		debit:  find(header, m.Debit, p.Debit, g.Debit),
		credit: find(header, m.Credit, p.Credit, g.Credit),
		amount: find(header, m.Amount, p.Amount, g.Amount),
		drcr:   find(header, m.DrCr, p.DrCr, g.DrCr),
		bal:    find(header, m.Balance, p.Balance, g.Balance),
	}
	if c.vdate == c.date {
		c.vdate = -1
	}
	ok := c.date >= 0 && (c.debit >= 0 && c.credit >= 0 || c.amount >= 0)
	return c, ok
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func parseRows(rows [][]string, p Preset, m Mapping) ([]Line, error) {
	hdr := -1
	var c cols
	for i := 0; i < len(rows) && i < 60; i++ {
		if cc, ok := locate(rows[i], p, m); ok {
			hdr, c = i, cc
			break
		}
	}
	if hdr < 0 {
		return nil, errors.New("could not find the header row (date and debit/credit or amount columns); pass a column mapping")
	}
	var out []Line
	for i := hdr + 1; i < len(rows); i++ {
		row := rows[i]
		d, err := ParseDate(cell(row, c.date), m.DateFormat)
		if err != nil {
			continue // opening/closing/footer rows
		}
		l := Line{Row: i + 1, Date: d, Description: cell(row, c.desc), Reference: cell(row, c.ref)}
		if v := cell(row, c.vdate); v != "" {
			if vd, err := ParseDate(v, m.DateFormat); err == nil {
				l.ValueDate = vd
			}
		}
		if c.debit >= 0 && c.credit >= 0 {
			if l.Withdrawal, err = amount(cell(row, c.debit)); err != nil {
				return nil, fmt.Errorf("row %d: withdrawal: %w", i+1, err)
			}
			if l.Deposit, err = amount(cell(row, c.credit)); err != nil {
				return nil, fmt.Errorf("row %d: deposit: %w", i+1, err)
			}
		}
		if l.Withdrawal == 0 && l.Deposit == 0 && c.amount >= 0 {
			raw := cell(row, c.amount)
			flag := strings.ToUpper(cell(row, c.drcr))
			up := strings.ToUpper(raw)
			if flag == "" {
				switch {
				case strings.Contains(up, "DR"):
					flag = "DR"
				case strings.Contains(up, "CR"):
					flag = "CR"
				}
			}
			a, err := amount(raw)
			if err != nil {
				return nil, fmt.Errorf("row %d: amount: %w", i+1, err)
			}
			neg := strings.HasPrefix(strings.TrimSpace(raw), "-") || strings.HasPrefix(strings.TrimSpace(raw), "(")
			switch {
			case strings.HasPrefix(flag, "D") || flag == "" && neg:
				l.Withdrawal = a
			default:
				l.Deposit = a
			}
		}
		if l.Withdrawal == 0 && l.Deposit == 0 {
			continue
		}
		if b := cell(row, c.bal); b != "" {
			if v, err := amount(b); err == nil {
				if strings.Contains(strings.ToUpper(b), "DR") {
					v = -v
				}
				l.Balance = &v
			}
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, errors.New("no transactions found below the header row")
	}
	return out, nil
}

// amount parses "1,234.50", "1,234.50 Cr", "(500.00)", "-" or "" (zero) to
// positive paise.
func amount(s string) (int64, error) {
	t := strings.TrimSpace(s)
	for _, suf := range []string{"(Dr)", "(Cr)", "Dr.", "Cr.", "DR", "CR", "Dr", "Cr"} {
		t = strings.TrimSpace(strings.TrimSuffix(t, suf))
	}
	if t == "" || t == "-" || t == "--" {
		return 0, nil
	}
	p, err := money.ParseRupees(t)
	if err != nil {
		return 0, err
	}
	if p < 0 {
		p = -p
	}
	return p, nil
}

var dateLayouts = []string{
	"02/01/2006", "2/1/2006", "02/01/06", "2/1/06", "02-01-2006", "2-1-2006", "02-01-06", "02.01.2006",
	"02 Jan 2006", "2 Jan 2006", "02-Jan-2006", "2-Jan-2006", "02-Jan-06", "02 Jan 06", "02/Jan/2006", "Jan 02, 2006",
	"2006-01-02", "2006/01/02", "02/01/2006 15:04:05", "02/01/2006 15:04", "02-01-2006 15:04:05", "2006-01-02 15:04:05",
	"02 January 2006",
}

var formatLayouts = map[string]string{
	"dd/mm/yyyy": "02/01/2006", "dd/mm/yy": "02/01/06", "dd-mm-yyyy": "02-01-2006", "dd-mm-yy": "02-01-06",
	"dd.mm.yyyy": "02.01.2006", "dd mon yyyy": "02 Jan 2006", "dd-mon-yyyy": "02-Jan-2006", "dd-mon-yy": "02-Jan-06",
	"yyyy-mm-dd": "2006-01-02", "mm/dd/yyyy": "01/02/2006",
}

// ParseDate reads a statement date (Indian day-first formats, ISO, or an
// Excel serial number) and returns YYYY-MM-DD.
func ParseDate(s, format string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty date")
	}
	if format != "" {
		layout, ok := formatLayouts[strings.ToLower(format)]
		if !ok {
			layout = format
		}
		t, err := time.Parse(layout, s)
		if err != nil {
			return "", err
		}
		return t.Format(time.DateOnly), nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 20000 && n < 80000 && !strings.ContainsAny(s, "/-") {
		return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(n)).Format(time.DateOnly), nil
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format(time.DateOnly), nil
		}
	}
	return "", fmt.Errorf("unrecognised date %q", s)
}
