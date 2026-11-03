package messaging

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// DeliveryUpdate is a provider status callback normalised.
type DeliveryUpdate struct {
	Ref    string
	Status string // sent | delivered | read | failed
	Error  string
}

// Inbound is a message a recipient sent back (used for STOP / START).
type Inbound struct {
	From string
	Text string
}

// WebhookResult is what a provider callback changed.
type WebhookResult struct {
	Updated int    `json:"updated"`
	Consent string `json:"consent,omitempty"` // opted_out | opted_in when the callback was a keyword
}

// ApplyWebhook verifies the token and applies provider delivery reports and
// inbound STOP/START keywords. Gupshup sends both to one callback URL; MSG91
// inbound SMS can be pointed at the same /v1/webhooks/msg91 URL.
func (s *Service) ApplyWebhook(ctx context.Context, provider, token string, raw []byte, contentType string) (*WebhookResult, error) {
	if s.WebhookToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.WebhookToken)) != 1 {
		return nil, httpx.Unauthorized("bad webhook token")
	}
	var ups []DeliveryUpdate
	var in *Inbound
	var channel string
	var err error
	switch provider {
	case "gupshup":
		channel = templates.WhatsApp
		ups, in, err = ParseGupshup(raw)
	case "msg91":
		channel = templates.SMS
		ups, in, err = ParseMSG91(raw, contentType)
	default:
		return nil, httpx.NotFound("unknown provider %s", provider)
	}
	if err != nil {
		return nil, httpx.BadRequest("%s", err.Error())
	}
	res := &WebhookResult{}
	if in != nil {
		if res.Consent, err = s.ApplyInbound(ctx, channel, in.From, in.Text); err != nil {
			return nil, err
		}
	}
	for _, u := range ups {
		// Never move backwards (e.g. a late "sent" after "read").
		tag, err := s.DB.Exec(ctx, `UPDATE messages SET status=$3, error=COALESCE(NULLIF($4,''), error), updated_at=now()
			WHERE provider=$1 AND provider_ref=$2 AND
			  (CASE status WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END) <
			  (CASE $3 WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END)`,
			provider, u.Ref, u.Status, u.Error)
		if err != nil {
			return res, err
		}
		res.Updated += int(tag.RowsAffected())
	}
	return res, nil
}

// ParseGupshup reads a Gupshup callback: message-event (delivery report) or
// message (inbound text / quick-reply button from the customer).
func ParseGupshup(raw []byte) ([]DeliveryUpdate, *Inbound, error) {
	var ev struct {
		Type    string `json:"type"`
		Payload struct {
			ID      string `json:"id"`
			GsID    string `json:"gsId"`
			Type    string `json:"type"`
			Source  string `json:"source"`
			Payload struct {
				Reason string `json:"reason"`
				Text   string `json:"text"`
				Title  string `json:"title"`
			} `json:"payload"`
			Sender struct {
				Phone string `json:"phone"`
			} `json:"sender"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, nil, err
	}
	switch ev.Type {
	case "message":
		from := ev.Payload.Source
		if from == "" {
			from = ev.Payload.Sender.Phone
		}
		text := ev.Payload.Payload.Text
		if text == "" {
			text = ev.Payload.Payload.Title // quick-reply button
		}
		if from == "" || text == "" {
			return nil, nil, nil
		}
		return nil, &Inbound{From: from, Text: text}, nil
	case "message-event":
	default:
		return nil, nil, nil // user-event etc.
	}
	status := map[string]string{"sent": "sent", "delivered": "delivered", "read": "read", "failed": "failed"}[ev.Payload.Type]
	if status == "" {
		return nil, nil, nil // enqueued, etc.
	}
	var out []DeliveryUpdate
	for _, ref := range []string{ev.Payload.ID, ev.Payload.GsID} {
		if ref != "" {
			out = append(out, DeliveryUpdate{Ref: ref, Status: status, Error: ev.Payload.Payload.Reason})
		}
	}
	return out, nil, nil
}

// ParseMSG91 reads MSG91 callbacks. Delivery reports are a single object or an
// array of {"requestId", "status"}; status may be a word or MSG91's numeric code
// (1 delivered, 2 failed, 9 NDNC, 16 rejected, 17 blocked, 25 rejected).
// Inbound SMS (JSON or form) carry a sender ("sender" | "mobile" | "from")
// and text ("message" | "text" | "content" | "keyword") and no requestId.
func ParseMSG91(raw []byte, contentType string) ([]DeliveryUpdate, *Inbound, error) {
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, nil, errors.New("unrecognised MSG91 form")
		}
		return nil, inboundFrom(func(k string) string { return form.Get(k) }), nil
	}
	type rep struct {
		RequestID string          `json:"requestId"`
		Status    json.RawMessage `json:"status"`
		Desc      string          `json:"desc"`
	}
	var reps []rep
	if err := json.Unmarshal(raw, &reps); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, nil, errors.New("unrecognised MSG91 report")
		}
		if _, isReport := one["requestId"]; !isReport {
			return nil, inboundFrom(func(k string) string { s, _ := one[k].(string); return s }), nil
		}
		var r rep
		_ = json.Unmarshal(raw, &r)
		reps = []rep{r}
	}
	var out []DeliveryUpdate
	for _, r := range reps {
		st := strings.ToLower(strings.Trim(string(r.Status), `"`))
		var status string
		switch st {
		case "1", "delivered", "delivrd":
			status = "delivered"
		case "2", "9", "16", "17", "25", "failed", "rejected", "ndnc":
			status = "failed"
		case "8", "sent", "submitted":
			status = "sent"
		}
		if r.RequestID != "" && status != "" {
			out = append(out, DeliveryUpdate{Ref: r.RequestID, Status: status, Error: r.Desc})
		}
	}
	return out, nil, nil
}

func inboundFrom(get func(string) string) *Inbound {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(get(k)); v != "" {
				return v
			}
		}
		return ""
	}
	from, text := pick("sender", "mobile", "from", "number"), pick("message", "text", "content", "keyword")
	if from == "" || text == "" {
		return nil
	}
	return &Inbound{From: from, Text: text}
}
