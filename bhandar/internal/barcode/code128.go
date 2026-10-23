package barcode

import (
	"errors"
	"fmt"
)

// This file holds the module encoders: Code 128 subset B and EAN-13. A
// module pattern is a []bool where true is a bar and false is a space, with
// no quiet zone; SVG adds the quiet zone when rendering.

const (
	code128StartB = 104
	code128Stop   = 106
	// maxCode128Len caps the data length so labels stay printable and
	// scannable: 80 characters is already 915 modules wide.
	maxCode128Len = 80
)

// code128Patterns is the module pattern of every Code 128 symbol value.
// Values 0–105 are 11 modules: three bars and three spaces, no element wider
// than 4 modules, and the bars total an even number of modules. The stop
// pattern (106) is 13 modules and ends with the 2-module termination bar.
// Comments give the subset B meaning of each value (value = ASCII − 32 for
// the printable range).
var code128Patterns = [107]string{
	"11011001100",   // 0   space
	"11001101100",   // 1   !
	"11001100110",   // 2   "
	"10010011000",   // 3   #
	"10010001100",   // 4   $
	"10001001100",   // 5   %
	"10011001000",   // 6   &
	"10011000100",   // 7   '
	"10001100100",   // 8   (
	"11001001000",   // 9   )
	"11001000100",   // 10  *
	"11000100100",   // 11  +
	"10110011100",   // 12  ,
	"10011011100",   // 13  -
	"10011001110",   // 14  .
	"10111001100",   // 15  /
	"10011101100",   // 16  0
	"10011100110",   // 17  1
	"11001110010",   // 18  2
	"11001011100",   // 19  3
	"11001001110",   // 20  4
	"11011100100",   // 21  5
	"11001110100",   // 22  6
	"11101101110",   // 23  7
	"11101001100",   // 24  8
	"11100101100",   // 25  9
	"11100100110",   // 26  :
	"11101100100",   // 27  ;
	"11100110100",   // 28  <
	"11100110010",   // 29  =
	"11011011000",   // 30  >
	"11011000110",   // 31  ?
	"11000110110",   // 32  @
	"10100011000",   // 33  A
	"10001011000",   // 34  B
	"10001000110",   // 35  C
	"10110001000",   // 36  D
	"10001101000",   // 37  E
	"10001100010",   // 38  F
	"11010001000",   // 39  G
	"11000101000",   // 40  H
	"11000100010",   // 41  I
	"10110111000",   // 42  J
	"10110001110",   // 43  K
	"10001101110",   // 44  L
	"10111011000",   // 45  M
	"10111000110",   // 46  N
	"10001110110",   // 47  O
	"11101110110",   // 48  P
	"11010001110",   // 49  Q
	"11000101110",   // 50  R
	"11011101000",   // 51  S
	"11011100010",   // 52  T
	"11011101110",   // 53  U
	"11101011000",   // 54  V
	"11101000110",   // 55  W
	"11100010110",   // 56  X
	"11101101000",   // 57  Y
	"11101100010",   // 58  Z
	"11100011010",   // 59  [
	"11101111010",   // 60  \
	"11001000010",   // 61  ]
	"11110001010",   // 62  ^
	"10100110000",   // 63  _
	"10100001100",   // 64  `
	"10010110000",   // 65  a
	"10010000110",   // 66  b
	"10000101100",   // 67  c
	"10000100110",   // 68  d
	"10110010000",   // 69  e
	"10110000100",   // 70  f
	"10011010000",   // 71  g
	"10011000010",   // 72  h
	"10000110100",   // 73  i
	"10000110010",   // 74  j
	"11000010010",   // 75  k
	"11001010000",   // 76  l
	"11110111010",   // 77  m
	"11000010100",   // 78  n
	"10001111010",   // 79  o
	"10100111100",   // 80  p
	"10010111100",   // 81  q
	"10010011110",   // 82  r
	"10111100100",   // 83  s
	"10011110100",   // 84  t
	"10011110010",   // 85  u
	"11110100100",   // 86  v
	"11110010100",   // 87  w
	"11110010010",   // 88  x
	"11011011110",   // 89  y
	"11011110110",   // 90  z
	"11110110110",   // 91  {
	"10101111000",   // 92  |
	"10100011110",   // 93  }
	"10001011110",   // 94  ~
	"10111101000",   // 95  DEL
	"10111100010",   // 96  FNC3
	"11110101000",   // 97  FNC2
	"11110100010",   // 98  Shift A
	"10111011110",   // 99  Code C
	"10111101110",   // 100 FNC4
	"11101011110",   // 101 Code A
	"11110101110",   // 102 FNC1
	"11010000100",   // 103 Start A
	"11010010000",   // 104 Start B
	"11010011100",   // 105 Start C
	"1100011101011", // 106 Stop (incl. termination bar)
}

