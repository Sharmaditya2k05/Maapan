package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maapan/mool/events"
	"github.com/maapan/soochna/internal/clients"
	"github.com/maapan/soochna/internal/templates"
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

func TestValidateEmailAndMedia(t *testing.T) {
	in := SendInput{Channel: "email", To: "Ravi <Ravi@Example.COM>", Template: "invoice", MediaURL: "https://x.example/a.pdf"}
	if err := in.Validate(); err != nil || in.To != "ravi@example.com" {
		t.Fatalf("%v %s", err, in.To)
	}
	for _, bad := range []SendInput{
		{Channel: "email", To: "not-an-address", Template: "invoice"},
		{Channel: "email", To: "a@b.c", Template: "otp"},                                   // no e-mail variant
		{Channel: "sms", To: "9876543210", Template: "invoice", MediaURL: "https://x/a"},   // sms has no media
		{Channel: "whatsapp", To: "9876543210", Template: "invoice", MediaURL: "http://x"}, // not https
		{Channel: "whatsapp", To: "9876543210", Template: "invoice", MediaURL: "https://x/a", MediaFilename: "../a.pdf"},
	} {
		b := bad
		if b.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
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

type stubBahi struct {
	party   *clients.Party
	overdue []clients.OverdueParty
	err     error
}

func (b *stubBahi) Party(context.Context, string, string) (*clients.Party, error) {
	return b.party, b.err
}
func (b *stubBahi) Overdue(context.Context, string, string) ([]clients.OverdueParty, error) {
	return b.overdue, b.err
}

func TestInvoiceEventBuildsWhatsAppAndEmail(t *testing.T) {
	s := &Service{Bahi: &stubBahi{party: &clients.Party{Email: "ravi@example.com"}},
		InvoicePDFURL: "https://files.example/{tenant_id}/{invoice_id}.pdf"}
	ten := clients.Tenant{BusinessName: "Sharma Store", OwnerPhone: "+919000000000"}
	ev, _ := events.New(events.InvoiceCreated, "aadesh", "t1", "s1", events.InvoicePayload{
		InvoiceID: "inv1", DocType: "sale", Number: "INV/26-27/00001", InvoiceDate: "2026-10-17", PartyName: "Ravi",
		PartyID: "p1", PartyPhone: "9876543210", GrandTotalPaise: 118000,
	})
	ins, err := s.invoiceMessages(context.Background(), ev, ten)
	if err != nil || len(ins) != 2 {
		t.Fatalf("%+v %v", ins, err)
	}
	wa, em := ins[0], ins[1]
	if wa.Channel != "whatsapp" || wa.Params["amount"] != "₹1,180.00" || wa.Params["date"] != "17 Oct 2026" {
		t.Fatalf("%+v", wa)
	}
	if wa.MediaURL != "https://files.example/t1/inv1.pdf" || wa.MediaFilename != "INV-26-27-00001.pdf" {
		t.Fatalf("document: %+v", wa)
	}
	if em.Channel != "email" || em.To != "ravi@example.com" || em.MediaURL != wa.MediaURL {
		t.Fatalf("%+v", em)
	}
	for _, in := range ins {
		if err := in.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := templates.Render(in.Template, in.Channel, in.Params); err != nil {
			t.Fatal(err)
		}
	}

	purchase, _ := events.New(events.InvoiceCreated, "aadesh", "t1", "s1", events.InvoicePayload{DocType: "purchase", PartyPhone: "9876543210"})
	if ins, _ := s.invoiceMessages(context.Background(), purchase, ten); len(ins) != 0 {
		t.Fatal("purchase bills must not message the supplier")
	}
	// bahi down → retryable error (Pub/Sub redelivers; idempotency keys prevent duplicates).
	s.Bahi = &stubBahi{err: context.DeadlineExceeded}
	if _, err := s.invoiceMessages(context.Background(), ev, ten); err == nil || !isRetryable(err) {
		t.Fatalf("want retryable error, got %v", err)
	}
	// No bahi wired, no PDF pattern → WhatsApp text only.
	s2 := &Service{}
	ins, _ = s2.invoiceMessages(context.Background(), ev, ten)
	if len(ins) != 1 || ins[0].MediaURL != "" {
		t.Fatalf("%+v", ins)
	}
}

func TestOwnerAlerts(t *testing.T) {
	ten := clients.Tenant{BusinessName: "B", OwnerPhone: "+919000000000"}
	ev, _ := events.New(events.StockLow, "bhandar", "t1", "s1", events.StockLowPayload{ItemName: "Sugar", OnHand: 2.5, ReorderLevel: 10})
	ins, err := stockLowMessage(context.Background(), ev, ten)
	if err != nil || len(ins) != 1 || ins[0].To != ten.OwnerPhone || ins[0].Params["on_hand"] != "2.5" {
		t.Fatalf("%+v", ins)
	}
	if _, err := templates.Render(ins[0].Template, ins[0].Channel, ins[0].Params); err != nil {
		t.Fatal(err)
	}
	ev2, _ := events.New(events.ImportCompleted, "aayaat", "t1", "", events.ImportCompletedPayload{Kind: "items", Total: 10, Imported: 9, Failed: 1})
	if ins, _ := importMessage(context.Background(), ev2, clients.Tenant{}); len(ins) != 0 {
		t.Fatalf("no phone → no message, got %+v", ins)
	}
	ev3, _ := events.New(events.BatchExpiring, "bhandar", "t1", "s1", events.BatchExpiringPayload{ItemName: "Milk", BatchNo: "B7", ExpiryDate: "2026-11-05", Qty: 12, DaysLeft: 2})
	ins, _ = batchExpiringMessage(context.Background(), ev3, ten)
	body, err := templates.Render(ins[0].Template, ins[0].Channel, ins[0].Params)
	if err != nil || !strings.Contains(body, "(12 units)") {
		t.Fatalf("%q %v", body, err)
	}
	if pos := templates.Positional(ins[0].Template, ins[0].Params); pos[len(pos)-1] != "12" {
		t.Fatalf("qty must reach provider templates: %v", pos)
	}
}

func TestParseWebhooks(t *testing.T) {
	ups, in, err := ParseGupshup([]byte(`{"type":"message-event","payload":{"id":"m1","gsId":"g1","type":"delivered"}}`))
	if err != nil || len(ups) != 2 || ups[0].Status != "delivered" || in != nil {
		t.Fatalf("%v %v", ups, err)
	}
	ups, in, _ = ParseGupshup([]byte(`{"type":"message","payload":{"id":"x","source":"919876543210","type":"text","payload":{"text":"STOP"}}}`))
	if len(ups) != 0 || in == nil || in.From != "919876543210" || in.Text != "STOP" {
		t.Fatalf("inbound: %v %+v", ups, in)
	}
	ups, err = parseMSG91(`[{"requestId":"r1","status":"1"},{"requestId":"r2","status":16,"desc":"DLT"}]`, "application/json")
	if err != nil || len(ups) != 2 || ups[0].Status != "delivered" || ups[1].Status != "failed" {
		t.Fatalf("%v %v", ups, err)
	}
	ups, err = parseMSG91(`{"requestId":"r3","status":"delivered"}`, "application/json")
	if err != nil || len(ups) != 1 {
		t.Fatalf("%v %v", ups, err)
	}
	_, in, _ = ParseMSG91([]byte(`sender=919876543210&message=stop`), "application/x-www-form-urlencoded")
	if in == nil || in.Text != "stop" {
		t.Fatalf("form inbound %+v", in)
	}
	_, in, _ = ParseMSG91([]byte(`{"mobile":"919876543210","message":"START"}`), "application/json")
	if in == nil || in.Text != "START" {
		t.Fatalf("json inbound %+v", in)
	}
}

func parseMSG91(raw, ct string) ([]DeliveryUpdate, error) {
	ups, _, err := ParseMSG91([]byte(raw), ct)
	return ups, err
}

func TestKeyword(t *testing.T) {
	for text, want := range map[string]string{"stop": ConsentOptedOut, " Stop. ": ConsentOptedOut, "STOP ALL": ConsentOptedOut,
		"unsubscribe": ConsentOptedOut, "बंद": ConsentOptedOut, "START": ConsentOptedIn, "hi": "", "stop sending me bills please": ""} {
		if got, _ := Keyword(text); got != want {
			t.Errorf("%q: got %q want %q", text, got, want)
		}
	}
	if _, all := Keyword("stop all"); !all {
		t.Error("STOP ALL must cover every channel")
	}
}

func TestQuietHours(t *testing.T) {
	q, err := ParseQuietHours("21:00-09:00", IST)
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 3, h, m, 0, 0, IST) }
	if _, quiet := q.NextAllowed(at(14, 0)); quiet {
		t.Fatal("14:00 is not quiet")
	}
	if next, quiet := q.NextAllowed(at(22, 30)); !quiet || !next.Equal(time.Date(2026, 11, 4, 9, 0, 0, 0, IST)) {
		t.Fatalf("22:30 → %v %v", next, quiet)
	}
	if next, quiet := q.NextAllowed(at(6, 0)); !quiet || !next.Equal(at(9, 0)) {
		t.Fatalf("06:00 → %v %v", next, quiet)
	}
	if _, quiet := q.NextAllowed(at(9, 0)); quiet {
		t.Fatal("09:00 is allowed")
	}
	if off, _ := ParseQuietHours("off", nil); off.Enabled() {
		t.Fatal("off must disable")
	}
	if _, err := ParseQuietHours("9pm", nil); err == nil {
		t.Fatal("bad spec accepted")
	}
}

