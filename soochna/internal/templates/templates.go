// Package templates renders message bodies per template and channel.
//
// Bodies are bilingual (English + Hindi) and short enough for one SMS segment
// where possible. Providers that need pre-approved templates (MSG91 DLT,
// WhatsApp business templates) use ParamOrder to send variables positionally;
// the approved provider template must contain the same variables in that order.
//
// Every template has a Category that decides consent and timing rules:
// customer-facing transactional and promotional messages honour opt-outs,
// promotional ones also the DND list and quiet hours; OTPs and owner/staff
// alerts are never suppressed.
package templates

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Names of the supported templates (ARCHITECTURE.md §6).
const (
	OTP             = "otp"
	Invoice         = "invoice"
	PaymentReceipt  = "payment_receipt"
	PaymentReminder = "payment_reminder"
	LedgerStatement = "ledger_statement"
	StockLow        = "stock_low"
	BatchExpiring   = "batch_expiring"
	ImportDone      = "import_done"
	WeeklySummary   = "weekly_summary" // text written by buddhi
	Promotion       = "promotion"      // store offers; promotional rules apply
)

// Category of a template.
type Category string

const (
	// CategoryOTP is a login code: never suppressed, never retried.
	CategoryOTP Category = "otp"
	// CategoryAlert goes to the business owner or staff: never suppressed by customer consent.
	CategoryAlert Category = "alert"
	// CategoryTransactional is a customer message about a transaction (bill, receipt, due payment).
	CategoryTransactional Category = "transactional"
	// CategoryPromotional is marketing: opt-out, DND and quiet hours apply.
	CategoryPromotional Category = "promotional"
)

// Channels.
const (
	SMS      = "sms"
	WhatsApp = "whatsapp"
	Email    = "email"
)

type def struct {
	category Category
	params   []string // positional order for provider templates
	sms      string
	whatsapp string
	subject  string // email subject; "" = no email variant
	email    string // email plain-text body
}

var defs = map[string]def{
	OTP: {
		category: CategoryOTP,
		params:   []string{"otp", "minutes"},
		sms:      `{{.otp}} is your Maapan login OTP. Valid for {{.minutes}} min. Do not share it. / आपका Maapan OTP {{.otp}} है।`,
		whatsapp: "*{{.otp}}* is your Maapan login OTP. It is valid for {{.minutes}} minutes. Do not share it with anyone.\n\nआपका Maapan लॉगिन OTP *{{.otp}}* है। इसे किसी के साथ साझा न करें।",
	},
	Invoice: {
		category: CategoryTransactional,
		params:   []string{"party_name", "business", "number", "amount", "date"},
		sms:      `Dear {{.party_name}}, thank you for shopping at {{.business}}. Bill {{.number}} dated {{.date}}: {{.amount}}.`,
		whatsapp: "Namaste {{.party_name}} 🙏\n\nThank you for shopping at *{{.business}}*.\nBill: *{{.number}}*\nDate: {{.date}}\nAmount: *{{.amount}}*\n{{if .link}}\nView bill: {{.link}}\n{{end}}\n{{.business}} से खरीदारी के लिए धन्यवाद।",
		subject:  `Bill {{.number}} from {{.business}}`,
		email:    "Dear {{.party_name}},\n\nThank you for shopping at {{.business}}.\n\nBill: {{.number}}\nDate: {{.date}}\nAmount: {{.amount}}\n{{if .link}}\nView bill: {{.link}}\n{{end}}\nThe bill is attached when available.\n\nRegards,\n{{.business}}",
	},
	PaymentReceipt: {
		category: CategoryTransactional,
		params:   []string{"party_name", "business", "amount", "ref"},
		sms:      `Received {{.amount}} from {{.party_name}} at {{.business}}. Ref {{.ref}}. Thank you! / भुगतान प्राप्त हुआ।`,
		whatsapp: "Payment received ✅\n\n*{{.amount}}* from {{.party_name}} to *{{.business}}*.\nReference: {{.ref}}\n\nभुगतान प्राप्त हुआ, धन्यवाद।",
		subject:  `Payment of {{.amount}} received by {{.business}}`,
		email:    "Dear {{.party_name}},\n\nWe have received your payment of {{.amount}}.\nReference: {{.ref}}\n\nThank you,\n{{.business}}",
	},
	PaymentReminder: {
		category: CategoryTransactional,
		params:   []string{"party_name", "business", "amount", "due_date"},
		sms:      `Dear {{.party_name}}, {{.amount}} is due to {{.business}} since {{.due_date}}. Please pay at the earliest. / कृपया भुगतान करें।`,
		whatsapp: "Namaste {{.party_name}} 🙏\n\nA payment of *{{.amount}}* to *{{.business}}* was due on {{.due_date}}.\n{{if .link}}\nPay now: {{.link}}\n{{end}}\nकृपया जल्द से जल्द भुगतान करें। Ignore if already paid.",
		subject:  `Payment reminder: {{.amount}} due to {{.business}}`,
		email:    "Dear {{.party_name}},\n\nThis is a reminder that {{.amount}} to {{.business}} was due on {{.due_date}}.\n{{if .link}}\nPay now: {{.link}}\n{{end}}\nPlease ignore this message if you have already paid.\n\nRegards,\n{{.business}}",
	},
	LedgerStatement: {
		category: CategoryTransactional,
		params:   []string{"party_name", "business", "balance", "as_of"},
		sms:      `Dear {{.party_name}}, your balance with {{.business}} as of {{.as_of}} is {{.balance}}.`,
		whatsapp: "Namaste {{.party_name}},\n\nYour account balance with *{{.business}}* as of {{.as_of}} is *{{.balance}}*.\n{{if .link}}\nStatement: {{.link}}\n{{end}}\nबकाया राशि: {{.balance}}",
		subject:  `Account statement from {{.business}} as of {{.as_of}}`,
		email:    "Dear {{.party_name}},\n\nYour account balance with {{.business}} as of {{.as_of}} is {{.balance}}.\n{{if .link}}\nStatement: {{.link}}\n{{end}}\nThe statement is attached when available.\n\nRegards,\n{{.business}}",
	},
	StockLow: {
		category: CategoryAlert,
		params:   []string{"item", "on_hand", "reorder_level", "business"},
		sms:      `{{.business}}: low stock - {{.item}} has {{.on_hand}} left (reorder at {{.reorder_level}}).`,
		whatsapp: "⚠️ *Low stock* at {{.business}}\n\n{{.item}}: only *{{.on_hand}}* left (reorder level {{.reorder_level}}).\n\nस्टॉक कम है — दोबारा ऑर्डर करें।",
	},
	BatchExpiring: {
		category: CategoryAlert,
		// qty is last so provider templates registered with the first five
		// variables keep their positions.
		params:   []string{"item", "batch", "expiry", "days", "business", "qty"},
		sms:      `{{.business}}: {{.item}} batch {{.batch}} ({{.qty}} units) expires on {{.expiry}} ({{.days}} days).`,
		whatsapp: "⏳ *Expiry alert* at {{.business}}\n\n{{.item}} — batch {{.batch}} ({{.qty}} units) expires on *{{.expiry}}* ({{.days}} days left).\n\nमाल की समाप्ति तिथि नज़दीक है।",
	},
	WeeklySummary: {
		category: CategoryAlert,
		params:   []string{"text"},
		sms:      `Maapan weekly summary: {{.text}}`,
		whatsapp: "📊 *Your week at a glance*\n\n{{.text}}\n\n— Maapan",
		subject:  `Your week at a glance — Maapan`,
		email:    "{{.text}}\n\n— Maapan",
	},
	ImportDone: {
		category: CategoryAlert,
		params:   []string{"kind", "imported", "total", "failed"},
		sms:      `Maapan: your {{.kind}} import finished - {{.imported}}/{{.total}} imported, {{.failed}} failed.`,
		whatsapp: "📥 *Import finished*\n\n{{.kind}}: {{.imported}} of {{.total}} imported, {{.failed}} failed.\n{{if .link}}\nDetails: {{.link}}\n{{end}}\nआपका डेटा आयात पूरा हुआ।",
	},
	Promotion: {
		category: CategoryPromotional,
		params:   []string{"business", "text"},
		sms:      `{{.business}}: {{.text}} Reply STOP to opt out.`,
		whatsapp: "*{{.business}}*\n\n{{.text}}\n\nReply STOP to stop these messages. / संदेश बंद करने के लिए STOP भेजें।",
		subject:  `{{.business}}: offers for you`,
		email:    "{{.text}}\n\n— {{.business}}\n\nYou receive this because you shop at {{.business}}. Reply STOP to unsubscribe.",
	},
}

