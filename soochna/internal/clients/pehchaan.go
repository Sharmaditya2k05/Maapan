// Package clients wraps soochna's calls to pehchaan.
package clients

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/taraju/mool/svc"
)

// Tenant is pehchaan's internal tenant shape.
type Tenant struct {
	ID           string `json:"id"`
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin"`
	StateCode    string `json:"state_code"`
	OwnerPhone   string `json:"owner_phone"`
}

// Tenants fetches tenants with a 10-minute cache.
type Tenants struct {
	C     *svc.Client
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	t   Tenant
	exp time.Time
}

// NewTenants builds the client.
func NewTenants(c *svc.Client) *Tenants { return &Tenants{C: c, cache: map[string]cached{}} }

// Get returns tenant id.
func (t *Tenants) Get(ctx context.Context, id string) (Tenant, error) {
	t.mu.Lock()
	if c, ok := t.cache[id]; ok && time.Now().Before(c.exp) {
		t.mu.Unlock()
		return c.t, nil
	}
	t.mu.Unlock()
	var out Tenant
	if err := t.C.Get(ctx, "/internal/v1/tenants/"+url.PathEscape(id), &out); err != nil {
		return Tenant{}, err
	}
	t.mu.Lock()
	t.cache[id] = cached{out, time.Now().Add(10 * time.Minute)}
	t.mu.Unlock()
	return out, nil
}
