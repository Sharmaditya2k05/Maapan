// Package otp holds the pure OTP and phone helpers (no I/O).
package otp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Length is the number of OTP digits.
const Length = 6

// NormalizePhone turns "98765 43210", "+91-9876543210", "09876543210" into "+919876543210".
// Only Indian mobile numbers (starting 6-9) are accepted.
func NormalizePhone(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	}
	if len(d) != 10 || d[0] < '6' {
		return "", errors.New("enter a valid 10-digit Indian mobile number")
	}
	return "+91" + d, nil
}

// Generate returns a uniformly random numeric code.
func Generate() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Hash binds the code to the phone with a server secret so a Redis dump does
// not reveal OTPs.
func Hash(secret []byte, phone, code string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(phone + "|" + code))
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compares two hashes in constant time.
func Equal(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// ValidCode reports whether s looks like an OTP.
func ValidCode(s string) bool {
	if len(s) != Length {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Mask hides the middle of a phone for logs/responses: +91******3210.
func Mask(phone string) string {
	if len(phone) < 7 {
		return "****"
	}
	return phone[:3] + strings.Repeat("*", len(phone)-7) + phone[len(phone)-4:]
}
