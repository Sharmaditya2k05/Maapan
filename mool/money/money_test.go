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
