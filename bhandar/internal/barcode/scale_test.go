package barcode

import "testing"

func withCheck(t *testing.T, first12 string) string {
	t.Helper()
	c, err := EAN13CheckDigit(first12)
	if err != nil {
		t.Fatal(err)
	}
	return first12 + string(c)
}

func TestParseScaleDefaultMatchesLegacy(t *testing.T) {
	for _, first12 := range []string{"200123401250", "270123400750", "251234599999"} {
		ean := withCheck(t, first12)
		p1, v1, k1, ok1 := ParseWeighingScale(ean)
		p2, v2, k2, ok2 := ParseScale(ean, DefaultScaleFormat(ean[:2]))
		if p1 != p2 || v1 != v2 || k1 != k2 || ok1 != ok2 {
			t.Errorf("%s: legacy (%s %d %s %v) vs default (%s %d %s %v)", ean, p1, v1, k1, ok1, p2, v2, k2, ok2)
		}
	}
}

func TestParseScaleLayouts(t *testing.T) {
	cases := []struct {
		name    string
		first12 string
		f       ScaleFormat
		plu     string
		value   int64
		kind    string
	}{
		// 6-digit PLU, 4-digit price in rupees with 1 decimal: 0125 → ₹12.5 = 1250 paise
		{"plu6 price 1dp", "220001230125", ScaleFormat{Prefix: "22", Kind: "price", PLULen: 6, ValueDecimals: 1}, "000123", 1250, "price"},
		// 5-digit PLU + internal check digit + 4-digit weight in kg with 3 decimals: 1500 g
		{"plu5 check weight", "280012371500", ScaleFormat{Prefix: "28", Kind: "weight", PLULen: 5, CheckDigit: true, ValueDecimals: 3}, "00123", 1500, "weight"},
		// 4-digit PLU, 6-digit weight in kg with 2 decimals: 000125 → 1.25 kg = 1250 g
		{"plu4 weight 2dp", "291234000125", ScaleFormat{Prefix: "29", Kind: "weight", PLULen: 4, ValueDecimals: 2}, "1234", 1250, "weight"},
		// price in whole rupees: 00099 → ₹99
		{"price rupees", "230042000099", ScaleFormat{Prefix: "23", Kind: "price", PLULen: 5, ValueDecimals: 0}, "00420", 9900, "price"},
	}
	for _, c := range cases {
		ean := withCheck(t, c.first12)
		plu, v, k, ok := ParseScale(ean, c.f)
		if !ok || plu != c.plu || v != c.value || k != c.kind {
			t.Errorf("%s: got (%s %d %s %v), want (%s %d %s)", c.name, plu, v, k, ok, c.plu, c.value, c.kind)
		}
	}
}

func TestParseScaleRejects(t *testing.T) {
	ean := withCheck(t, "210000000001")
	if _, _, _, ok := ParseScale(ean, ScaleFormat{Prefix: "21", Kind: "none"}); ok {
		t.Error("kind none must not parse")
	}
	if _, _, _, ok := ParseScale(ean, DefaultScaleFormat("22")); ok {
		t.Error("prefix mismatch must not parse")
	}
	if _, _, _, ok := ParseScale("2100000000010", DefaultScaleFormat("21")); ok {
		t.Error("bad check digit must not parse")
	}
	bad := []ScaleFormat{
		{Prefix: "30", Kind: "price", PLULen: 5},
		{Prefix: "20", Kind: "price", PLULen: 7},
		{Prefix: "20", Kind: "price", PLULen: 6, CheckDigit: true, ValueDecimals: 2}, // 3 value digits
		{Prefix: "20", Kind: "price", PLULen: 5, ValueDecimals: 3},
		{Prefix: "20", Kind: "volume", PLULen: 5},
	}
	for _, f := range bad {
		if f.Validate() == nil {
			t.Errorf("%+v must be invalid", f)
		}
	}
}
