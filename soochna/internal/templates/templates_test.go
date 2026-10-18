package templates

import (
	"strings"
	"testing"
)

func TestRenderAllTemplates(t *testing.T) {
	params := map[string]string{}
	for _, n := range Names() {
		for _, p := range ParamOrder(n) {
			params[p] = "X" + p
		}
	}
	for _, n := range Names() {
		for _, ch := range []string{"sms", "whatsapp"} {
			body, err := Render(n, ch, params)
			if err != nil {
				t.Fatalf("%s/%s: %v", n, ch, err)
			}
			if strings.Contains(body, "<no value>") || body == "" {
				t.Fatalf("%s/%s rendered badly: %q", n, ch, body)
			}
		}
	}
}

func TestRenderOTPAndMissingParam(t *testing.T) {
	body, err := Render(OTP, "sms", map[string]string{"otp": "123456", "minutes": "5"})
	if err != nil || !strings.HasPrefix(body, "123456 is your Taraju login OTP") {
		t.Fatalf("got %q %v", body, err)
	}
	if _, err := Render(OTP, "sms", map[string]string{"minutes": "5"}); err == nil {
		t.Fatal("missing otp must fail")
	}
	if _, err := Render("nope", "sms", nil); err == nil {
		t.Fatal("unknown template must fail")
	}
	if got := Positional(OTP, map[string]string{"otp": "1", "minutes": "5"}); len(got) != 2 || got[0] != "1" {
		t.Fatalf("positional %v", got)
	}
}

func TestOptionalLinkOmitted(t *testing.T) {
	p := map[string]string{"party_name": "A", "business": "B", "number": "INV/1", "amount": "₹1.00", "date": "2026-10-17"}
	body, _ := Render(Invoice, "whatsapp", p)
	if strings.Contains(body, "View bill") {
		t.Fatal("link line should be hidden without link")
	}
	p["link"] = "https://x"
	body, _ = Render(Invoice, "whatsapp", p)
	if !strings.Contains(body, "View bill: https://x") {
		t.Fatal("link line missing")
	}
}
