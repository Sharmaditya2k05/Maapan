package messaging

import (
	"strings"
	"testing"

	"github.com/taraju/mool/events"
	"github.com/taraju/soochna/internal/clients"
	"github.com/taraju/soochna/internal/templates"
)

func TestValidateNormalisesPhone(t *testing.T) {
	in := SendInput{Channel: "sms", To: "098765 43210", Template: "otp"}
	if err := in.Validate(); err != nil || in.To != "+919876543210" {
		t.Fatalf("%v %s", err, in.To)
	}
	bad := SendInput{Channel: "fax", To: "9876543210", Template: "otp"}
	if bad.Validate() == nil {
		t.Fatal("bad channel accepted")
	}
	bad = SendInput{Channel: "sms", To: "9876543210", Template: "nope"}
	if bad.Validate() == nil {
		t.Fatal("unknown template accepted")
	}
}

func TestRedactOTP(t *testing.T) {
	body, _ := templates.Render("otp", "sms", map[string]string{"otp": "482913", "minutes": "5"})
	p, b := redact("otp", map[string]string{"otp": "482913", "minutes": "5"}, body)
	if p["otp"] != "******" || strings.Contains(b, "482913") {
		t.Fatalf("not redacted: %v %q", p, b)
	}
	p2, b2 := redact("invoice", map[string]string{"otp": "1"}, "x")
	if p2["otp"] != "1" || b2 != "x" {
		t.Fatal("non-otp must be untouched")
	}
}

func TestInvoiceEventBuildsWhatsApp(t *testing.T) {
	ten := clients.Tenant{BusinessName: "Sharma Store", OwnerPhone: "+919000000000"}
	ev, _ := events.New(events.InvoiceCreated, "aadesh", "t1", "s1", events.InvoicePayload{
		DocType: "sale", Number: "INV/26-27/00001", InvoiceDate: "2026-10-17", PartyName: "Ravi",
		PartyPhone: "9876543210", GrandTotalPaise: 118000,
	})
	in, ok, err := invoiceMessage(ev, ten)
	if err != nil || !ok || in.Channel != "whatsapp" || in.Params["amount"] != "₹1,180.00" || in.Params["date"] != "17 Oct 2026" {
		t.Fatalf("%+v %v %v", in, ok, err)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := templates.Render(in.Template, in.Channel, in.Params); err != nil {
		t.Fatal(err)
	}

	purchase, _ := events.New(events.InvoiceCreated, "aadesh", "t1", "s1", events.InvoicePayload{DocType: "purchase", PartyPhone: "9876543210"})
	if _, ok, _ := invoiceMessage(purchase, ten); ok {
		t.Fatal("purchase bills must not message the supplier")
	}
}

func TestOwnerAlerts(t *testing.T) {
	ten := clients.Tenant{BusinessName: "B", OwnerPhone: "+919000000000"}
	ev, _ := events.New(events.StockLow, "bhandar", "t1", "s1", events.StockLowPayload{ItemName: "Sugar", OnHand: 2.5, ReorderLevel: 10})
	in, ok, err := stockLowMessage(ev, ten)
	if err != nil || !ok || in.To != ten.OwnerPhone || in.Params["on_hand"] != "2.5" {
		t.Fatalf("%+v", in)
	}
	if _, err := templates.Render(in.Template, in.Channel, in.Params); err != nil {
		t.Fatal(err)
	}
	ev2, _ := events.New(events.ImportCompleted, "aayaat", "t1", "", events.ImportCompletedPayload{Kind: "items", Total: 10, Imported: 9, Failed: 1})
	in2, ok, _ := importMessage(ev2, clients.Tenant{})
	if ok {
		t.Fatalf("no phone → no message, got %+v", in2)
	}
}

func TestParseWebhooks(t *testing.T) {
	ups, err := ParseGupshup([]byte(`{"type":"message-event","payload":{"id":"m1","gsId":"g1","type":"delivered"}}`))
	if err != nil || len(ups) != 2 || ups[0].Status != "delivered" {
		t.Fatalf("%v %v", ups, err)
	}
	ups, _ = ParseGupshup([]byte(`{"type":"message","payload":{}}`))
	if len(ups) != 0 {
		t.Fatal("inbound must be ignored")
	}
	ups, err = ParseMSG91([]byte(`[{"requestId":"r1","status":"1"},{"requestId":"r2","status":16,"desc":"DLT"}]`))
	if err != nil || len(ups) != 2 || ups[0].Status != "delivered" || ups[1].Status != "failed" {
		t.Fatalf("%v %v", ups, err)
	}
	ups, err = ParseMSG91([]byte(`{"requestId":"r3","status":"delivered"}`))
	if err != nil || len(ups) != 1 {
		t.Fatalf("%v %v", ups, err)
	}
}
