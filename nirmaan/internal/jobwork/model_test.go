package jobwork

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDueDatesAndStatus(t *testing.T) {
	sent := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if d := DueDate(sent, GoodsInputs, false); d != "2027-04-14" {
		t.Errorf("inputs due %s", d)
	}
	if d := DueDate(sent, GoodsCapital, false); d != "2029-04-14" {
		t.Errorf("capital goods due %s", d)
	}
	if d := DueDate(sent, GoodsCapital, true); d != "" {
		t.Errorf("exempt due %s", d)
	}
	for _, c := range []struct {
		asOf    string
		pending float64
		want    string
	}{
		{"2026-06-01", 5, DueOK}, {"2027-03-20", 5, DueSoon}, {"2027-04-15", 5, DueOverdue}, {"2027-04-15", 0, DueSettled},
	} {
		if got, _ := DueStatus("2027-04-14", c.asOf, c.pending); got != c.want {
			t.Errorf("%s pending %v: %s want %s", c.asOf, c.pending, got, c.want)
		}
	}
	if got, _ := DueStatus("", "2030-01-01", 1); got != DueExempt {
		t.Errorf("exempt = %s", got)
	}
}

func TestWarnings(t *testing.T) {
	f := ChallanFacts{ChallanDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), PrincipalState: "27", JobWorkerState: "24",
		TotalValuePaise: 10_000_00, Lines: []FactLine{{GoodsInputs, false}, {GoodsCapital, false}, {GoodsCapital, true}}}
	all := strings.Join(Warnings(f), "\n")
	for _, want := range []string{"2027-04-30", "2029-04-30", "no return time limit", "Inter-state", "No e-way bill", "Unregistered", "ITC-04"} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings miss %q:\n%s", want, all)
		}
	}
	f.JobWorkerState, f.JobWorkerGSTIN = "27", "27AAAAA0000A1Z5"
	all = strings.Join(Warnings(f), "\n")
	if strings.Contains(all, "e-way") {
		t.Errorf("intra-state ₹10,000 needs no e-way bill:\n%s", all)
	}
	f.TotalValuePaise = 60_000_00
	if !strings.Contains(strings.Join(Warnings(f), "\n"), "above ₹50,000") {
		t.Error("₹60,000 consignment without e-way warning")
	}
}

func TestParsePeriod(t *testing.T) {
	for in, want := range map[string][2]string{
		"2026-27":    {"2026-04-01", "2027-03-31"},
		"2026-27-H1": {"2026-04-01", "2026-09-30"},
		"2026-27-H2": {"2026-10-01", "2027-03-31"},
		"2026-27-Q3": {"2026-10-01", "2026-12-31"},
		"2026-27-Q4": {"2027-01-01", "2027-03-31"},
	} {
		p, err := ParsePeriod(in, "", "")
		if err != nil || p.From != want[0] || p.To != want[1] {
			t.Errorf("%s = %+v %v", in, p, err)
		}
	}
	for _, bad := range []string{"", "2026-28", "2026-27-Q5", "x"} {
		if _, err := ParsePeriod(bad, "", ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, err := ParsePeriod("", "2026-05-01", "2026-05-31"); err != nil || p.From != "2026-05-01" {
		t.Errorf("from/to = %+v %v", p, err)
	}
}

func TestAllocate(t *testing.T) {
	got := Allocate(100, []int64{1, 1, 1})
	if got[0]+got[1]+got[2] != 100 || got[0] != 34 {
		t.Errorf("allocate = %v", got)
	}
	got = Allocate(1_000_000_000_000, []int64{9_000_000_000, 1_000_000_000})
	if got[0] != 900_000_000_000 || got[1] != 100_000_000_000 {
		t.Errorf("big allocate = %v", got)
	}
}

func TestCSVAndTable5A(t *testing.T) {
	ig, cg, sg := TaxSplit(1800, false)
	var buf bytes.Buffer
	if err := WriteTable4CSV(&buf, []Table4Row{{JobWorkerState: "27", ChallanNo: "JW/2026-27/00001", ChallanDate: "2026-05-01",
		GoodsType: GoodsInputs, Description: "Cotton yarn", HSN: "5205", UQC: "KGS", Qty: 100.5, TaxablePaise: 2500050,
		IGSTRateBP: ig, CGSTRateBP: cg, SGSTRateBP: sg}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, ",27,JW/2026-27/00001,01-05-2026,Inputs,Cotton yarn,5205,KGS,100.5,25000.50,0,9,9,0") {
		t.Errorf("table 4 csv:\n%s", out)
	}
	rows := BuildTable5A([]ReceivedLine{
		{Table5ARow: Table5ARow{OrigChallanNo: "JW/1", UQC: "MTR", Qty: 95, ReceiptNo: "JWR/1", ReceiptDate: "2026-05-10"}, ChallanLineNo: 1, Kind: KindProcessed, ReceiptID: "r1"},
		{Table5ARow: Table5ARow{OrigChallanNo: "JW/1", UQC: "KGS", Qty: 2, ReceiptNo: "JWR/1", ReceiptDate: "2026-05-10"}, ChallanLineNo: 1, Kind: KindWaste, ReceiptID: "r1"},
		{Table5ARow: Table5ARow{OrigChallanNo: "JW/1", UQC: "KGS", Qty: 1, ReceiptNo: "JWR/1", ReceiptDate: "2026-05-10"}, ChallanLineNo: 2, Kind: KindWaste, ReceiptID: "r1"},
	})
	if len(rows) != 2 || rows[0].LossesQty != 2 || rows[0].LossesUQC != "KGS" || rows[1].Qty != 0 || rows[1].LossesQty != 1 {
		t.Fatalf("5A rows = %+v", rows)
	}
	buf.Reset()
	if err := WriteTable5ACSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "JWR/1,10-05-2026") {
		t.Errorf("5A falls back to our receipt number:\n%s", buf.String())
	}
}
