package sheet

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestXLSXWorkbook(t *testing.T) {
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", "Parties")
	_ = f.SetSheetRow("Parties", "A1", &[]any{"SHARMA TRADERS"})
	_ = f.SetSheetRow("Parties", "A3", &[]any{"Party Name", "Opening Bal"})
	_ = f.SetSheetRow("Parties", "A4", &[]any{"Ram Stores", 1200.5})
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	wb, err := Open(&buf, "export.XLSX")
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	if s := wb.Sheets(); len(s) != 1 || s[0] != "Parties" {
		t.Fatalf("sheets %v", s)
	}
	got := map[int][]string{}
	_ = wb.Each("Parties", func(n int, c []string) error { got[n] = c; return nil })
	if got[3][0] != "Party Name" || got[4][1] != "1200.5" {
		t.Fatalf("rows %v", got)
	}
}
