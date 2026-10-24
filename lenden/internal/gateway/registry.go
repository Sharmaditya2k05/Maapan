package gateway

import (
	"context"
	"fmt"
	"sort"
)

// Registry holds every configured gateway. New payment requests go to the
// default (PAYMENT_GATEWAY) unless the request names another; refunds and
// reconciliation always use the gateway stored on the payment, so switching
// the default never strands older payments.
//
// With per-tenant credentials (MATURITY-PLAN D5) a tenant's own keys win over
// the platform keys: For(tenant, name) builds a driver from the tenant's
// credentials when they are stored, else falls back to the global driver, so
// deployments without tenant keys keep working unchanged.
type Registry struct {
	def       string
	byNm      map[string]Gateway       // platform-wide drivers (env keys)
	hooks     map[string]WebhookSource // webhook sources to mount
	factories map[string]Factory       // per-tenant driver builders
	// Creds resolves tenants' own keys; nil means platform keys only.
	Creds CredentialSource
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byNm: map[string]Gateway{}, hooks: map[string]WebhookSource{}, factories: map[string]Factory{}}
}

// Add registers a platform-wide gateway and its webhook source.
func (r *Registry) Add(g Gateway, hook WebhookSource) {
	r.byNm[g.Name()] = g
	if hook != nil {
		r.hooks[g.Name()] = hook
	}
}

// Enable makes a gateway usable by tenants that store their own keys for it
// (and mounts its webhook source) even when the platform has no keys for it.
func (r *Registry) Enable(name string, f Factory, hook WebhookSource) {
	r.factories[name] = f
	if hook != nil {
		if _, have := r.hooks[name]; !have {
			r.hooks[name] = hook
		}
	}
}

// SetDefault picks the gateway for new payment requests.
func (r *Registry) SetDefault(name string) error {
	if _, ok := r.byNm[name]; !ok {
		return fmt.Errorf("default gateway %q is not configured (configured: %v)", name, r.Names())
	}
	r.def = name
	return nil
}

// Get returns a platform-wide gateway by name.
func (r *Registry) Get(name string) (Gateway, bool) {
	g, ok := r.byNm[name]
	return g, ok
}

// Default returns the platform-wide gateway for new requests.
func (r *Registry) Default() Gateway { return r.byNm[r.def] }

// DefaultName is the name of the default gateway.
func (r *Registry) DefaultName() string { return r.def }

// Names lists platform-wide configured gateways, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byNm))
	for n := range r.byNm {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Supported lists every gateway lenden can talk to for some tenant: platform
// drivers plus those enabled for per-tenant keys. Sorted.
func (r *Registry) Supported() []string {
	seen := map[string]bool{}
	for n := range r.byNm {
		seen[n] = true
	}
	for n := range r.factories {
		seen[n] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TenantConfigurable reports whether tenants may store their own keys for name.
func (r *Registry) TenantConfigurable(name string) bool {
	_, ok := r.factories[name]
	return ok
}

// For returns the driver to use for tenantID on gateway name: a driver built
// from the tenant's own credentials when stored, else the platform driver.
// ErrNotConfigured when neither exists.
func (r *Registry) For(ctx context.Context, tenantID, name string) (Gateway, error) {
	if r.Creds != nil && tenantID != "" {
		if f, ok := r.factories[name]; ok {
			c, found, err := r.Creds.Credentials(ctx, tenantID, name)
			if err != nil {
				return nil, err
			}
			if found {
				return f(c)
			}
		}
	}
	if g, ok := r.byNm[name]; ok {
		return g, nil
	}
	return nil, ErrNotConfigured
}

// DefaultFor returns the gateway new requests of tenantID go to: the default
// gateway, through the tenant's own keys when it has them.
func (r *Registry) DefaultFor(ctx context.Context, tenantID string) (Gateway, error) {
	return r.For(ctx, tenantID, r.def)
}

// NamesFor lists the gateways tenantID may pick: platform ones plus those it
// holds its own keys for.
func (r *Registry) NamesFor(ctx context.Context, tenantID string) ([]string, error) {
	seen := map[string]bool{}
	for n := range r.byNm {
		seen[n] = true
	}
	if r.Creds != nil && tenantID != "" {
		for n := range r.factories {
			if seen[n] {
				continue
			}
			if _, found, err := r.Creds.Credentials(ctx, tenantID, n); err != nil {
				return nil, err
			} else if found {
				seen[n] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Webhooks lists the webhook sources to mount.
func (r *Registry) Webhooks() []WebhookSource {
	out := make([]WebhookSource, 0, len(r.hooks))
	for _, n := range r.Supported() {
		if h, ok := r.hooks[n]; ok {
			out = append(out, h)
		}
	}
	return out
}

// Mock returns the mock gateway when configured.
func (r *Registry) Mock() (*Mock, bool) {
	m, ok := r.byNm["mock"].(*Mock)
	return m, ok
}
