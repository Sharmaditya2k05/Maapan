package creds

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := DeriveKey("local-dev-service-secret")
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.Seal("rzp_live_secret_value", "t1|razorpay|key_secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("rzp_live")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := c.Open(blob, "t1|razorpay|key_secret")
	if err != nil || got != "rzp_live_secret_value" {
		t.Fatalf("open: %q %v", got, err)
	}
	// Two seals of the same value differ (random nonce).
	blob2, _ := c.Seal("rzp_live_secret_value", "t1|razorpay|key_secret")
	if bytes.Equal(blob, blob2) {
		t.Fatal("nonce must be random")
	}
	// A blob cannot be moved to another row (different AAD) or opened with another key.
	if _, err := c.Open(blob, "t2|razorpay|key_secret"); err == nil {
		t.Fatal("different aad must fail")
	}
	other, _ := NewCipher(DeriveKey("other"))
	if _, err := other.Open(blob, "t1|razorpay|key_secret"); err == nil {
		t.Fatal("different key must fail")
	}
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 1
	if _, err := c.Open(tampered, "t1|razorpay|key_secret"); err == nil {
		t.Fatal("tampered blob must fail")
	}
	if _, err := c.Open(blob[:5], "x"); err == nil {
		t.Fatal("short blob must fail")
	}
	if _, err := NewCipher([]byte("short")); err == nil {
		t.Fatal("wrong key size must fail")
	}
}

func TestParseKeyAndLoad(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, KeySize)
	for _, enc := range []string{hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw), " " + hex.EncodeToString(raw) + "\n"} {
		k, err := ParseKey(enc)
		if err != nil || !bytes.Equal(k, raw) {
			t.Fatalf("ParseKey(%q): %v", enc, err)
		}
	}
	for _, bad := range []string{"", "abc", hex.EncodeToString(raw[:16]), base64.StdEncoding.EncodeToString(raw[:31])} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("ParseKey(%q) must fail", bad)
		}
	}
	log := slog.Default()
	if _, err := Load("", "svc", true, log); err == nil {
		t.Fatal("prod without CREDENTIALS_KEY must refuse to start")
	}
	if _, err := Load("", "", false, log); err == nil {
		t.Fatal("no key and no service secret must fail")
	}
	dev, err := Load("", "svc", false, log)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := dev.Seal("v", "a")
	again, _ := Load("", "svc", false, log)
	if got, err := again.Open(blob, "a"); err != nil || got != "v" {
		t.Fatal("derived key must be deterministic")
	}
	if _, err := Load("not-a-key", "svc", false, log); err == nil {
		t.Fatal("garbage key must fail")
	}
	if _, err := Load(hex.EncodeToString(raw), "", true, log); err != nil {
		t.Fatal(err)
	}
}

func TestMask(t *testing.T) {
	if Mask("rzp_live_AbCdEf1234") != "****1234" || Mask("abcd") != "****" || Mask("") != "" {
		t.Fatalf("mask: %q %q %q", Mask("rzp_live_AbCdEf1234"), Mask("abcd"), Mask(""))
	}
	if strings.Contains(Mask("rzp_live_SECRETPART9999"), "SECRET") {
		t.Fatal("mask leaks")
	}
}
