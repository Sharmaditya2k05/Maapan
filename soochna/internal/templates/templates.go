// Package templates renders message bodies per template and channel.
//
// Bodies are bilingual (English + Hindi) and short enough for one SMS segment
// where possible. Providers that need pre-approved templates (MSG91 DLT,
// WhatsApp business templates) use ParamOrder to send variables positionally;
// the approved provider template must contain the same variables in that order.
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
	LedgerStatement = "ledger_statement"
	StockLow        = "stock_low"
	BatchExpiring   = "batch_expiring"
	ImportDone      = "import_done"
	WeeklySummary   = "weekly_summary" // text written by buddhi
)

type def struct {
	params   []string // positional order for provider templates
	sms      string
	whatsapp string
}

var defs = map[string]def{
	OTP: {
		params:   []string{"otp", "minutes"},
		sms:      `{{.otp}} is your Taraju login OTP. Valid for {{.minutes}} min. Do not share it. / आपका Taraju OTP {{.otp}} है।`,
		whatsapp: "*{{.otp}}* is your Taraju login OTP. It is valid for {{.minutes}} minutes. Do not share it with anyone.\n\nआपका Taraju लॉगिन OTP *{{.otp}}* है। इसे किसी के साथ साझा न करें।",
	},
	Invoice: {
		params:   []string{"party_name", "business", "number", "amount", "date"},
		sms:      `Dear {{.party_name}}, thank you for shopping at {{.business}}. Bill {{.number}} dated {{.date}}: {{.amount}}.`,
		whatsapp: "Namaste {{.party_name}} 🙏\n\nThank you for shopping at *{{.business}}*.\nBill: *{{.number}}*\nDate: {{.date}}\nAmount: *{{.amount}}*\n{{if .link}}\nView bill: {{.link}}\n{{end}}\n{{.business}} से खरीदारी के लिए धन्यवाद।",
	},
	PaymentReceipt: {
		params:   []string{"party_name", "business", "amount", "ref"},
		sms:      `Received {{.amount}} from {{.party_name}} at {{.business}}. Ref {{.ref}}. Thank you! / भुगतान प्राप्त हुआ।`,
		whatsapp: "Payment received ✅\n\n*{{.amount}}* from {{.party_name}} to *{{.business}}*.\nReference: {{.ref}}\n\nभुगतान प्राप्त हुआ, धन्यवाद।",
	},
	LedgerStatement: {
		params:   []string{"party_name", "business", "balance", "as_of"},
		sms:      `Dear {{.party_name}}, your balance with {{.business}} as of {{.as_of}} is {{.balance}}.`,
		whatsapp: "Namaste {{.party_name}},\n\nYour account balance with *{{.business}}* as of {{.as_of}} is *{{.balance}}*.\n{{if .link}}\nStatement: {{.link}}\n{{end}}\nबकाया राशि: {{.balance}}",
	},
	StockLow: {
		params:   []string{"item", "on_hand", "reorder_level", "business"},
		sms:      `{{.business}}: low stock - {{.item}} has {{.on_hand}} left (reorder at {{.reorder_level}}).`,
		whatsapp: "⚠️ *Low stock* at {{.business}}\n\n{{.item}}: only *{{.on_hand}}* left (reorder level {{.reorder_level}}).\n\nस्टॉक कम है — दोबारा ऑर्डर करें।",
	},
	BatchExpiring: {
		params:   []string{"item", "batch", "expiry", "days", "business"},
		sms:      `{{.business}}: {{.item}} batch {{.batch}} expires on {{.expiry}} ({{.days}} days).`,
		whatsapp: "⏳ *Expiry alert* at {{.business}}\n\n{{.item}} — batch {{.batch}} ({{.qty}} units) expires on *{{.expiry}}* ({{.days}} days left).\n\nमाल की समाप्ति तिथि नज़दीक है।",
	},
	WeeklySummary: {
		params:   []string{"text"},
		sms:      `Taraju weekly summary: {{.text}}`,
		whatsapp: "📊 *Your week at a glance*\n\n{{.text}}\n\n— Taraju",
	},
	ImportDone: {
		params:   []string{"kind", "imported", "total", "failed"},
		sms:      `Taraju: your {{.kind}} import finished - {{.imported}}/{{.total}} imported, {{.failed}} failed.`,
		whatsapp: "📥 *Import finished*\n\n{{.kind}}: {{.imported}} of {{.total}} imported, {{.failed}} failed.\n{{if .link}}\nDetails: {{.link}}\n{{end}}\nआपका डेटा आयात पूरा हुआ।",
	},
}

var compiled = map[string]*template.Template{}

func init() {
	for name, d := range defs {
		for ch, body := range map[string]string{"sms": d.sms, "whatsapp": d.whatsapp} {
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

// ParamOrder returns the positional variable order for provider templates.
func ParamOrder(name string) []string { return defs[name].params }

// Render produces the body for name on channel ("sms" | "whatsapp").
// Missing params render as empty text; required positional params must be present.
func Render(name, channel string, params map[string]string) (string, error) {
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
