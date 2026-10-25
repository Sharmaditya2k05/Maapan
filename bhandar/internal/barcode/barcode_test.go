package barcode

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"testing"
)

// bits renders modules as a string of '1' (bar) and '0' (space).
func bits(m []bool) string {
	var b strings.Builder
	for _, bar := range m {
		if bar {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// barRuns counts maximal runs of adjacent bars.
func barRuns(m []bool) int {
	n := 0
	for i, bar := range m {
		if bar && (i == 0 || !m[i-1]) {
			n++
		}
	}
	return n
}

// mustEAN appends the check digit to first12.
func mustEAN(t *testing.T, first12 string) string {
	t.Helper()
	c, err := EAN13CheckDigit(first12)
	if err != nil {
		t.Fatalf("EAN13CheckDigit(%q): %v", first12, err)
	}
	return first12 + string(c)
}

// assertWellFormedXML fails unless s parses as XML from start to end.
func assertWellFormedXML(t *testing.T, s string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, s)
		}
	}
}

// ---------------------------------------------------------------- EAN-13

func TestEAN13CheckDigit(t *testing.T) {
	cases := []struct {
		in   string
		want byte
	}{
		{"400638133393", '1'},
		{"590123412345", '7'},
		{"978020137962", '4'},
		{"000000000000", '0'},
		{"210000000001", '2'}, // 2×1 + 1×3 + 1×3 = 8 → 2
	}
	for _, c := range cases {
		got, err := EAN13CheckDigit(c.in)
		if err != nil {
			t.Errorf("EAN13CheckDigit(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("EAN13CheckDigit(%q) = %c, want %c", c.in, got, c.want)
		}
	}

	bad := []string{"", "12345678901", "1234567890123", "12345678901a", "1234567890 1", "12345678901٣"}
	for _, in := range bad {
		if _, err := EAN13CheckDigit(in); err == nil {
			t.Errorf("EAN13CheckDigit(%q): want error, got nil", in)
		}
	}
}

func TestValidEAN13(t *testing.T) {
	valid := []string{"4006381333931", "5901234123457", "9780201379624", "0000000000000"}
	for _, s := range valid {
		if !ValidEAN13(s) {
			t.Errorf("ValidEAN13(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"4006381333932", // wrong check digit
		"400638133393",  // 12 digits
		"40063813339311",
		"400638133393a",
		"4006381333931 ",
		"",
	}
	for _, s := range invalid {
		if ValidEAN13(s) {
			t.Errorf("ValidEAN13(%q) = true, want false", s)
		}
	}
}

func TestInternalEAN13(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "2100000000005"},
		{1, "2100000000012"},
		{42, "2100000000425"},
		{9_999_999_999, "2199999999995"},
	}
	for _, c := range cases {
		got, err := InternalEAN13(c.n)
		if err != nil {
			t.Errorf("InternalEAN13(%d): unexpected error %v", c.n, err)
			continue
		}
		if got != c.want {
			t.Errorf("InternalEAN13(%d) = %q, want %q", c.n, got, c.want)
		}
		if len(got) != 13 || !strings.HasPrefix(got, "21") {
			t.Errorf("InternalEAN13(%d) = %q: want 13 digits starting with 21", c.n, got)
		}
		if !ValidEAN13(got) {
			t.Errorf("ValidEAN13(InternalEAN13(%d) = %q) = false", c.n, got)
		}
		if !IsInternal(got) {
			t.Errorf("IsInternal(InternalEAN13(%d) = %q) = false", c.n, got)
		}
	}

	// InternalEAN13(1) is "21" + "0000000001" + the check digit of that prefix.
	want := mustEAN(t, "21"+"0000000001")
	if got, _ := InternalEAN13(1); got != want {
		t.Errorf("InternalEAN13(1) = %q, want %q", got, want)
	}

	for _, n := range []int64{-1, 10_000_000_000, -9_999_999_999} {
		if got, err := InternalEAN13(n); err == nil {
			t.Errorf("InternalEAN13(%d) = %q, want error", n, got)
		}
	}

	// Consecutive sequence numbers never collide.
	seen := make(map[string]int64)
	for n := int64(0); n < 1000; n++ {
		s, err := InternalEAN13(n)
		if err != nil {
			t.Fatalf("InternalEAN13(%d): %v", n, err)
		}
		if prev, dup := seen[s]; dup {
			t.Fatalf("InternalEAN13(%d) = %q collides with n=%d", n, s, prev)
		}
		seen[s] = n
	}
}

func TestIsInternal(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"2100000000012", true},
		{"2000000000000", true}, // prefix check only; check digit not verified
		{"2999999999999", true},
		{"4006381333931", false},
		{"1200000000000", false},
		{"3000000000000", false},
		{"210000000001", false},   // 12 digits
		{"21000000000123", false}, // 14 digits
		{"21000000000a2", false},
		{"", false},
		{"2", false},
	}
	for _, c := range cases {
		if got := IsInternal(c.in); got != c.want {
			t.Errorf("IsInternal(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  abc-123 ", "ABC-123"},
		{"\tsku 9\n", "SKU 9"},
		{"036000291452", "0036000291452"}, // UPC-A → EAN-13
		{"0036000291452", "0036000291452"},
		{"4006381333931", "4006381333931"},
		{"ABCDEFGHIJKL", "ABCDEFGHIJKL"}, // 12 chars but not digits
		{"12345678901", "12345678901"},   // 11 digits untouched
		{" 036000291452 ", "0036000291452"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := NormalizeCode(c.in); got != c.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// A valid UPC-A stays valid as an EAN-13: the leading 0 has weight 1 and
	// contributes nothing to the checksum.
	if got := NormalizeCode("036000291452"); !ValidEAN13(got) {
		t.Errorf("NormalizeCode(UPC-A) = %q is not a valid EAN-13", got)
	}
}

func TestParseWeighingScale(t *testing.T) {
	cases := []struct {
		ean   string
		plu   string
		value int64
		kind  string
	}{
		{"2012345012509", "12345", 1250, "price"}, // ₹12.50 → 1250 paise
		{"2600042015000", "00042", 1500, "weight"},
		{mustEAN(t, "25"+"99999"+"00001"), "99999", 1, "price"},
		{mustEAN(t, "23"+"00007"+"99999"), "00007", 99999, "price"},
		{mustEAN(t, "26"+"00001"+"00000"), "00001", 0, "weight"},
		{mustEAN(t, "29"+"12345"+"00250"), "12345", 250, "weight"},
	}
	for _, c := range cases {
		if !ValidEAN13(c.ean) {
			t.Fatalf("test code %q is not a valid EAN-13", c.ean)
		}
		plu, value, kind, ok := ParseWeighingScale(c.ean)
		if !ok {
			t.Errorf("ParseWeighingScale(%q): ok = false", c.ean)
			continue
		}
		if plu != c.plu || value != c.value || kind != c.kind {
			t.Errorf("ParseWeighingScale(%q) = (%q, %d, %q), want (%q, %d, %q)",
				c.ean, plu, value, kind, c.plu, c.value, c.kind)
		}
	}

	// Every prefix 20–25 is price and 26–29 is weight.
	for flag := byte('0'); flag <= '9'; flag++ {
		ean := mustEAN(t, "2"+string(flag)+"0000100001")
		_, _, kind, ok := ParseWeighingScale(ean)
		want := "price"
		if flag >= '6' {
			want = "weight"
		}
		if !ok || kind != want {
			t.Errorf("ParseWeighingScale(%q) = (kind %q, ok %v), want (%q, true)", ean, kind, ok, want)
		}
	}

	notOK := []string{
		"4006381333931", // valid but not prefix 2
		"5901234123457",
		"2012345012500", // bad check digit
		"201234501250",  // 12 digits
		"2012345O12509", // letter O
		"",
		mustEAN(t, "120000000001"), // prefix 1
	}
	for _, s := range notOK {
		plu, value, kind, ok := ParseWeighingScale(s)
		if ok || plu != "" || value != 0 || kind != "" {
			t.Errorf("ParseWeighingScale(%q) = (%q, %d, %q, %v), want zero values and ok=false", s, plu, value, kind, ok)
		}
	}
}

// ---------------------------------------------------------- EAN-13 modules

func TestEAN13Tables(t *testing.T) {
	ones := func(s string) int { return strings.Count(s, "1") }
	reverse := func(s string) string {
		b := []byte(s)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		return string(b)
	}
	for d := 0; d < 10; d++ {
		l, g, r := ean13L[d], ean13G[d], ean13R[d]
		if len(l) != 7 || len(g) != 7 || len(r) != 7 {
			t.Fatalf("digit %d: encodings must be 7 modules: L=%q G=%q R=%q", d, l, g, r)
		}
		for i := 0; i < 7; i++ {
			if l[i] == r[i] {
				t.Errorf("digit %d: R %q must be the complement of L %q (module %d)", d, r, l, i)
			}
		}
		if reverse(r) != g {
			t.Errorf("digit %d: G %q must be R %q reversed", d, g, r)
		}
		if ones(l)%2 != 1 {
			t.Errorf("digit %d: L %q must have odd parity", d, l)
		}
		if ones(g)%2 != 0 || ones(r)%2 != 0 {
			t.Errorf("digit %d: G %q and R %q must have even parity", d, g, r)
		}
		if l[0] != '0' || l[6] != '1' || r[0] != '1' || r[6] != '0' || g[0] != '0' || g[6] != '1' {
			t.Errorf("digit %d: L/G must run space→bar and R bar→space: L=%q G=%q R=%q", d, l, g, r)
		}
	}

	// Known left-hand (L) encodings.
	wantL := [10]string{"0001101", "0011001", "0010011", "0111101", "0100011", "0110001", "0101111", "0111011", "0110111", "0001011"}
	if ean13L != wantL {
		t.Errorf("L table = %v, want %v", ean13L, wantL)
	}

	// Parity pattern selected by the first digit.
	wantParity := [10]string{"LLLLLL", "LLGLGG", "LLGGLG", "LLGGGL", "LGLLGG", "LGGLLG", "LGGGLL", "LGLGLG", "LGLGGL", "LGGLGL"}
	if ean13Parity != wantParity {
		t.Errorf("parity table = %v, want %v", ean13Parity, wantParity)
	}
	for d, p := range ean13Parity {
		if p[0] != 'L' {
			t.Errorf("parity[%d] = %q: first encoded digit is always L", d, p)
		}
		if d > 0 && strings.Count(p, "G") != 3 {
			t.Errorf("parity[%d] = %q: first digits 1–9 use exactly three G encodings", d, p)
		}
	}
	if ean13LeftGuard != "101" || ean13CentreGuard != "01010" || ean13RightGuard != "101" {
		t.Errorf("guards = %q %q %q", ean13LeftGuard, ean13CentreGuard, ean13RightGuard)
	}
}

func TestEAN13ModulesKnown(t *testing.T) {
	// 5901234123457: first digit 5 → parity LGGLLG for digits 9 0 1 2 3 4.
	want := "101" +
		"0001011" + // 9 L
		"0100111" + // 0 G
		"0110011" + // 1 G
		"0010011" + // 2 L
		"0111101" + // 3 L
		"0011101" + // 4 G
		"01010" +
		"1100110" + // 1 R
		"1101100" + // 2 R
		"1000010" + // 3 R
		"1011100" + // 4 R
		"1001110" + // 5 R
		"1000100" + // 7 R
		"101"
	if len(want) != 95 {
		t.Fatalf("hand-derived pattern is %d modules, want 95", len(want))
	}
	m, err := EAN13Modules("5901234123457")
	if err != nil {
		t.Fatalf("EAN13Modules: %v", err)
	}
	got := bits(m)
	if !strings.HasPrefix(got, "1010001011") {
		t.Errorf("first modules = %q, want prefix 1010001011 (guard + 9 in L)", got[:10])
	}
	if got != want {
		t.Errorf("EAN13Modules(5901234123457)\n got %s\nwant %s", got, want)
	}

	// First digit 0 uses L for the whole left half: 0036000291452.
	m, err = EAN13Modules("0036000291452")
	if err != nil {
		t.Fatalf("EAN13Modules: %v", err)
	}
	wantLeft := ean13L[0] + ean13L[3] + ean13L[6] + ean13L[0] + ean13L[0] + ean13L[0]
	if left := bits(m[3:45]); left != wantLeft {
		t.Errorf("left half of 0036000291452 = %s, want %s", left, wantLeft)
	}
	wantRight := ean13R[2] + ean13R[9] + ean13R[1] + ean13R[4] + ean13R[5] + ean13R[2]
	if right := bits(m[50:92]); right != wantRight {
		t.Errorf("right half of 0036000291452 = %s, want %s", right, wantRight)
	}
}

func TestEAN13ModulesLengthAndGuards(t *testing.T) {
	codes := []string{"4006381333931", "5901234123457", "9780201379624", "0000000000000"}
	for d := 0; d < 10; d++ { // one code per first digit
		codes = append(codes, mustEAN(t, string(byte('0'+d))+"12345678901"))
	}
	for _, c := range codes {
		m, err := EAN13Modules(c)
		if err != nil {
			t.Errorf("EAN13Modules(%q): %v", c, err)
			continue
		}
		if len(m) != 95 {
			t.Errorf("EAN13Modules(%q): %d modules, want 95", c, len(m))
			continue
		}
		s := bits(m)
		if s[0:3] != "101" || s[45:50] != "01010" || s[92:95] != "101" {
			t.Errorf("EAN13Modules(%q): guards wrong in %s", c, s)
		}
		// Each digit encoding is 7 modules with 2 bars and 2 spaces.
		for i := 0; i < 12; i++ {
			start := 3 + 7*i
			if i >= 6 {
				start = 50 + 7*(i-6)
			}
			if runs := barRuns(m[start : start+7]); runs != 2 {
				t.Errorf("EAN13Modules(%q): digit %d has %d bar runs, want 2", c, i, runs)
			}
		}
	}
}

func TestEAN13ModulesInvalid(t *testing.T) {
	for _, s := range []string{"4006381333932", "123", "", "590123412345", "59012341234567", "590123412345a"} {
		if m, err := EAN13Modules(s); err == nil || m != nil {
			t.Errorf("EAN13Modules(%q) = (%v, %v), want error", s, m, err)
		}
	}
}

// ---------------------------------------------------------------- Code 128

func TestCode128Table(t *testing.T) {
	if n := len(code128Patterns); n != 107 {
		t.Fatalf("table has %d entries, want 107", n)
	}
	seen := make(map[string]int, 107)
	for v, p := range code128Patterns {
		if prev, dup := seen[p]; dup {
			t.Errorf("value %d pattern %q duplicates value %d", v, p, prev)
		}
		seen[p] = v
		if strings.Trim(p, "01") != "" {
			t.Errorf("value %d pattern %q is not binary", v, p)
		}
		if v == code128Stop {
			if p != "1100011101011" {
				t.Errorf("stop pattern = %q, want 1100011101011", p)
			}
			continue
		}
		if len(p) != 11 {
			t.Errorf("value %d pattern %q is %d modules, want 11", v, p, len(p))
			continue
		}
		if p[0] != '1' || p[10] != '0' {
			t.Errorf("value %d pattern %q must start with a bar and end with a space", v, p)
		}
		m := appendPattern(nil, p)
		if runs := barRuns(m); runs != 3 {
			t.Errorf("value %d pattern %q has %d bar runs, want 3", v, p, runs)
		}
		if bars := strings.Count(p, "1"); bars%2 != 0 {
			t.Errorf("value %d pattern %q has %d bar modules, want even", v, p, bars)
		}
		if strings.Contains(p, "11111") || strings.Contains(p, "00000") {
			t.Errorf("value %d pattern %q has an element wider than 4 modules", v, p)
		}
	}
	if code128Patterns[code128StartB] != "11010010000" {
		t.Errorf("start B = %q, want 11010010000", code128Patterns[code128StartB])
	}
	if code128Patterns[0] != "11011001100" {
		t.Errorf("value 0 (space) = %q, want 11011001100", code128Patterns[0])
	}
	if code128Patterns['A'-32] != "10100011000" {
		t.Errorf("value 33 (A) = %q, want 10100011000", code128Patterns[33])
	}
}

func TestCode128B_ABC(t *testing.T) {
	// Checksum per the spec: (104 + 33×1 + 34×2 + 35×3) mod 103 = 310 mod 103 = 1.
	if sum := (104 + 33*1 + 34*2 + 35*3) % 103; sum != 1 {
		t.Fatalf("spec arithmetic: checksum = %d, want 1", sum)
	}
	m, err := Code128B("ABC")
	if err != nil {
		t.Fatalf("Code128B(ABC): %v", err)
	}
	want := "11010010000" + // start B (104)
		"10100011000" + // A (33)
		"10001011000" + // B (34)
		"10001000110" + // C (35)
		"11001101100" + // check symbol value 1
		"1100011101011" // stop, ending in the 2-module termination bar
	if len(want) != 11*(1+3+1)+13 {
		t.Fatalf("hand-derived pattern is %d modules, want %d", len(want), 11*5+13)
	}
	if got := bits(m); got != want {
		t.Errorf("Code128B(ABC)\n got %s\nwant %s", got, want)
	}
	if n := len(m); n != 11*(1+3+1)+13 {
		t.Errorf("Code128B(ABC): %d modules, want %d", n, 11*5+13)
	}
	if !m[len(m)-1] || !m[len(m)-2] || m[len(m)-3] {
		t.Errorf("Code128B(ABC) must end with the 2-module termination bar after a space")
	}
}

func TestCode128BLength(t *testing.T) {
	for _, n := range []int{1, 2, 10, 13, 42, 79, 80} {
		s := strings.Repeat("x", n)
		m, err := Code128B(s)
		if err != nil {
			t.Errorf("Code128B(%d chars): %v", n, err)
			continue
		}
		if want := 11*(1+n+1) + 13; len(m) != want {
			t.Errorf("Code128B(%d chars): %d modules, want %d", n, len(m), want)
		}
		if !m[0] {
			t.Errorf("Code128B(%d chars): must start with a bar", n)
		}
		if got := bits(m[len(m)-13:]); got != code128Patterns[code128Stop] {
			t.Errorf("Code128B(%d chars): ends with %s, want stop pattern", n, got)
		}
	}
}

func TestCode128BChecksum(t *testing.T) {
	inputs := []string{" ", "~", "A", "ABC", "Hello, World!", "MAAPAN-0001", "sku_42/B", strings.Repeat(" ~", 40)}
	for _, s := range inputs {
		m, err := Code128B(s)
		if err != nil {
			t.Fatalf("Code128B(%q): %v", s, err)
		}
		got := bits(m)
		// Re-derive the symbol sequence independently and compare piecewise.
		sum := 104
		pos := 0
		expect := func(what string, pattern string) {
			t.Helper()
			if got[pos:pos+len(pattern)] != pattern {
				t.Errorf("Code128B(%q): %s = %s, want %s", s, what, got[pos:pos+len(pattern)], pattern)
			}
			pos += len(pattern)
		}
		expect("start", code128Patterns[104])
		for i := 0; i < len(s); i++ {
			v := int(s[i]) - 32
			sum += v * (i + 1)
			expect("char "+string(s[i]), code128Patterns[v])
		}
		expect("checksum", code128Patterns[sum%103])
		expect("stop", code128Patterns[106])
		if pos != len(got) {
			t.Errorf("Code128B(%q): %d modules, consumed %d", s, len(got), pos)
		}
	}
}

func TestCode128BErrors(t *testing.T) {
	bad := []string{
		"",
		strings.Repeat("a", 81),
		"tab\there",
		"new\nline",
		"\x7f",
		"café", // non-ASCII bytes are not printable ASCII
		"\x00",
	}
	for _, s := range bad {
		if m, err := Code128B(s); err == nil || m != nil {
			t.Errorf("Code128B(%q) = (%v, %v), want error", s, m, err)
		}
	}
	// Boundaries that are fine.
	for _, s := range []string{" ", "~", strings.Repeat("Z", 80), " !\"#$%&'()*+,-./0123456789:;<=>?@[\\]^_`{|}~"} {
		if _, err := Code128B(s); err != nil {
			t.Errorf("Code128B(%q): unexpected error %v", s, err)
		}
	}
}

// --------------------------------------------------------------------- SVG

func TestSVGBasic(t *testing.T) {
	m, err := Code128B("ABC")
	if err != nil {
		t.Fatal(err)
	}
	svg := SVG(m, "ABC", Options{})
	if !strings.HasPrefix(svg, "<svg") {
		t.Errorf("SVG must start with <svg, got %.20q", svg)
	}
	if !strings.HasSuffix(svg, "</svg>") {
		t.Errorf("SVG must end with </svg>")
	}
	width := (len(m) + 2*10) * 2 // defaults: margin 10 modules, module 2 px
	if !strings.Contains(svg, `width="176"`) || width != 176 {
		t.Errorf("SVG width attribute: want width=\"176\" (computed %d) in %s", width, svg)
	}
	if !strings.Contains(svg, ` height="`) {
		t.Errorf("SVG lacks a height attribute: %s", svg)
	}
	if got, want := strings.Count(svg, "<rect"), barRuns(m); got != want {
		t.Errorf("SVG has %d <rect, want one per bar run = %d", got, want)
	}
	if !strings.Contains(svg, "<text") || !strings.Contains(svg, ">ABC</text>") {
		t.Errorf("SVG with default options must print the label: %s", svg)
	}
	if !strings.Contains(svg, `fill="#fff"`) || !strings.Contains(svg, `fill="#000"`) {
		t.Errorf("SVG must draw black bars on white: %s", svg)
	}
	// First bar (start code B begins with a 2-module bar) sits after the quiet zone.
	if !strings.Contains(svg, `<rect x="20" y="0" width="4" height="60"/>`) {
		t.Errorf("first bar misplaced in %s", svg)
	}
	assertWellFormedXML(t, svg)
}

func TestSVGEscapesLabel(t *testing.T) {
	m, _ := Code128B("X")
	label := `A<B&C>"D'E`
	svg := SVG(m, label, DefaultOptions())
	if strings.Contains(svg, "<B") || strings.Contains(svg, "&C") || strings.Contains(svg, label) {
		t.Errorf("label not escaped: %s", svg)
	}
	for _, want := range []string{"&lt;", "&amp;", "&gt;", "&quot;", "&apos;"} {
		if !strings.Contains(svg, want) {
			t.Errorf("escaped label should contain %s: %s", want, svg)
		}
	}
	assertWellFormedXML(t, svg)

	// Control characters are dropped rather than emitted as invalid XML.
	svg = SVG(m, "a\x01b", DefaultOptions())
	if !strings.Contains(svg, ">ab</text>") {
		t.Errorf("control character not dropped: %s", svg)
	}
	assertWellFormedXML(t, svg)
}

func TestSVGOptions(t *testing.T) {
	m, _ := Code128B("ABC")

	// Partially filled options: Text false is honoured, zero sizes default.
	svg := SVG(m, "ABC", Options{Module: 3, Height: 40, Margin: 5})
	if strings.Contains(svg, "<text") {
		t.Errorf("Text=false must not print a label: %s", svg)
	}
	if want := (len(m) + 10) * 3; !strings.Contains(svg, `width="`+strconv.Itoa(want)+`"`) {
		t.Errorf("width should be %d: %s", want, svg)
	}
	if !strings.Contains(svg, ` height="40"`) || !strings.Contains(svg, `height="40"/>`) {
		t.Errorf("height should be 40 with no label band: %s", svg)
	}
	if !strings.Contains(svg, `<rect x="15" y="0" width="6" height="40"/>`) {
		t.Errorf("first bar should start at margin 5×3 px with width 2×3 px: %s", svg)
	}
	if got, want := strings.Count(svg, "<rect"), barRuns(m); got != want {
		t.Errorf("%d <rect, want %d", got, want)
	}
	assertWellFormedXML(t, svg)

	// DefaultOptions with one override keeps the label.
	o := DefaultOptions()
	o.Height = 30
	svg = SVG(m, "ABC", o)
	if !strings.Contains(svg, ">ABC</text>") || !strings.Contains(svg, `height="30"/>`) {
		t.Errorf("DefaultOptions override: %s", svg)
	}
	assertWellFormedXML(t, svg)

	// Negative sizes fall back to defaults.
	svg = SVG(m, "ABC", Options{Module: -1, Height: -1, Margin: -1, Text: true})
	if !strings.Contains(svg, `width="176"`) || !strings.Contains(svg, `height="60"/>`) {
		t.Errorf("negative sizes should default: %s", svg)
	}

	// Text requested but empty label: no <text> element and no label band.
	svg = SVG(m, "", DefaultOptions())
	if strings.Contains(svg, "<text") || !strings.Contains(svg, ` height="60" `) {
		t.Errorf("empty label: %s", svg)
	}
	assertWellFormedXML(t, svg)

	// No modules at all is still a valid, empty image.
	svg = SVG(nil, "", Options{})
	if strings.Count(svg, "<rect") != 0 || !strings.Contains(svg, `width="40"`) {
		t.Errorf("empty modules: %s", svg)
	}
	assertWellFormedXML(t, svg)
}

func TestSVGMergesAdjacentBars(t *testing.T) {
	m := appendPattern(nil, "1101110001")
	svg := SVG(m, "", Options{Module: 1, Height: 10, Margin: 1})
	if got := strings.Count(svg, "<rect"); got != 3 {
		t.Errorf("%d <rect, want 3 bar runs: %s", got, svg)
	}
	for _, want := range []string{
		`<rect x="1" y="0" width="2" height="10"/>`,
		`<rect x="4" y="0" width="3" height="10"/>`,
		`<rect x="10" y="0" width="1" height="10"/>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %s in %s", want, svg)
		}
	}
	if !strings.Contains(svg, `width="12" height="10"`) {
		t.Errorf("size should be 12×10: %s", svg)
	}
}

func TestRender(t *testing.T) {
	// Valid EAN-13 → 95 modules → (95+20)×2 = 230 px wide.
	svg, err := Render("5901234123457", Options{})
	if err != nil {
		t.Fatalf("Render(EAN): %v", err)
	}
	if !strings.Contains(svg, `width="230"`) || !strings.Contains(svg, ">5901234123457</text>") {
		t.Errorf("Render(EAN) = %s", svg)
	}
	// Bar runs: left guard 2 + centre guard 2 + right guard 2 + 12 digits × 2
	// = 30. Runs never merge across boundaries: L/G codes start with a space,
	// R codes end with one, and the centre guard starts with one.
	if got := strings.Count(svg, "<rect"); got != 30 {
		t.Errorf("Render(EAN) has %d <rect, want 30", got)
	}
	assertWellFormedXML(t, svg)

	// Invalid check digit → Code 128 B of the 13 characters →
	// 11×15+13 = 178 modules → (178+20)×2 = 396 px.
	svg, err = Render("4006381333932", Options{})
	if err != nil {
		t.Fatalf("Render(bad EAN): %v", err)
	}
	if !strings.Contains(svg, `width="396"`) {
		t.Errorf("Render(bad EAN) should fall back to Code 128: %s", svg)
	}
	assertWellFormedXML(t, svg)

	// Alphanumeric → Code 128 B.
	svg, err = Render("ABC", Options{})
	if err != nil {
		t.Fatalf("Render(ABC): %v", err)
	}
	if !strings.Contains(svg, `width="176"`) || !strings.Contains(svg, ">ABC</text>") {
		t.Errorf("Render(ABC) = %s", svg)
	}

	// Code 128 errors propagate.
	for _, bad := range []string{"", strings.Repeat("a", 81), "café"} {
		if svg, err := Render(bad, Options{}); err == nil || svg != "" {
			t.Errorf("Render(%q) = (%q, %v), want error", bad, svg, err)
		}
	}
}
