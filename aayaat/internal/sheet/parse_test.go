package sheet

import (
	"strings"
	"testing"
)

func TestParsePaiseIndianFormats(t *testing.T) {
	cases := map[string]int64{
		"1,23,456.78": 12345678, "₹ 1,200": 120000, "Rs. 500.5": 50050, "(1,200.00)": -120000,
		"-45": -4500, "1200.00-": -120000, "": 0, "0.005": 1, "12.344": 1234, "1.2E3": 120000, "INR 99": 9900,
	}
	for in, want := range cases {
		got, err := ParsePaise(in)
		if err != nil || got != want {
			t.Errorf("ParsePaise(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "12.3.4", "1,2a"} {
		if _, err := ParsePaise(bad); err == nil {
			t.Errorf("ParsePaise(%q) should fail", bad)
		}
	}
}

func TestParseDrCr(t *testing.T) {
	cases := []struct {
		in   string
		p    int64
		side string
	}{
		{"1,200.50 Dr", 120050, "dr"}, {"1,200.50 Cr", 120050, "cr"}, {"Cr 300", 30000, "cr"},
		{"-450", 45000, "cr"}, {"450", 45000, ""}, {"2,000 Dr.", 200000, "dr"}, {"500 credit", 50000, "cr"},
		{"-100 Dr", 10000, "cr"},
	}
	for _, c := range cases {
		p, side, err := ParseDrCr(c.in)
		if err != nil || p != c.p || side != c.side {
			t.Errorf("ParseDrCr(%q) = %d %q %v; want %d %q", c.in, p, side, err, c.p, c.side)
		}
	}
}

func TestParseRateQtyDate(t *testing.T) {
	for in, want := range map[string]int{"18": 1800, "18%": 1800, "GST 5%": 500, "0.25": 25, "": 0, "12.00 %": 1200} {
		if got, err := ParseRateBP(in); err != nil || got != want {
			t.Errorf("ParseRateBP(%q)=%d %v want %d", in, got, err, want)
		}
	}
	if _, err := ParseRateBP("180"); err == nil {
		t.Error("rate above 100 must fail")
	}
	if q, err := ParseQty("1,250.5 Pcs"); err != nil || q != 1250.5 {
		t.Errorf("qty %v %v", q, err)
	}
	dates := map[string]string{
		"31/03/2026": "2026-03-31", "1-4-2026": "2026-04-01", "2026-04-01": "2026-04-01",
		"12/26": "2026-12-31", "02/2028": "2028-02-29", "46113": "2026-04-01", "05-Oct-2026": "2026-10-05",
	}
	for in, want := range dates {
		if got, err := ParseDate(in); err != nil || got != want {
			t.Errorf("ParseDate(%q)=%q %v want %q", in, got, err, want)
		}
	}
	if _, err := ParseDate("31st March"); err == nil {
		t.Error("garbage date must fail")
	}
}

func TestCSVWorkbook(t *testing.T) {
	wb, err := Open(strings.NewReader("\xef\xbb\xbfParty Name;GSTIN No\nRam Traders;27AAPFU0939F1ZV\n"), "parties.csv")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	_ = wb.Each(wb.Sheets()[0], func(_ int, c []string) error { rows = append(rows, c); return nil })
	if len(rows) != 2 || rows[0][0] != "Party Name" || rows[1][1] != "27AAPFU0939F1ZV" {
		t.Fatalf("rows %v", rows)
	}
	if _, err := Open(strings.NewReader(""), "old.xls"); err != ErrUnsupported {
		t.Fatal("xls must be unsupported")
	}
}
