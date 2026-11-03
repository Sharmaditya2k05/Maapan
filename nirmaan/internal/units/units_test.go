package units

import "testing"

func TestFactor(t *testing.T) {
	cases := []struct {
		unit string
		iu   ItemUnits
		want float64
		ok   bool
	}{
		{"GM", ItemUnits{Base: "KG"}, 0.001, true},
		{"gms", ItemUnits{Base: "KGS"}, 0.001, true},
		{"KG", ItemUnits{Base: "GM"}, 1000, true},
		{"ML", ItemUnits{Base: "LTR"}, 0.001, true},
		{"DOZ", ItemUnits{Base: "NOS"}, 12, true},
		{"BOX", ItemUnits{Base: "PCS", Alts: map[string]float64{"BOX": 48}}, 48, true},
		{"GM", ItemUnits{Base: "PKT", Alts: map[string]float64{"KG": 2}}, 0.002, true}, // 1 KG = 2 PKT of 500 g
		{"", ItemUnits{Base: "KG"}, 1, true},
		{"GM", ItemUnits{Base: "PCS"}, 0, false},
		{"BOX", ItemUnits{Base: "PCS"}, 0, false},
	}
	for _, c := range cases {
		got, err := Factor(c.unit, c.iu)
		if (err == nil) != c.ok || (c.ok && Round3(got*1000) != Round3(c.want*1000)) {
			t.Errorf("Factor(%s, %+v) = %v, %v", c.unit, c.iu, got, err)
		}
	}
	var cat Catalog
	if f, base, err := cat.FactorFor("x", "kg"); err != nil || f != 1 || base != "KG" {
		t.Errorf("unknown item: %v %v %v", f, base, err)
	}
}

func TestMoney(t *testing.T) {
	if got := MulDiv(1, 1, 2); got != 1 {
		t.Errorf("half rounds away: %d", got)
	}
	if got := MulDiv(-1, 1, 2); got != -1 {
		t.Errorf("negative half: %d", got)
	}
	if got := MulDiv(9_000_000_000_000, 9_000_000, 9_000_000); got != 9_000_000_000_000 {
		t.Errorf("no overflow: %d", got)
	}
	if got := Extend(0.1, 40000); got != 4000 {
		t.Errorf("extend %d", got)
	}
	if got := Rate(60000, 19); got != 3158 {
		t.Errorf("rate %d", got)
	}
	if Rate(100, 0) != 0 {
		t.Error("rate on zero qty")
	}
	if UQC("kg") != "KGS" || UQC("nos") != "PCS" || UQC("weird") != "OTH" {
		t.Error("uqc")
	}
}
