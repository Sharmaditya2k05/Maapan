package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// VerifySignature checks X-Razorpay-Signature: hex(HMAC-SHA256(secret, rawBody)).
func VerifySignature(rawBody []byte, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// Sign computes the signature (tests and the local simulator use it).
func Sign(rawBody []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	return hex.EncodeToString(mac.Sum(nil))
}

// WebhookKind is what lenden does with a webhook.
type WebhookKind string

// Kinds of normalised webhook.
const (
	KindCapture    WebhookKind = "capture"    // a payment was captured
	KindRefund     WebhookKind = "refund"     // a refund was processed
	KindSettlement WebhookKind = "settlement" // the gateway paid out a settlement
	KindIgnore     WebhookKind = "ignore"     // not relevant (still acknowledged)
)

// Settlement is a gateway payout as reported by a webhook. Gross − Refunds
// should equal Net + Fee + FeeTax; the ledger checks and logs when it does not.
type Settlement struct {
	GatewayRef   string // setl_xxx
	UTR          string
	GrossPaise   int64
	FeePaise     int64 // MDR / platform fee, ex tax
	FeeTaxPaise  int64 // GST on the fee
	RefundsPaise int64
	NetPaise     int64
	SettledAt    time.Time
}

// WebhookEvent is a gateway webhook normalised to what lenden needs.
type WebhookEvent struct {
	Type        string // e.g. payment.captured
	Kind        WebhookKind
	RequestID   string // our payment_request id from notes/reference_id
	GatewayRef  string // qr_/plink_ id when present
	PaymentRef  string // pay_xxx
	AmountPaise int64  // payment amount (capture) or refund amount (refund)
	Method      string
	CapturedAt  time.Time
	RefundRef   string      // rfnd_xxx
	RefundNote  string      // our refund id from refund notes
	Settlement  *Settlement // KindSettlement only
}

type rzpEntity struct {
	ID          string          `json:"id"`
	Amount      int64           `json:"amount"`
	Method      string          `json:"method"`
	Status      string          `json:"status"`
	PaymentID   string          `json:"payment_id"`
	ReferenceID string          `json:"reference_id"`
	Receipt     string          `json:"receipt"`
	Fees        int64           `json:"fees"`
	Tax         int64           `json:"tax"`
	UTR         string          `json:"utr"`
	CreatedAt   int64           `json:"created_at"`
	Notes       json.RawMessage `json:"notes"`
	notes       map[string]string
}

func (e *rzpEntity) note(key string) string {
	if e.notes == nil {
		e.notes = map[string]string{}
		// Razorpay sends notes as an object, or as [] when empty.
		_ = json.Unmarshal(e.Notes, &e.notes)
	}
	return e.notes[key]
}

type rzpWebhook struct {
	Event     string `json:"event"`
	CreatedAt int64  `json:"created_at"`
	Payload   struct {
		Payment *struct {
			Entity rzpEntity `json:"entity"`
		} `json:"payment"`
		QRCode *struct {
			Entity rzpEntity `json:"entity"`
		} `json:"qr_code"`
		PaymentLink *struct {
			Entity rzpEntity `json:"entity"`
		} `json:"payment_link"`
		Refund *struct {
			Entity rzpEntity `json:"entity"`
		} `json:"refund"`
		Settlement *struct {
			Entity rzpEntity `json:"entity"`
		} `json:"settlement"`
	} `json:"payload"`
}

// ParseWebhook normalises the events lenden handles: payment.captured,
// qr_code.credited, payment_link.paid, refund.processed, settlement.processed.
// Everything else is KindIgnore.
func ParseWebhook(raw []byte) (WebhookEvent, error) {
	var w rzpWebhook
	if err := json.Unmarshal(raw, &w); err != nil {
		return WebhookEvent{}, err
	}
	if w.Event == "" {
		return WebhookEvent{}, errors.New("webhook has no event field")
	}
	ev := WebhookEvent{Type: w.Event, Kind: KindIgnore}
	switch w.Event {
	case "payment.captured", "qr_code.credited", "payment_link.paid":
		if w.Payload.Payment == nil {
			return ev, errors.New(w.Event + ": missing payment entity")
		}
		p := &w.Payload.Payment.Entity
		if p.Status != "" && p.Status != "captured" {
			return ev, nil // e.g. authorized only: wait for capture
		}
		ev.Kind = KindCapture
		ev.PaymentRef = p.ID
		ev.AmountPaise = p.Amount
		ev.Method = MapMethod(p.Method)
		ev.CapturedAt = time.Unix(p.CreatedAt, 0).UTC()
		ev.RequestID = p.note("request_id")
		if q := w.Payload.QRCode; q != nil {
			ev.GatewayRef = q.Entity.ID
			if ev.RequestID == "" {
				ev.RequestID = q.Entity.note("request_id")
			}
		}
		if l := w.Payload.PaymentLink; l != nil {
			ev.GatewayRef = l.Entity.ID
			if ev.RequestID == "" {
				ev.RequestID = l.Entity.note("request_id")
			}
			if ev.RequestID == "" {
				ev.RequestID = l.Entity.ReferenceID
			}
		}
	case "refund.processed":
		if w.Payload.Refund == nil {
			return ev, errors.New("refund.processed: missing refund entity")
		}
		r := &w.Payload.Refund.Entity
		ev.Kind = KindRefund
		ev.RefundRef = r.ID
		ev.PaymentRef = r.PaymentID
		ev.AmountPaise = r.Amount
		ev.RefundNote = r.note("refund_id")
		if ev.RefundNote == "" {
			ev.RefundNote = r.Receipt
		}
	case "settlement.processed":
		if w.Payload.Settlement == nil {
			return ev, errors.New("settlement.processed: missing settlement entity")
		}
		s := &w.Payload.Settlement.Entity
		if s.ID == "" {
			return ev, errors.New("settlement.processed: missing settlement id")
		}
		if s.Status != "" && s.Status != "processed" {
			return ev, nil
		}
		// Razorpay reports the net payout (amount), the fee (fees) and GST on it (tax);
		// refunds are already netted into the payout, so gross = net + fees + tax.
		ev.Kind = KindSettlement
		ev.GatewayRef = s.ID
		ev.AmountPaise = s.Amount
		ev.Settlement = &Settlement{
			GatewayRef: s.ID, UTR: s.UTR, NetPaise: s.Amount, FeePaise: s.Fees, FeeTaxPaise: s.Tax,
			GrossPaise: s.Amount + s.Fees + s.Tax, SettledAt: time.Unix(s.CreatedAt, 0).UTC(),
		}
		if s.CreatedAt == 0 {
			ev.Settlement.SettledAt = time.Unix(w.CreatedAt, 0).UTC()
		}
		if s.Amount < 0 || s.Fees < 0 || s.Tax < 0 {
			return ev, errors.New("settlement.processed: negative amounts")
		}
		return ev, nil
	}
	if ev.Kind != KindIgnore && ev.AmountPaise <= 0 {
		return ev, errors.New(w.Event + ": amount must be positive")
	}
	return ev, nil
}
