package sheet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/shakinm/xlsReader/xls"
	"golang.org/x/net/html"
)

// memBook is a workbook held in memory as rows of strings: legacy .xls,
// HTML-table "Excel" exports and Tally XML all end up here.
type memBook struct {
	names  []string
	sheets map[string][][]string
}

func (m *memBook) Sheets() []string { return m.names }
func (m *memBook) Close() error     { return nil }

func (m *memBook) Each(sheet string, fn func(int, []string) error) error {
	rows, ok := m.sheets[sheet]
	if !ok {
		return fmt.Errorf("sheet %q not found", sheet)
	}
	for i, r := range rows {
		if err := fn(i+1, r); err != nil {
			return err
		}
	}
	return nil
}

func (m *memBook) add(name string, rows [][]string) {
	if m.sheets == nil {
		m.sheets = map[string][][]string{}
	}
	if _, dup := m.sheets[name]; !dup {
		m.names = append(m.names, name)
	}
	m.sheets[name] = rows
}

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// openXLS reads a ".xls" file. Three things travel under that extension in
// Indian accounting exports: real BIFF8 workbooks (Excel 97-2003), HTML tables
// (MargBooks and many billing programs "export to Excel" this way) and plain
// tab-separated text. The content decides, not the extension.
func openXLS(b []byte, name string) (Workbook, error) {
	switch {
	case bytes.HasPrefix(b, oleMagic):
		return openBIFF(b)
	case looksLikeHTML(b):
		return openHTMLTables(b, name)
	case utf8.Valid(decodeText(b)) && len(bytes.TrimSpace(b)) > 0:
		return newCSV(b, name), nil
	}
	return nil, ErrUnsupported
}

// openBIFF reads an Excel 97-2003 workbook. The reader library can panic on
// damaged files; that becomes an error, never a crash.
func openBIFF(b []byte) (wb Workbook, err error) {
	defer func() {
		if r := recover(); r != nil {
			wb, err = nil, fmt.Errorf("open xls: damaged or unsupported workbook (%v); save it as .xlsx", r)
		}
	}()
	f, err := xls.OpenReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("open xls: %w", err)
	}
	m := &memBook{}
	for i := 0; i < f.GetNumberSheets(); i++ {
		sh, err := f.GetSheet(i)
		if err != nil {
			return nil, fmt.Errorf("open xls: %w", err)
		}
		var rows [][]string
		for _, r := range sh.GetRows() {
			var cells []string
			for _, c := range r.GetCols() {
				cells = append(cells, xlsCell(c.GetType(), c.GetString(), c.GetFloat64()))
			}
			rows = append(rows, cells)
		}
		m.add(sh.GetName(), rows)
	}
	if len(m.names) == 0 {
		return nil, fmt.Errorf("open xls: no sheets found")
	}
	return m, nil
}

// xlsCell renders numbers without float noise (1200.5, not 1200.4999999).
func xlsCell(typ, s string, f float64) string {
	if strings.Contains(typ, "Number") || strings.Contains(typ, "Rk") || strings.Contains(typ, "RK") {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return s
}

func looksLikeHTML(b []byte) bool {
	head := strings.ToLower(string(decodeText(b[:min(len(b), 4096)])))
	return strings.Contains(head, "<table") || strings.Contains(head, "<html") || strings.Contains(head, "<tr")
}

// openHTMLTables turns every <tr> of an HTML export into a row; each <table>
// becomes a sheet ("<name>", "<name> (2)", …).
func openHTMLTables(b []byte, name string) (Workbook, error) {
	doc, err := html.Parse(bytes.NewReader(decodeText(b)))
	if err != nil {
		return nil, fmt.Errorf("open html table: %w", err)
	}
	m := &memBook{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			var rows [][]string
			collectRows(n, &rows)
			sheet := name
			if len(m.names) > 0 {
				sheet = fmt.Sprintf("%s (%d)", name, len(m.names)+1)
			}
			m.add(sheet, rows)
			return // nested tables belong to their cell
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(m.names) == 0 {
		return nil, fmt.Errorf("open html table: no <table> in the file")
	}
	return m, nil
}

func collectRows(n *html.Node, rows *[][]string) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "tr":
			var cells []string
			for td := c.FirstChild; td != nil; td = td.NextSibling {
				if td.Type == html.ElementNode && (td.Data == "td" || td.Data == "th") {
					text := strings.Join(strings.Fields(nodeText(td)), " ")
					span := 1
					for _, a := range td.Attr {
						if a.Key == "colspan" {
							if n, err := strconv.Atoi(a.Val); err == nil && n > 1 && n < 100 {
								span = n
							}
						}
					}
					cells = append(cells, text)
					for i := 1; i < span; i++ {
						cells = append(cells, "")
					}
				}
			}
			*rows = append(*rows, cells)
		case "table":
			// nested table: skip
		default:
			collectRows(c, rows) // thead / tbody / tfoot
		}
	}
}

func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "br" {
			b.WriteString(" ")
		}
		b.WriteString(nodeText(c))
	}
	return b.String()
}

// decodeText converts UTF-16 (with BOM, as Tally and some Excel "Unicode text"
// exports write) to UTF-8; other input is returned unchanged.
func decodeText(b []byte) []byte {
	var order binary.ByteOrder
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		order = binary.LittleEndian
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		order = binary.BigEndian
	default:
		return b
	}
	b = b[2:]
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return []byte(string(utf16.Decode(u)))
}
