package sheet

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"
	"unicode/utf16"
)

// biffCell is one cell of a generated test workbook: a string or a number.
type biffCell struct {
	s   string
	n   float64
	num bool
}

// buildXLS writes a minimal Excel 97-2003 (BIFF8) workbook with one sheet in
// a compound file, the way Excel / MargBooks' "Excel 97" export store it.
func buildXLS(t *testing.T, sheetName string, rows [][]biffCell) []byte {
	t.Helper()
	rec := func(buf *bytes.Buffer, typ uint16, data []byte) {
		_ = binary.Write(buf, binary.LittleEndian, typ)
		_ = binary.Write(buf, binary.LittleEndian, uint16(len(data)))
		buf.Write(data)
	}
	bof := func(dt uint16) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint16(b[0:], 0x0600)
		binary.LittleEndian.PutUint16(b[2:], dt)
		binary.LittleEndian.PutUint16(b[4:], 0x0DBB)
		binary.LittleEndian.PutUint16(b[6:], 0x07CC)
		return b
	}
	// shared strings
	var sst []string
	idx := map[string]int{}
	for _, r := range rows {
		for _, c := range r {
			if !c.num {
				if _, ok := idx[c.s]; !ok {
					idx[c.s] = len(sst)
					sst = append(sst, c.s)
				}
			}
		}
	}
	var sheetBuf bytes.Buffer
	rec(&sheetBuf, 0x0809, bof(0x0010))
	for ri, r := range rows {
		for ci, c := range r {
			if c.num {
				d := make([]byte, 14)
				binary.LittleEndian.PutUint16(d[0:], uint16(ri))
				binary.LittleEndian.PutUint16(d[2:], uint16(ci))
				binary.LittleEndian.PutUint16(d[4:], 15)
				binary.LittleEndian.PutUint64(d[6:], math.Float64bits(c.n))
				rec(&sheetBuf, 0x0203, d)
				continue
			}
			d := make([]byte, 10)
			binary.LittleEndian.PutUint16(d[0:], uint16(ri))
			binary.LittleEndian.PutUint16(d[2:], uint16(ci))
			binary.LittleEndian.PutUint16(d[4:], 15)
			binary.LittleEndian.PutUint32(d[6:], uint32(idx[c.s]))
			rec(&sheetBuf, 0x00FD, d)
		}
	}
	rec(&sheetBuf, 0x000A, nil)

	globals := func(sheetPos uint32) []byte {
		var g bytes.Buffer
		rec(&g, 0x0809, bof(0x0005))
		rec(&g, 0x0042, []byte{0xB0, 0x04}) // CODEPAGE 1200
		for i := 0; i < 16; i++ {
			rec(&g, 0x00E0, make([]byte, 20)) // XF
		}
		bs := make([]byte, 8, 8+len(sheetName))
		binary.LittleEndian.PutUint32(bs[0:], sheetPos)
		bs[6] = byte(len(sheetName))
		bs = append(bs, sheetName...)
		rec(&g, 0x0085, bs)
		var s bytes.Buffer
		_ = binary.Write(&s, binary.LittleEndian, uint32(len(sst)))
		_ = binary.Write(&s, binary.LittleEndian, uint32(len(sst)))
		for _, str := range sst {
			u := utf16.Encode([]rune(str))
			_ = binary.Write(&s, binary.LittleEndian, uint16(len(u)))
			s.WriteByte(0x01) // fHighByte: UTF-16LE characters
			for _, ch := range u {
				_ = binary.Write(&s, binary.LittleEndian, ch)
			}
		}
		rec(&g, 0x00FC, s.Bytes())
		rec(&g, 0x000A, nil)
		return g.Bytes()
	}
	g := globals(0)
	g = globals(uint32(len(g)))
	stream := append(g, sheetBuf.Bytes()...)
	if len(stream) < 4096 { // stay out of the mini stream
		stream = append(stream, make([]byte, 4096-len(stream))...)
	}
	if r := len(stream) % 512; r != 0 {
		stream = append(stream, make([]byte, 512-r)...)
	}
	nStream := len(stream) / 512

	const endOfChain, freeSect, fatSect, noStream = 0xFFFFFFFE, 0xFFFFFFFF, 0xFFFFFFFD, 0xFFFFFFFF
	hdr := make([]byte, 512)
	copy(hdr, oleMagic)
	binary.LittleEndian.PutUint16(hdr[0x18:], 0x003E)
	binary.LittleEndian.PutUint16(hdr[0x1A:], 0x0003)
	binary.LittleEndian.PutUint16(hdr[0x1C:], 0xFFFE)
	binary.LittleEndian.PutUint16(hdr[0x1E:], 9)
	binary.LittleEndian.PutUint16(hdr[0x20:], 6)
	binary.LittleEndian.PutUint32(hdr[0x2C:], 1)          // FAT sectors
	binary.LittleEndian.PutUint32(hdr[0x30:], 1)          // first directory sector
	binary.LittleEndian.PutUint32(hdr[0x38:], 4096)       // mini stream cutoff
	binary.LittleEndian.PutUint32(hdr[0x3C:], endOfChain) // no mini FAT
	binary.LittleEndian.PutUint32(hdr[0x44:], endOfChain) // no DIFAT sectors
	binary.LittleEndian.PutUint32(hdr[0x4C:], 0)          // FAT is sector 0
	for i := 1; i < 109; i++ {
		binary.LittleEndian.PutUint32(hdr[0x4C+4*i:], freeSect)
	}
	fat := make([]byte, 512)
	for i := 0; i < 128; i++ {
		binary.LittleEndian.PutUint32(fat[4*i:], freeSect)
	}
	binary.LittleEndian.PutUint32(fat[0:], fatSect)
	binary.LittleEndian.PutUint32(fat[4:], endOfChain)
	for i := 0; i < nStream; i++ {
		next := uint32(2 + i + 1)
		if i == nStream-1 {
			next = endOfChain
		}
		binary.LittleEndian.PutUint32(fat[4*(2+i):], next)
	}
	dir := make([]byte, 512)
	entry := func(i int, name string, typ byte, child, start uint32, size uint32) {
		e := dir[128*i : 128*(i+1)]
		u := utf16.Encode([]rune(name))
		for k, ch := range u {
			binary.LittleEndian.PutUint16(e[2*k:], ch)
		}
		if name != "" {
			binary.LittleEndian.PutUint16(e[0x40:], uint16(2*(len(u)+1)))
		}
		e[0x42], e[0x43] = typ, 1
		binary.LittleEndian.PutUint32(e[0x44:], noStream)
		binary.LittleEndian.PutUint32(e[0x48:], noStream)
		binary.LittleEndian.PutUint32(e[0x4C:], child)
		binary.LittleEndian.PutUint32(e[0x74:], start)
		binary.LittleEndian.PutUint32(e[0x78:], size)
	}
	entry(0, "Root Entry", 5, 1, endOfChain, 0)
	entry(1, "Workbook", 2, noStream, 2, uint32(len(stream)))
	entry(2, "", 0, noStream, 0, 0)
	entry(3, "", 0, noStream, 0, 0)

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(fat)
	out.Write(dir)
	out.Write(stream)
	return out.Bytes()
}

