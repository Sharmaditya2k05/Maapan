package otp

import "testing"

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"9876543210":       "+919876543210",
		"+91 98765 43210":  "+919876543210",
		"09876543210":      "+919876543210",
		"91-98765-43210":   "+919876543210",
		"(+91) 6000000001": "+916000000001",
	}
	for in, want := range ok {
		got, err := NormalizePhone(in)
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "5876543210", "+1 415 555 0100"} {
		if _, err := NormalizePhone(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestGenerateAndHash(t *testing.T) {
	c, err := Generate()
	if err != nil || !ValidCode(c) {
		t.Fatalf("bad code %q %v", c, err)
	}
	s := []byte("secret")
	if !Equal(Hash(s, "+919876543210", c), Hash(s, "+919876543210", c)) {
		t.Fatal("hash not deterministic")
	}
	if Equal(Hash(s, "+919876543210", c), Hash(s, "+919876543211", c)) {
		t.Fatal("hash must bind phone")
	}
	if ValidCode("12a456") || ValidCode("12345") {
		t.Fatal("ValidCode")
	}
	if Mask("+919876543210") != "+91******3210" {
		t.Fatalf("mask %s", Mask("+919876543210"))
	}
}

func TestDeriveKey(t *testing.T) {
	root := []byte("local-dev-jwt-secret-change-me-32chars")
	k := DeriveKey(root)
	if len(k) != 32 || string(k) == string(root) || string(k) != string(DeriveKey(root)) {
		t.Fatalf("derived key must be 32 bytes, stable and differ from the root")
	}
	if string(DeriveKey([]byte("other"))) == string(k) {
		t.Fatal("different roots give different keys")
	}
	if Hash(k, "+919876543210", "123456") == Hash(root, "+919876543210", "123456") {
		t.Fatal("OTP hash must not be keyed by the root secret")
	}
}
