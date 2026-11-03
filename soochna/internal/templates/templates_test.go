package templates

import (
	"regexp"
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
	if err != nil || !strings.HasPrefix(body, "123456 is your Maapan login OTP") {
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

// Every variable a body uses must be a positional param (so provider templates
// receive it) or one of the documented optional extras.
func TestBodiesOnlyUseDeclaredParams(t *testing.T) {
	optional := map[string]bool{"link": true}
	ref := regexp.MustCompile(`\{\{-?\s*(?:if\s+)?\.(\w+)`)
	for name, d := range defs {
		declared := map[string]bool{}
		for _, p := range d.params {
			declared[p] = true
		}
		for ch, body := range map[string]string{"sms": d.sms, "whatsapp": d.whatsapp, "subject": d.subject, "email": d.email} {
			for _, m := range ref.FindAllStringSubmatch(body, -1) {
				if !declared[m[1]] && !optional[m[1]] {
					t.Errorf("%s/%s uses {{.%s}} which is not in ParamOrder %v", name, ch, m[1], d.params)
				}
			}
		}
		if d.category == "" {
			t.Errorf("%s has no category", name)
		}
	}
}

func TestEmailVariants(t *testing.T) {
	p := map[string]string{"party_name": "Ravi", "business": "Sharma Store", "number": "INV/1", "amount": "₹10.00", "date": "1 Nov 2026"}
	subj, err := Subject(Invoice, p)
	if err != nil || subj != "Bill INV/1 from Sharma Store" {
		t.Fatalf("subject %q %v", subj, err)
	}
	if body, err := Render(Invoice, Email, p); err != nil || !strings.Contains(body, "Bill: INV/1") {
		t.Fatalf("body %q %v", body, err)
	}
	if HasChannel(StockLow, Email) || !HasChannel(PaymentReminder, Email) {
		t.Fatal("email availability wrong")
	}
	if _, err := Render(OTP, Email, map[string]string{"otp": "1", "minutes": "5"}); err == nil {
		t.Fatal("otp has no email variant")
	}
}

func TestBatchExpiringPassesQty(t *testing.T) {
	order := ParamOrder(BatchExpiring)
	if order[len(order)-1] != "qty" {
		t.Fatalf("qty must be the last positional param: %v", order)
	}
	body, err := Render(BatchExpiring, WhatsApp, map[string]string{"item": "Milk", "batch": "B1", "expiry": "5 Nov 2026", "days": "2", "business": "S", "qty": "12"})
	if err != nil || !strings.Contains(body, "(12 units)") {
		t.Fatalf("%q %v", body, err)
	}
}
