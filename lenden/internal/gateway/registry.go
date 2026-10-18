package gateway

import (
	"fmt"
	"sort"
)

// Registry holds every configured gateway. New payment requests go to the
// default (PAYMENT_GATEWAY) unless the request names another; refunds and
// reconciliation always use the gateway stored on the payment, so switching
// the default never strands older payments.
type Registry struct {
	def   string
	byNm  map[string]Gateway
	hooks map[string]WebhookSource
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byNm: map[string]Gateway{}, hooks: map[string]WebhookSource{}}
}

// Add registers a gateway and its webhook source.
func (r *Registry) Add(g Gateway, hook WebhookSource) {
	r.byNm[g.Name()] = g
	if hook != nil {
		r.hooks[g.Name()] = hook
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

// Get returns a gateway by name.
func (r *Registry) Get(name string) (Gateway, bool) {
	g, ok := r.byNm[name]
	return g, ok
}

// Default returns the gateway for new requests.
func (r *Registry) Default() Gateway { return r.byNm[r.def] }

// Names lists configured gateways, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byNm))
	for n := range r.byNm {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Webhooks lists the webhook sources to mount.
func (r *Registry) Webhooks() []WebhookSource {
	out := make([]WebhookSource, 0, len(r.hooks))
	for _, n := range r.Names() {
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
