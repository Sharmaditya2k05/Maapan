package messaging

import (
	"context"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// SenderSettings is a tenant's entry in the sender/template registry: its own
// DLT header, WhatsApp number, e-mail sender, provider template ids and the
// payment-reminder schedule. Empty fields fall back to Maapan's defaults.
type SenderSettings struct {
	SMSSenderID      string           `json:"sms_sender_id"`
	WhatsAppSource   string           `json:"whatsapp_source"`
	WhatsAppAppName  string           `json:"whatsapp_app_name"`
	EmailFrom        string           `json:"email_from"`
	EmailFromName    string           `json:"email_from_name"`
	EmailReplyTo     string           `json:"email_reply_to"`
	PaymentReminders bool             `json:"payment_reminders"`
	ReminderDays     []int            `json:"reminder_days"`
	ReminderChannels []string         `json:"reminder_channels"`
	Templates        []TenantTemplate `json:"templates"`
	UpdatedAt        *time.Time       `json:"updated_at,omitempty"`
	UpdatedBy        *string          `json:"updated_by,omitempty"`
}

// TenantTemplate maps a Maapan template to the tenant's provider template id.
type TenantTemplate struct {
	Template           string `json:"template"`
	Channel            string `json:"channel"` // sms | whatsapp
	ProviderTemplateID string `json:"provider_template_id"`
}

var dltHeader = regexp.MustCompile(`^[A-Z]{6}$`)

// Validate normalises and checks the settings.
func (in *SenderSettings) Validate() error {
	if in.SMSSenderID != "" && !dltHeader.MatchString(in.SMSSenderID) {
		return httpx.Validation("sms_sender_id must be a 6-letter DLT header (e.g. SHRMST)")
	}
	if in.WhatsAppSource != "" {
		p, err := NormalizePhone(in.WhatsAppSource)
		if err != nil {
			return httpx.Validation("whatsapp_source: %s", err.Error())
		}
		in.WhatsAppSource = p
	}
	for _, f := range []*string{&in.EmailFrom, &in.EmailReplyTo} {
		if *f != "" {
			a, err := NormalizeEmail(*f)
			if err != nil {
				return httpx.Validation("e-mail sender: %s", err.Error())
			}
			*f = a
		}
	}
	if len(in.EmailFromName) > 80 {
		return httpx.Validation("email_from_name too long")
	}
	if in.ReminderDays == nil {
		in.ReminderDays = []int{0, 7, 15, 30}
	}
	for _, d := range in.ReminderDays {
		if d < 0 || d > 365 {
			return httpx.Validation("reminder_days must be 0..365")
		}
	}
	slices.Sort(in.ReminderDays)
	in.ReminderDays = slices.Compact(in.ReminderDays)
	if in.ReminderChannels == nil {
		in.ReminderChannels = []string{templates.WhatsApp, templates.Email}
	}
	for _, c := range in.ReminderChannels {
		if c != templates.SMS && c != templates.WhatsApp && c != templates.Email {
			return httpx.Validation("reminder_channels: sms, whatsapp or email")
		}
	}
	for i, t := range in.Templates {
		if !templates.Known(t.Template) || (t.Channel != templates.SMS && t.Channel != templates.WhatsApp) || t.ProviderTemplateID == "" {
			return httpx.Validation("templates[%d]: known template, channel sms|whatsapp and provider_template_id required", i)
		}
	}
	return nil
}

// GetSenders returns the tenant's registry entry (defaults when none is stored).
func (s *Service) GetSenders(ctx context.Context, tenantID string) (*SenderSettings, error) {
	out := &SenderSettings{ReminderDays: []int{0, 7, 15, 30}, ReminderChannels: []string{templates.WhatsApp, templates.Email}}
	err := s.DB.QueryRow(ctx, `SELECT COALESCE(sms_sender_id,''), COALESCE(whatsapp_source,''), COALESCE(whatsapp_app_name,''),
		COALESCE(email_from,''), COALESCE(email_from_name,''), COALESCE(email_reply_to,''), payment_reminders, reminder_days,
		reminder_channels, updated_at, updated_by FROM tenant_senders WHERE tenant_id=$1`, tenantID).
		Scan(&out.SMSSenderID, &out.WhatsAppSource, &out.WhatsAppAppName, &out.EmailFrom, &out.EmailFromName, &out.EmailReplyTo,
			&out.PaymentReminders, &out.ReminderDays, &out.ReminderChannels, &out.UpdatedAt, &out.UpdatedBy)
	if err != nil && !db.IsNotFound(err) {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT template, channel, provider_template_id FROM tenant_templates
		WHERE tenant_id=$1 ORDER BY template, channel`, tenantID)
	if err != nil {
		return nil, err
	}
	out.Templates, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TenantTemplate, error) {
		var t TenantTemplate
		err := r.Scan(&t.Template, &t.Channel, &t.ProviderTemplateID)
		return t, err
	})
	if out.Templates == nil {
		out.Templates = []TenantTemplate{}
	}
	return out, err
}

// PutSenders replaces the tenant's registry entry (templates included).
func (s *Service) PutSenders(ctx context.Context, tenantID, by string, in SenderSettings) (*SenderSettings, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_senders (tenant_id, sms_sender_id, whatsapp_source, whatsapp_app_name,
				email_from, email_from_name, email_reply_to, payment_reminders, reminder_days, reminder_channels, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (tenant_id) DO UPDATE SET sms_sender_id=EXCLUDED.sms_sender_id, whatsapp_source=EXCLUDED.whatsapp_source,
			  whatsapp_app_name=EXCLUDED.whatsapp_app_name, email_from=EXCLUDED.email_from, email_from_name=EXCLUDED.email_from_name,
			  email_reply_to=EXCLUDED.email_reply_to, payment_reminders=EXCLUDED.payment_reminders,
			  reminder_days=EXCLUDED.reminder_days, reminder_channels=EXCLUDED.reminder_channels,
			  updated_by=EXCLUDED.updated_by, updated_at=now()`,
			tenantID, nilIfEmpty(in.SMSSenderID), nilIfEmpty(in.WhatsAppSource), nilIfEmpty(in.WhatsAppAppName),
			nilIfEmpty(in.EmailFrom), nilIfEmpty(in.EmailFromName), nilIfEmpty(in.EmailReplyTo), in.PaymentReminders,
			in.ReminderDays, in.ReminderChannels, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tenant_templates WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		for _, t := range in.Templates {
			if _, err := tx.Exec(ctx, `INSERT INTO tenant_templates (tenant_id, template, channel, provider_template_id)
				VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_id, template, channel) DO UPDATE SET
				provider_template_id=EXCLUDED.provider_template_id, updated_at=now()`,
				tenantID, t.Template, t.Channel, t.ProviderTemplateID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetSenders(ctx, tenantID)
}

// senderOverrides are the registry values applied to one send.
type senderOverrides struct {
	SMSSenderID, WhatsAppSource, WhatsAppAppName string
	EmailFrom, EmailFromName, EmailReplyTo       string
	TemplateID                                   string
}

// overrides looks up the tenant's sender and template id for one message.
// A tenant template id is only used together with the tenant's own sender
// (a store's DLT template is registered under its own header / number).
func (s *Service) overrides(ctx context.Context, tenantID, channel, template string) (senderOverrides, error) {
	var o senderOverrides
	if tenantID == "" {
		return o, nil
	}
	err := s.DB.QueryRow(ctx, `SELECT COALESCE(ts.sms_sender_id,''), COALESCE(ts.whatsapp_source,''),
		COALESCE(ts.whatsapp_app_name,''), COALESCE(ts.email_from,''), COALESCE(ts.email_from_name,''),
		COALESCE(ts.email_reply_to,''), COALESCE(tt.provider_template_id,'')
		FROM (SELECT $1::uuid AS tenant_id) k
		LEFT JOIN tenant_senders ts ON ts.tenant_id = k.tenant_id
		LEFT JOIN tenant_templates tt ON tt.tenant_id = k.tenant_id AND tt.template=$2 AND tt.channel=$3`,
		tenantID, template, channel).Scan(&o.SMSSenderID, &o.WhatsAppSource, &o.WhatsAppAppName,
		&o.EmailFrom, &o.EmailFromName, &o.EmailReplyTo, &o.TemplateID)
	if err != nil {
		return senderOverrides{}, err
	}
	switch channel {
	case templates.SMS:
		if o.SMSSenderID == "" {
			o.TemplateID = ""
		}
	case templates.WhatsApp:
		if o.WhatsAppSource == "" {
			o.TemplateID = ""
		}
	}
	return o, nil
}
