// Package sheet reads Excel (.xlsx) and CSV files row by row and parses the
// number/date formats found in MargBooks and other Indian accounting exports.
package sheet

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ErrUnsupported is returned for formats we cannot read.
var ErrUnsupported = errors.New("unsupported file type: upload .xlsx, .xls, .csv or a Tally .xml export")

// Workbook is an opened spreadsheet.
type Workbook interface {
	Sheets() []string
	// Each streams the rows of a sheet; returning an error from fn stops early.
	Each(sheet string, fn func(rowNo int, cells []string) error) error
	Close() error
}

// KindSheets is implemented by workbooks whose sheets are fixed tables (Tally
// XML), so the sheet that feeds an import kind can be picked automatically.
type KindSheets interface {
	DefaultSheet(kind string) string
}

// Source names the program a workbook came from ("tally" for Tally XML), ""
// when unknown.
type Source interface {
	Source() string
}

// Extensions accepted by Open.
var Extensions = []string{".xlsx", ".xlsm", ".xls", ".csv", ".txt", ".tsv", ".xml"}

// Open reads a workbook from r; the file name decides the format.
func Open(r io.Reader, fileName string) (Workbook, error) {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".xlsx", ".xlsm":
		f, err := excelize.OpenReader(r)
		if err != nil {
			return nil, fmt.Errorf("open xlsx: %w", err)
		}
		return &xlsx{f: f}, nil
	case ".xls":
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		return openXLS(b, baseName(fileName))
	case ".xml":
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		return OpenTally(b)
	case ".csv", ".txt", ".tsv":
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		return newCSV(b, baseName(fileName)), nil
	}
	return nil, ErrUnsupported
}

func baseName(fileName string) string {
	return strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func newCSV(b []byte, name string) *csvBook {
	return &csvBook{data: bytes.TrimPrefix(decodeText(b), utf8BOM), name: name}
}

type xlsx struct{ f *excelize.File }

func (x *xlsx) Sheets() []string { return x.f.GetSheetList() }
func (x *xlsx) Close() error     { return x.f.Close() }

func (x *xlsx) Each(sheet string, fn func(int, []string) error) error {
	rows, err := x.f.Rows(sheet)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		cells, err := rows.Columns()
		if err != nil {
			return err
		}
		if err := fn(n, cells); err != nil {
			return err
		}
	}
	return rows.Error()
}

type csvBook struct {
	data []byte
	name string
}

func (c *csvBook) Sheets() []string { return []string{c.name} }
func (c *csvBook) Close() error     { return nil }

func (c *csvBook) Each(_ string, fn func(int, []string) error) error {
	cr := csv.NewReader(bytes.NewReader(c.data))
	cr.Comma = detectDelimiter(c.data)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	n := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("csv line %d: %w", n+1, err)
		}
		n++
		if err := fn(n, rec); err != nil {
			return err
		}
	}
}

// detectDelimiter picks , ; | or tab by frequency in the first line.
func detectDelimiter(b []byte) rune {
	first, _ := bufio.NewReader(bytes.NewReader(b)).ReadString('\n')
	best, bestN := ',', strings.Count(first, ",")
	for _, d := range []rune{';', '\t', '|'} {
		if n := strings.Count(first, string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

// IsBlank reports whether every cell is empty.
func IsBlank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// Cell returns cells[i] trimmed ("" when out of range).
func Cell(cells []string, i int) string {
	if i < 0 || i >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[i])
}
