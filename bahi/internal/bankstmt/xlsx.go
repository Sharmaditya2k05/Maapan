package bankstmt

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// readXLSX returns the first worksheet of an .xlsx file as rows of strings.
// It is a minimal reader (shared strings, inline strings, numbers) — enough
// for bank exports; formulas are read from their cached value.
func readXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("xlsx: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, nil
		}
		if f.UncompressedSize64 > 64<<20 {
			return nil, errors.New("xlsx: part too large")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 64<<20))
	}

	var shared []string
	if b, err := read("xl/sharedStrings.xml"); err != nil {
		return nil, err
	} else if b != nil {
		var sst struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(b, &sst); err != nil {
			return nil, fmt.Errorf("xlsx shared strings: %w", err)
		}
		for _, si := range sst.SI {
			s := si.T
			for _, r := range si.R {
				s += r.T
			}
			shared = append(shared, s)
		}
	}

	sheet := firstSheet(files, read)
	if sheet == "" {
		return nil, errors.New("xlsx: no worksheet found")
	}
	b, err := read(sheet)
	if err != nil {
		return nil, err
	}
	var ws struct {
		Rows []struct {
			C []struct {
				R  string `xml:"r,attr"`
				T  string `xml:"t,attr"`
				V  string `xml:"v"`
				Is struct {
					T string `xml:"t"`
					R []struct {
						T string `xml:"t"`
					} `xml:"r"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(b, &ws); err != nil {
		return nil, fmt.Errorf("xlsx sheet: %w", err)
	}
	var out [][]string
	for _, row := range ws.Rows {
		var cells []string
		for i, c := range row.C {
			col := i
			if c.R != "" {
				col = colIndex(c.R)
			}
			var v string
			switch c.T {
			case "s":
				var n int
				if _, err := fmt.Sscanf(c.V, "%d", &n); err == nil && n >= 0 && n < len(shared) {
					v = shared[n]
				}
			case "inlineStr":
				v = c.Is.T
				for _, r := range c.Is.R {
					v += r.T
				}
			default:
				v = c.V
			}
			for len(cells) <= col {
				cells = append(cells, "")
			}
			cells[col] = v
		}
		out = append(out, cells)
	}
	return out, nil
}

// firstSheet resolves the first sheet in workbook order, falling back to the
// lowest-named worksheet part.
func firstSheet(files map[string]*zip.File, read func(string) ([]byte, error)) string {
	wb, _ := read("xl/workbook.xml")
	rels, _ := read("xl/_rels/workbook.xml.rels")
	if wb != nil && rels != nil {
		var w struct {
			Sheets []struct {
				RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sheets>sheet"`
		}
		var r struct {
			Rel []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		if xml.Unmarshal(wb, &w) == nil && xml.Unmarshal(rels, &r) == nil && len(w.Sheets) > 0 {
			for _, rel := range r.Rel {
				if rel.ID == w.Sheets[0].RID {
					t := strings.TrimPrefix(rel.Target, "/")
					if !strings.HasPrefix(t, "xl/") {
						t = path.Join("xl", t)
					}
					if _, ok := files[t]; ok {
						return t
					}
				}
			}
		}
	}
	var names []string
	for n := range files {
		if strings.HasPrefix(n, "xl/worksheets/") && strings.HasSuffix(n, ".xml") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// colIndex turns "C12" into 2.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}
