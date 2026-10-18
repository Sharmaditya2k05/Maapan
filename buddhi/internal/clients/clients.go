// Package clients wraps buddhi's read-only calls to other services (contracts in
// sanchalan/docs/ARCHITECTURE.md §6) and its one write: sending messages via soochna.
package clients

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/taraju/mool/svc"
)

// Clients groups the downstream services.
type Clients struct {
	Bahi     *svc.Client
	Bhandar  *svc.Client
	Aadesh   *svc.Client
	Soochna  *svc.Client
	Pehchaan *svc.Client
}

// StockItem is one row of bhandar's stock summary.
type StockItem struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	OnHand       float64 `json:"on_hand"`
	ValuePaise   int64   `json:"value_paise"`
	ReorderLevel float64 `json:"reorder_level"`
}

// StockSummary calls bhandar GET /internal/v1/stock/summary.
func (c *Clients) StockSummary(ctx context.Context, storeID string) ([]StockItem, error) {
	var out struct {
		Items []StockItem `json:"items"`
	}
	q := url.Values{}
	if storeID != "" {
		q.Set("store_id", storeID)
	}
	err := c.Bhandar.Get(ctx, "/internal/v1/stock/summary?"+q.Encode(), &out)
	return out.Items, err
}

// BooksSummary calls bahi GET /internal/v1/reports/summary (raw JSON, passed to the model).
func (c *Clients) BooksSummary(ctx context.Context, from, to string) (json.RawMessage, error) {
	q := url.Values{}
	if from != "" {
		q.Set("from", from)
	}
	if to != "" {
		q.Set("to", to)
	}
	var out json.RawMessage
	err := c.Bahi.Get(ctx, "/internal/v1/reports/summary?"+q.Encode(), &out)
	return out, err
}

// Invoices calls aadesh GET /internal/v1/invoices with filters (raw JSON).
func (c *Clients) Invoices(ctx context.Context, q url.Values) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Aadesh.Get(ctx, "/internal/v1/invoices?"+q.Encode(), &out)
	return out, err
}

// Tenant is pehchaan's tenant view.
type Tenant struct {
	ID           string `json:"id"`
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin"`
	StateCode    string `json:"state_code"`
	OwnerPhone   string `json:"owner_phone"`
}

// Tenant calls pehchaan GET /internal/v1/tenants/{id}.
func (c *Clients) Tenant(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	err := c.Pehchaan.Get(ctx, "/internal/v1/tenants/"+url.PathEscape(id), &t)
	return t, err
}

// Store is pehchaan's store view (for the buyer state in bill scanning).
type Store struct {
	ID        string `json:"id"`
	StateCode string `json:"state_code"`
}

// Store calls pehchaan GET /internal/v1/stores/{id}.
func (c *Clients) Store(ctx context.Context, id string) (Store, error) {
	var s Store
	err := c.Pehchaan.Get(ctx, "/internal/v1/stores/"+url.PathEscape(id), &s)
	return s, err
}

// Message is soochna's send request.
type Message struct {
	Channel        string            `json:"channel"`
	To             string            `json:"to"`
	Template       string            `json:"template"`
	Params         map[string]string `json:"params"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// Send calls soochna POST /internal/v1/messages.
func (c *Clients) Send(ctx context.Context, m Message) error {
	return c.Soochna.Post(ctx, "/internal/v1/messages", m, nil)
}