var compiled = map[string]*template.Template{}

func init() {
	for name, d := range defs {
		bodies := map[string]string{SMS: d.sms, WhatsApp: d.whatsapp}
		if d.email != "" {
			bodies[Email] = d.email
			bodies["subject"] = d.subject
		}
		for ch, body := range bodies {
			compiled[name+"/"+ch] = template.Must(template.New(name + "/" + ch).Option("missingkey=zero").Parse(body))
		}
	}
}

// Known reports whether name is a supported template.
func Known(name string) bool { _, ok := defs[name]; return ok }

// Names lists all templates (sorted).
func Names() []string {
	out := make([]string, 0, len(defs))
	for n := range defs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// CategoryOf returns the template's category ("" for unknown templates).
func CategoryOf(name string) Category { return defs[name].category }

// HasChannel reports whether template name can be sent on channel.
func HasChannel(name, channel string) bool {
	_, ok := compiled[name+"/"+channel]
	return ok
}

// ParamOrder returns the positional variable order for provider templates.
func ParamOrder(name string) []string { return defs[name].params }

// Render produces the body for name on channel ("sms" | "whatsapp" | "email").
// Missing params render as empty text; required positional params must be present.
func Render(name, channel string, params map[string]string) (string, error) {
	if channel == "subject" {
		return "", fmt.Errorf("use Subject for e-mail subjects")
	}
	return render(name, channel, params)
}

// Subject renders the e-mail subject of name.
func Subject(name string, params map[string]string) (string, error) {
	s, err := render(name, "subject", params)
	return strings.Join(strings.Fields(s), " "), err
}

func render(name, channel string, params map[string]string) (string, error) {
	t, ok := compiled[name+"/"+channel]
	if !ok {
		return "", fmt.Errorf("unknown template %q for channel %q", name, channel)
	}
	for _, p := range defs[name].params {
		if strings.TrimSpace(params[p]) == "" {
			return "", fmt.Errorf("template %s needs param %q", name, p)
		}
	}
	data := make(map[string]string, len(params))
	for k, v := range params {
		data[k] = v
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	out := strings.TrimSpace(strings.ReplaceAll(buf.String(), "<no value>", ""))
	return out, nil
}

// Positional returns params in ParamOrder for provider template APIs.
func Positional(name string, params map[string]string) []string {
	order := defs[name].params
	out := make([]string, len(order))
	for i, k := range order {
		out[i] = params[k]
	}
	return out
}