func str(s string) biffCell  { return biffCell{s: s} }
func num(n float64) biffCell { return biffCell{n: n, num: true} }
func TestXLSBinaryWorkbook(t *testing.T) {
	data := buildXLS(t, "Party Master", [][]biffCell{
		{str("SHREE GANESH TRADERS (DEMO)")},
		{str("Party Name"), str("GSTIN No"), str("Opening Bal"), str("Dr/Cr")},
		{str("Sharma Agencies - Pune"), str("27AAKFS4821M1ZQ"), num(12450.5), str("Dr")},
		{str("Konkan Fresh Foods"), str("27AAJFK9034L1ZU"), num(7500), str("Cr")},
	})
	wb, err := Open(bytes.NewReader(data), "PartyMaster.xls")
	if err != nil {
		t.Fatal(err)
	}
	if s := wb.Sheets(); len(s) != 1 || s[0] != "Party Master" {
		t.Fatalf("sheets %v", s)
	}
	got := map[int][]string{}
	_ = wb.Each("Party Master", func(n int, c []string) error { got[n] = c; return nil })
	if got[2][0] != "Party Name" || got[3][0] != "Sharma Agencies - Pune" || got[3][2] != "12450.5" || got[4][2] != "7500" || got[4][3] != "Cr" {
		t.Fatalf("rows %q", got)
	}
}

func TestXLSAsHTMLTable(t *testing.T) {
	b, err := os.ReadFile("../../testdata/marg/PartyMaster-html.xls")
	if err != nil {
		t.Fatal(err)
	}
	wb, err := Open(bytes.NewReader(b), "PartyMaster-html.xls")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	_ = wb.Each(wb.Sheets()[0], func(_ int, c []string) error { rows = append(rows, c); return nil })
	if len(rows) != 4 || len(rows[0]) != 4 || rows[1][0] != "Party Name" || rows[3][3] != "7,500.00 Cr" {
		t.Fatalf("rows %q", rows)
	}
}

func TestXLSAsTabText(t *testing.T) {
	wb, err := Open(bytes.NewReader([]byte("Item Name\tMRP\nParle-G 100g\t10\n")), "items.xls")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	_ = wb.Each(wb.Sheets()[0], func(_ int, c []string) error { rows = append(rows, c); return nil })
	if len(rows) != 2 || rows[1][1] != "10" {
		t.Fatalf("rows %q", rows)
	}
	if _, err := Open(bytes.NewReader(append(append([]byte{}, oleMagic...), 1, 2, 3)), "broken.xls"); err == nil {
		t.Fatal("a damaged compound file must fail, not panic")
	}
}

func TestUTF16Text(t *testing.T) {
	u := utf16.Encode([]rune("Party Name,Mobile\nराम स्टोर्स,9822011223\n"))
	b := []byte{0xFF, 0xFE}
	for _, ch := range u {
		b = binary.LittleEndian.AppendUint16(b, ch)
	}
	wb, err := Open(bytes.NewReader(b), "unicode.txt")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	_ = wb.Each(wb.Sheets()[0], func(_ int, c []string) error { rows = append(rows, c); return nil })
	if len(rows) != 2 || rows[1][0] != "राम स्टोर्स" {
		t.Fatalf("rows %q", rows)
	}
}