// Code128B encodes s as a Code 128 subset B symbol and returns its module
// pattern (true = bar): start code B, one symbol per character, the
// modulo-103 check symbol and the stop pattern, whose last two modules are
// the termination bar. The check value is (104 + Σ value_i × i) mod 103,
// where value_i = char_i − 32 and positions i count from 1. The result is
// 11×(len(s)+2)+13 modules long and carries no quiet zone.
//
// It returns an error if s is empty, longer than 80 characters, or contains
// a byte outside printable ASCII (0x20–0x7E).
func Code128B(s string) ([]bool, error) {
	if s == "" {
		return nil, errors.New("barcode: Code128B: empty input")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return nil, fmt.Errorf("barcode: Code128B: byte 0x%02X at index %d is not printable ASCII", s[i], i)
		}
	}
	if len(s) > maxCode128Len {
		return nil, fmt.Errorf("barcode: Code128B: %d characters exceeds the maximum of %d", len(s), maxCode128Len)
	}

	out := make([]bool, 0, 11*(len(s)+2)+13)
	out = appendPattern(out, code128Patterns[code128StartB])
	sum := code128StartB
	for i := 0; i < len(s); i++ {
		v := int(s[i]) - 32
		sum += v * (i + 1)
		out = appendPattern(out, code128Patterns[v])
	}
	out = appendPattern(out, code128Patterns[sum%103])
	out = appendPattern(out, code128Patterns[code128Stop])
	return out, nil
}

// EAN-13 digit encodings, 7 modules each. R is the bitwise complement of L;
// G is R read backwards.
var (
	ean13L = [10]string{"0001101", "0011001", "0010011", "0111101", "0100011", "0110001", "0101111", "0111011", "0110111", "0001011"}
	ean13G = [10]string{"0100111", "0110011", "0011011", "0100001", "0011101", "0111001", "0000101", "0010001", "0001001", "0010111"}
	ean13R = [10]string{"1110010", "1100110", "1101100", "1000010", "1011100", "1001110", "1010000", "1000100", "1001000", "1110100"}
)

// ean13Parity gives, for each first digit, the L/G encoding of the six
// digits in the left half. The first digit itself is not drawn; it is
// implied by this pattern.
var ean13Parity = [10]string{
	"LLLLLL", // 0
	"LLGLGG", // 1
	"LLGGLG", // 2
	"LLGGGL", // 3
	"LGLLGG", // 4
	"LGGLLG", // 5
	"LGGGLL", // 6
	"LGLGLG", // 7
	"LGLGGL", // 8
	"LGGLGL", // 9
}

// EAN-13 guard patterns.
const (
	ean13LeftGuard   = "101"
	ean13CentreGuard = "01010"
	ean13RightGuard  = "101"
)

// EAN13Modules returns the 95-module pattern (true = bar) of a valid
// 13-digit EAN-13: left guard, digits 2–7 in L/G encoding selected by the
// first digit's parity pattern, centre guard, digits 8–13 in R encoding,
// right guard. It returns an error if ean is not a valid EAN-13.
func EAN13Modules(ean string) ([]bool, error) {
	if !ValidEAN13(ean) {
		return nil, fmt.Errorf("barcode: EAN13Modules: %q is not a valid EAN-13", ean)
	}
	parity := ean13Parity[ean[0]-'0']
	out := make([]bool, 0, 95)
	out = appendPattern(out, ean13LeftGuard)
	for i := 1; i <= 6; i++ {
		d := ean[i] - '0'
		if parity[i-1] == 'G' {
			out = appendPattern(out, ean13G[d])
		} else {
			out = appendPattern(out, ean13L[d])
		}
	}
	out = appendPattern(out, ean13CentreGuard)
	for i := 7; i <= 12; i++ {
		out = appendPattern(out, ean13R[ean[i]-'0'])
	}
	out = appendPattern(out, ean13RightGuard)
	return out, nil
}

// appendPattern appends one module per character of pattern: '1' is a bar,
// anything else a space.
func appendPattern(dst []bool, pattern string) []bool {
	for i := 0; i < len(pattern); i++ {
		dst = append(dst, pattern[i] == '1')
	}
	return dst
}
