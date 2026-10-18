// Package clients wraps the synchronous calls aadesh makes to pehchaan and bahi.
package clients

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/taraju/mool/svc"
)

// Store is the slice of pehchaan's store that billing needs.
type Store struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	StateCode string `json:"state_code"`
	GSTIN     string `json:"gstin"`
	Address   string `json:"address"`
}

// Party is the slice of bahi's party that billing needs.
type Party struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	GSTIN     string `json:"gstin"`
	StateCode string `json:"state_code"`
}

// Directory resolves stores and parties.
type Directory struct {
	Pehchaan *svc.Client
	Bahi     *svc.Client

	mu     sync.Mutex
	stores map[string]cachedStore
}

type cachedStore struct {
	s   Store
	exp time.Time
}

// NewDirectory builds a Directory.
func NewDirectory(pehchaan, bahi *svc.Client) *Directory {
	return &Directory{Pehchaan: pehchaan, Bahi: bahi, stores: map[string]cachedStore{}}
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
