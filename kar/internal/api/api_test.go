package api

import "testing"

func TestPlaceFromAddress(t *testing.T) {
	cases := map[string]string{
		"12 MG Road, Shivajinagar, Pune, Maharashtra, 411005": "Pune",
		"4 Church St, Bengaluru 560001":                       "Bengaluru",
		"":                                                    "",
	}
	for in, want := range cases {
		if got := placeFromAddress(in); got != want {
			t.Errorf("placeFromAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSettingsUpdateValidate(t *testing.T) {
	bad := "99"
	if err := (&SettingsUpdate{StateCode: &bad}).Validate(); err == nil {
		t.Fatal("unknown state must fail")
	}
	g := "27AAPFU0939F1ZV"
	if err := (&SettingsUpdate{GSTIN: &g}).Validate(); err != nil {
		t.Fatal(err)
	}
}
