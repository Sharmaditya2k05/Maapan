// Package clients wraps the synchronous calls aadesh makes to pehchaan
// (stores), bahi (parties), bhandar (items) and lenden (counter payments).
package clients

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/taraju/mool/svc"
)

// Store is the slice of pehchaan's store that billing needs, with the GST
// registration it bills under (PHASE3-CONTRACTS.md §1). gstin, state_code and
// address are the registration's when the store has none of its own. An empty
// RegistrationID means an older pehchaan: the store is then on the primary.
type Store struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenant_id"`
	Name                string `json:"name"`
	Code                string `json:"code"`
	StateCode           string `json:"state_code"`
	GSTIN               string `json:"gstin"`
	Address             string `json:"address"`
	Phone               string `json:"phone"`
	RegistrationID      string `json:"registration_id"`
	LegalName           string `json:"legal_name"`
	TradeName           string `json:"trade_name"`
	City                string `json:"city"`
	PIN                 string `json:"pin"`
	Composition         bool   `json:"composition"`
	EInvoiceApplicable  bool   `json:"einvoice_applicable"`
	RegistrationPrimary bool   `json:"registration_primary"`
}

// Party is the slice of bahi's party that billing needs.
type Party struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Phone            string `json:"phone"`
	GSTIN            string `json:"gstin"`
	StateCode        string `json:"state_code"`
	CreditLimitPaise int64  `json:"credit_limit_paise"`
	CreditDays       int    `json:"credit_days"`
	BalancePaise     int64  `json:"balance_paise"` // positive = the party owes us
}

// Item is the slice of bhandar's item master that billing validates against.
type Item struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SKU              string `json:"sku"`
	HSN              string `json:"hsn"`
	Unit             string `json:"unit"`
	SalePricePaise   int64  `json:"sale_price_paise"`
	MRPPaise         int64  `json:"mrp_paise"`
	TaxRateBP        int    `json:"tax_rate_bp"`
	CessRateBP       int    `json:"cess_rate_bp"`
	CessPerUnitPaise int64  `json:"cess_per_unit_paise"`
	TaxCategory      string `json:"tax_category"`
	PriceIncludesTax bool   `json:"price_includes_tax"`
	TrackBatches     bool   `json:"track_batches"`
	Status           string `json:"status"`
	AltUnits         []struct {
		Unit   string  `json:"unit"`
		Factor float64 `json:"factor"`
	} `json:"alt_units"`
}

// PaymentRequestInput is lenden's POST /internal/v1/payment-requests body.
type PaymentRequestInput struct {
	InvoiceID    string `json:"invoice_id,omitempty"`
	PartyID      string `json:"party_id,omitempty"`
	PartyPhone   string `json:"party_phone,omitempty"`
	AmountPaise  int64  `json:"amount_paise"`
	Purpose      string `json:"purpose"`
	Kind         string `json:"kind"`
	ExpiresInSec int    `json:"expires_in_sec,omitempty"`
	StoreID      string `json:"store_id,omitempty"`
}

// PaymentRequest is lenden's response: what the counter shows the customer.
type PaymentRequest struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	UPIQRImageURL string `json:"upi_qr_image_url,omitempty"`
	UPIIntent     string `json:"upi_intent,omitempty"`
	ShortURL      string `json:"short_url,omitempty"`
	GatewayRef    string `json:"gateway_ref,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// Directory resolves stores, parties, items and payment requests.
type Directory struct {
	Pehchaan *svc.Client
	Bahi     *svc.Client
	Bhandar  *svc.Client
	Lenden   *svc.Client

	mu     sync.Mutex
	stores map[string]cachedStore
}

type cachedStore struct {
	s   Store
	exp time.Time
}

// NewDirectory builds a Directory. bhandar and lenden may be nil-configured
// (empty base URL): item lines then fail with 503 and counter QR is unavailable.
func NewDirectory(pehchaan, bahi, bhandar, lenden *svc.Client) *Directory {
	return &Directory{Pehchaan: pehchaan, Bahi: bahi, Bhandar: bhandar, Lenden: lenden, stores: map[string]cachedStore{}}
}

// Store returns a store, cached for 10 minutes (store state rarely changes and
// billing must stay fast).
func (d *Directory) Store(ctx context.Context, tenantID, storeID string) (Store, error) {
	key := tenantID + "/" + storeID
	d.mu.Lock()
	if c, ok := d.stores[key]; ok && time.Now().Before(c.exp) {
		d.mu.Unlock()
		return c.s, nil
	}
	d.mu.Unlock()

	var s Store
	if err := d.Pehchaan.Get(ctx, "/internal/v1/stores/"+url.PathEscape(storeID), &s); err != nil {
		return Store{}, err
	}
	d.mu.Lock()
	d.stores[key] = cachedStore{s: s, exp: time.Now().Add(10 * time.Minute)}
	d.mu.Unlock()
	return s, nil
}

// Party fetches a party from bahi.
func (d *Directory) Party(ctx context.Context, partyID string) (Party, error) {
	var p Party
	err := d.Bahi.Get(ctx, "/internal/v1/parties/"+url.PathEscape(partyID), &p)
	return p, err
}

// Items fetches items from bhandar, keyed by id. Missing ids are simply absent.
func (d *Directory) Items(ctx context.Context, ids []string) (map[string]Item, error) {
	out := map[string]Item{}
	if len(ids) == 0 {
		return out, nil
	}
	var res struct {
		Items []Item `json:"items"`
	}
	if err := d.Bhandar.Post(ctx, "/internal/v1/items/lookup", map[string]any{"ids": ids}, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		out[it.ID] = it
	}
	return out, nil
}

// PaymentRequest asks lenden for a UPI QR or payment link.
func (d *Directory) PaymentRequest(ctx context.Context, in PaymentRequestInput) (PaymentRequest, error) {
	var out PaymentRequest
	err := d.Lenden.Post(ctx, "/internal/v1/payment-requests", in, &out)
	return out, err
}
