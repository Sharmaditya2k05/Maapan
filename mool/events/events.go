// Package events is Taraju's async messaging: an envelope, publishers (Pub/Sub
// on Cloud Run, direct HTTP locally), a push-subscriber handler and a
// transactional outbox so events are never lost when a request commits.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event types. See sanchalan/docs/ARCHITECTURE.md §5.
const (
	InvoiceCreated      = "aadesh.invoice.created"
	InvoiceCancelled    = "aadesh.invoice.cancelled"
	NoteCreated         = "aadesh.note.created"
	PaymentCaptured     = "lenden.payment.captured"
	PaymentRefunded     = "lenden.payment.refunded"
	SettlementProcessed = "lenden.settlement.processed"
	StockLow            = "bhandar.stock.low"
	BatchExpiring       = "bhandar.batch.expiring"
	StockMoved          = "bhandar.stock.moved"
	ProductionCompleted = "nirmaan.production.completed"
	ImportCompleted     = "aayaat.import.completed"
	TenantCreated       = "pehchaan.tenant.created"
	EInvoiceGenerated   = "kar.einvoice.generated"
	EWayGenerated       = "kar.eway.generated"
)

// Envelope is the message format on every topic.
type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	TenantID   string          `json:"tenant_id"`
	StoreID    string          `json:"store_id,omitempty"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// New builds an envelope with a fresh ID.
func New(eventType, source, tenantID, storeID string, data any) (Envelope, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		ID: uuid.NewString(), Type: eventType, TenantID: tenantID, StoreID: storeID,
		Source: source, OccurredAt: time.Now().UTC(), Data: b,
	}, nil
}

// Decode unmarshals the payload.
func (e Envelope) Decode(dst any) error { return json.Unmarshal(e.Data, dst) }

// Publisher sends envelopes to their topic.
type Publisher interface {
	Publish(ctx context.Context, e Envelope) error
}

// Subscriptions is the subscriber matrix (topic → services). Mirrors
// ARCHITECTURE.md §5 and sanchalan/scripts/pubsub-setup.sh.
var Subscriptions = map[string][]string{
	InvoiceCreated:      {"bahi", "bhandar", "soochna", "buddhi", "kar"},
	InvoiceCancelled:    {"bahi", "bhandar", "buddhi"},
	NoteCreated:         {"bahi", "bhandar", "buddhi", "kar"},
	PaymentCaptured:     {"bahi", "aadesh", "soochna", "buddhi"},
	PaymentRefunded:     {"bahi", "aadesh"},
	SettlementProcessed: {"bahi"},
	StockLow:            {"soochna", "buddhi"},
	BatchExpiring:       {"soochna"},
	StockMoved:          {"bahi", "buddhi"},
	ProductionCompleted: {"bhandar", "bahi"},
	ImportCompleted:     {"soochna"},
	TenantCreated:       {"bahi", "bhandar", "kar"},
	EInvoiceGenerated:   {"aadesh", "soochna"},
	EWayGenerated:       {"aadesh"},
}
