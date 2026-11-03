package bankstmt

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSBICSVWithPreamble(t *testing.T) {
	csv := "Account Name :,MAAPAN TRADERS\n" +
		"Account Number :,00000012345678901\n" +
		"\n" +
		"Txn Date,Value Date,Description,Ref No./Cheque No.,Debit,Credit,Balance\n" +
		"1 Oct 2026,1 Oct 2026,BY TRANSFER-NEFT*HDFC0000001*UTR123456789*RAMESH,UTR123456789,,\"10,000.00\",\"1,10,000.00\"\n" +
		"3 Oct 2026,3 Oct 2026,TO CLG CHQ PAID,000777,\"5,000.00\",,\"1,05,000.00\"\n" +
		"5 Oct 2026,5 Oct 2026,SMS CHARGES,, 59.00 ,,\"1,04,941.00\"\n" +
		"\"**This is a computer generated statement**\"\n"
	lines, err := Parse([]byte(csv), "sbi.csv", "sbi", Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines %d", len(lines))
	}
	l := lines[0]
	if l.Date != "2026-10-01" || l.Deposit != 1000000 || l.Withdrawal != 0 || l.Reference != "UTR123456789" || *l.Balance != 11000000 {
		t.Fatalf("%+v", l)
	}
	if lines[1].Withdrawal != 500000 || lines[1].Reference != "000777" || lines[2].Withdrawal != 5900 {
		t.Fatalf("%+v %+v", lines[1], lines[2])
	}
}

func TestHDFCDayFirstShortYear(t *testing.T) {
	csv := "Date,Narration,Chq./Ref.No.,Value Dt,Withdrawal Amt.,Deposit Amt.,Closing Balance\n" +
		"02/10/26,UPI-RAMESH-PAYMENT,0000412345678901,02/10/26,0.00,2500.50,52500.50\n" +
		"12/10/26,ACH D- LIC,0000000000000000,12/10/26,1200.00,0.00,51300.50\n"
	lines, err := Parse([]byte(csv), "hdfc.csv", "hdfc", Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if lines[0].Date != "2026-10-02" || lines[0].Deposit != 250050 || lines[1].Date != "2026-10-12" || lines[1].Withdrawal != 120000 {
		t.Fatalf("%+v", lines)
	}
}

func TestKotakAmountWithDrCr(t *testing.T) {
	csv := "Sl. No.,Transaction Date,Value Date,Description,Chq / Ref No.,Amount,Dr / Cr,Balance\n" +
		"1,05-10-2026,05-10-2026,IMPS RECEIVED,IMPS123,\"1,500.00\",CR,\"9,500.00\"\n" +
		"2,06-10-2026,06-10-2026,ATM WDL,,500.00,DR,\"9,000.00\"\n"
	lines, err := Parse([]byte(csv), "kotak.csv", "kotak", Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if lines[0].Deposit != 150000 || lines[1].Withdrawal != 50000 || lines[1].Date != "2026-10-06" {
		t.Fatalf("%+v", lines)
	}
}

func TestCustomMappingAndTabs(t *testing.T) {
	tsv := "When\tWhat\tOut\tIn\n2026-10-07\tRENT\t15000\t\n2026-10-08\tINTEREST\t\t12.34\n"
	lines, err := Parse([]byte(tsv), "x.txt", "generic", Mapping{Date: "When", Description: "What", Debit: "Out", Credit: "In", DateFormat: "yyyy-mm-dd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Withdrawal != 1500000 || lines[1].Deposit != 1234 {
		t.Fatalf("%+v", lines)
	}
	if _, err := Parse([]byte("a,b\n1,2\n"), "x.csv", "generic", Mapping{}); err == nil {
		t.Fatal("no header must fail")
	}
	if _, err := Parse([]byte("x"), "x.csv", "nope", Mapping{}); err == nil {
		t.Fatal("unknown preset")
	}
}

func xlsxFile(t *testing.T, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var shared []string
	idx := map[string]int{}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for r, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, r+1)
		for c, v := range row {
			ref := fmt.Sprintf("%c%d", 'A'+c, r+1)
			if v == "" {
				continue
			}
			if strings.HasPrefix(v, "#") { // numeric cell
				fmt.Fprintf(&sheet, `<c r="%s"><v>%s</v></c>`, ref, v[1:])
				continue
			}
			i, ok := idx[v]
			if !ok {
				i = len(shared)
				idx[v] = i
				shared = append(shared, v)
			}
			fmt.Fprintf(&sheet, `<c r="%s" t="s"><v>%d</v></c>`, ref, i)
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	var sst strings.Builder
	sst.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range shared {
		fmt.Fprintf(&sst, `<si><t>%s</t></si>`, s)
	}
	sst.WriteString(`</sst>`)
	for name, body := range map[string]string{"xl/worksheets/sheet1.xml": sheet.String(), "xl/sharedStrings.xml": sst.String()} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestICICIXLSXWithSerialDates(t *testing.T) {
	data := xlsxFile(t, [][]string{
		{"DETAILED STATEMENT"},
		{},
		{"S No.", "Value Date", "Transaction Date", "Cheque Number", "Transaction Remarks", "Withdrawal Amount (INR )", "Deposit Amount (INR )", "Balance (INR )"},
		{"#1", "#46296", "#46296", "", "NEFT-UTR998877-ACME", "#0", "#25000.5", "#125000.5"},
		{"#2", "#46297", "#46297", "000123", "CLG CHQ", "#3000", "#0", "#122000.5"},
	})
	lines, err := Parse(data, "icici.xlsx", "icici", Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	// Excel serial 46296 = 2026-10-01
	if len(lines) != 2 || lines[0].Date != "2026-10-01" || lines[0].Deposit != 2500050 || lines[1].Withdrawal != 300000 ||
		lines[1].Reference != "000123" || *lines[1].Balance != 12200050 {
		t.Fatalf("%+v", lines)
	}
	if _, err := Parse([]byte("x"), "old.xls", "icici", Mapping{}); err == nil {
		t.Fatal(".xls must be refused")
	}
}

func TestParseDate(t *testing.T) {
	for in, want := range map[string]string{"01/10/2026": "2026-10-01", "1/10/26": "2026-10-01", "01-Oct-2026": "2026-10-01",
		"01-Oct-26": "2026-10-01", "2026-10-01": "2026-10-01", "01.10.2026": "2026-10-01", "46296": "2026-10-01",
		"01/10/2026 10:15:00": "2026-10-01"} {
		if got, err := ParseDate(in, ""); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := ParseDate("Opening Balance", ""); err == nil {
		t.Fatal("text is not a date")
	}
	if got, _ := ParseDate("10/01/2026", "mm/dd/yyyy"); got != "2026-10-01" {
		t.Fatal(got)
	}
}
