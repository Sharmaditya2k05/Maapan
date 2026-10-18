package gst

import "strings"

var ones = []string{"", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten",
	"Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen", "Sixteen", "Seventeen", "Eighteen", "Nineteen"}
var tens = []string{"", "", "Twenty", "Thirty", "Forty", "Fifty", "Sixty", "Seventy", "Eighty", "Ninety"}

func twoDigits(n int64) string {
	if n < 20 {
		return ones[n]
	}
	if n%10 == 0 {
		return tens[n/10]
	}
	return tens[n/10] + " " + ones[n%10]
}

func threeDigits(n int64) string {
	var parts []string
	if n >= 100 {
		parts = append(parts, ones[n/100]+" Hundred")
		n %= 100
	}
	if n > 0 {
		parts = append(parts, twoDigits(n))
	}
	return strings.Join(parts, " ")
}

// indian spells n using crore/lakh/thousand grouping.
func indian(n int64) string {
	if n == 0 {
		return "Zero"
	}
	var parts []string
	if n >= 10000000 {
		parts = append(parts, indian(n/10000000)+" Crore")
		n %= 10000000
	}
	if n >= 100000 {
		parts = append(parts, twoDigits(n/100000)+" Lakh")
		n %= 100000
	}
	if n >= 1000 {
		parts = append(parts, twoDigits(n/1000)+" Thousand")
		n %= 1000
	}
	if n > 0 {
		parts = append(parts, threeDigits(n))
	}
	return strings.Join(parts, " ")
}

// AmountInWords renders paise as printed on Indian tax invoices,
// e.g. 123450 -> "Rupees One Thousand Two Hundred Thirty Four and Fifty Paise Only".
func AmountInWords(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	r, p := paise/100, paise%100
	s := "Rupees " + indian(r)
	if p > 0 {
		s += " and " + twoDigits(p) + " Paise"
	}
	s += " Only"
	if neg {
		s = "Minus " + s
	}
	return s
}
