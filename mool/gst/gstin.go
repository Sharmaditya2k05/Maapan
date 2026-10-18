// Package gst holds the pure GST rules: GSTIN validation, state codes, the
// CGST/SGST vs IGST tax engine and amount-in-words. No I/O — safe to call on
// the POS hot path.
package gst

import (
	"errors"
	"regexp"
	"strings"
)

const gstinChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z0-9]{10}[0-9A-Z]Z[0-9A-Z]$`)

// Normalize upper-cases and trims a GSTIN.
func Normalize(gstin string) string { return strings.ToUpper(strings.TrimSpace(gstin)) }

// ValidateGSTIN checks format, state code and the mod-36 check digit.
func ValidateGSTIN(gstin string) error {
	g := Normalize(gstin)
	if len(g) != 15 || !gstinPattern.MatchString(g) {
		return errors.New("GSTIN must be 15 characters: 2-digit state, 10-char PAN, entity, Z, checksum")
	}
	if _, ok := States[g[:2]]; !ok {
		return errors.New("GSTIN has an unknown state code " + g[:2])
	}
	if checksum(g[:14]) != g[14] {
		return errors.New("GSTIN checksum does not match")
	}
	return nil
}

// checksum computes the 15th character of a GSTIN from the first 14.
func checksum(first14 string) byte {
	sum := 0
	for i := 0; i < 14; i++ {
		v := strings.IndexByte(gstinChars, first14[i])
		factor := 1
		if i%2 == 1 {
			factor = 2
		}
		p := v * factor
		sum += p/36 + p%36
	}
	return gstinChars[(36-sum%36)%36]
}

// StateCode returns the 2-digit state code of a GSTIN ("" if too short).
func StateCode(gstin string) string {
	g := Normalize(gstin)
	if len(g) < 2 {
		return ""
	}
	return g[:2]
}

// PAN returns the PAN embedded in a GSTIN.
func PAN(gstin string) string {
	g := Normalize(gstin)
	if len(g) < 12 {
		return ""
	}
	return g[2:12]
}

// States maps GST state codes to names.
var States = map[string]string{
	"01": "Jammu and Kashmir", "02": "Himachal Pradesh", "03": "Punjab", "04": "Chandigarh",
	"05": "Uttarakhand", "06": "Haryana", "07": "Delhi", "08": "Rajasthan", "09": "Uttar Pradesh",
	"10": "Bihar", "11": "Sikkim", "12": "Arunachal Pradesh", "13": "Nagaland", "14": "Manipur",
	"15": "Mizoram", "16": "Tripura", "17": "Meghalaya", "18": "Assam", "19": "West Bengal",
	"20": "Jharkhand", "21": "Odisha", "22": "Chhattisgarh", "23": "Madhya Pradesh", "24": "Gujarat",
	"26": "Dadra and Nagar Haveli and Daman and Diu", "27": "Maharashtra", "29": "Karnataka",
	"30": "Goa", "31": "Lakshadweep", "32": "Kerala", "33": "Tamil Nadu", "34": "Puducherry",
	"35": "Andaman and Nicobar Islands", "36": "Telangana", "37": "Andhra Pradesh", "38": "Ladakh",
	"97": "Other Territory", "96": "Other Country",
}

// ValidState reports whether code is a known GST state code.
func ValidState(code string) bool { _, ok := States[code]; return ok }