func TestBackoffAndMask(t *testing.T) {
	if Backoff(1) != time.Minute || Backoff(4) != time.Hour || Backoff(9) != time.Hour {
		t.Fatal("backoff")
	}
	if maskAddress("+919876543210") != "*********3210" || maskAddress("ravi@example.com") != "r***@example.com" {
		t.Fatal(maskAddress("+919876543210"), maskAddress("ravi@example.com"))
	}
}

func TestReminderInputs(t *testing.T) {
	r := clients.OverdueParty{PartyID: "p1", PartyName: "Ravi", Phone: "9876543210", Email: "", OverduePaise: 250000,
		OldestDueDate: "2026-10-27", DaysOverdue: 7}
	ins := reminderInputs(r, "Sharma Store", "2026-11-03", []string{"whatsapp", "email", "sms"})
	if len(ins) != 2 || ins[0].Channel != "whatsapp" || ins[1].Channel != "sms" {
		t.Fatalf("%+v", ins)
	}
	if ins[0].IdempotencyKey != "reminder:p1:2026-11-03:whatsapp" || ins[0].Params["amount"] != "₹2,500.00" || ins[0].Params["due_date"] != "27 Oct 2026" {
		t.Fatalf("%+v", ins[0])
	}
	if _, err := templates.Render(ins[0].Template, ins[0].Channel, ins[0].Params); err != nil {
		t.Fatal(err)
	}
}

func TestSenderSettingsValidate(t *testing.T) {
	ok := SenderSettings{SMSSenderID: "SHRMST", WhatsAppSource: "9111111111", EmailFrom: "Bills@Sharma.example",
		ReminderDays: []int{7, 0, 7}, Templates: []TenantTemplate{{Template: "invoice", Channel: "sms", ProviderTemplateID: "x"}}}
	if err := ok.Validate(); err != nil || ok.WhatsAppSource != "+919111111111" || ok.EmailFrom != "bills@sharma.example" || len(ok.ReminderDays) != 2 {
		t.Fatalf("%v %+v", err, ok)
	}
	for _, bad := range []SenderSettings{{SMSSenderID: "shr"}, {ReminderDays: []int{-1}}, {ReminderChannels: []string{"fax"}},
		{Templates: []TenantTemplate{{Template: "invoice", Channel: "email", ProviderTemplateID: "x"}}}} {
		b := bad
		if b.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
