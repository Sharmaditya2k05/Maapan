package money

import "testing"

func TestFormatAndRound(t *testing.T) {
	cases := map[int64]string{0: "₹0.00", 12345678: "₹1,23,456.78", 99: "₹0.99", -150000: "-₹1,500.00", 1234567890: "₹1,23,45,678.90"}
	for in, want := range cases {
		if got := Format(in); got != want {
			t.Errorf("Format(%d)=%s want %s", in, got, want)
		}
	}
	if r, off := RoundToRupee(11199); r != 11200 || off != 1 {
		t.Errorf("round up: %d %d", r, off)
	}
	if r, off := RoundToRupee(11149); r != 11100 || off != -49 {
		t.Errorf("round down: %d %d", r, off)
	}
	if ApplyBP(100000, 1800) != 18000 || MulDiv(-5, 1, 10) != -1 {
		t.Error("ApplyBP/MulDiv")
	}
}

func TestParseRupees(t *testing.T) {
	cases := map[string]int64{
		"1,23,456.78": 12345678, "₹ 99": 9900, "Rs.1500": 150000, "-250": -25000, "(250)": -25000,
		"0.5": 50, "1.005": 101, "1.004": 100, ".75": 75, "  12  ": 1200, "+3.1": 310, "1,000": 100000,
	}
	for in, want := range cases {
		got, err := ParseRupees(in)
		if err != nil || got != want {
			t.Errorf("ParseRupees(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "12-", "--5"} {
		if _, err := ParseRupees(bad); err == nil {
			t.Errorf("ParseRupees(%q) must fail", bad)
		}
	}
	if FromRupees(1.005) != 101 || FromRupees(99.99) != 9999 || FromRupees(-0.5) != -50 {
		t.Errorf("FromRupees: %d %d %d", FromRupees(1.005), FromRupees(99.99), FromRupees(-0.5))
	}
}
