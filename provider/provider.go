package provider

import (
	"context"
	"fmt"
)

// Provider resolves named values from a single source. Its Type
// identifies its template prefix: "${env:FOO}" routes to the provider
// whose Type() returns "env".
type Provider interface {
	// Type returns the template prefix this provider claims (e.g. "env").
	Type() string

	// Has reports whether name is claimed by this provider.
	Has(name string) bool

	// Get returns the current value of name. Side-effect-free; safe to
	// call concurrently and repeatedly.
	Get(ctx context.Context, name string) (string, error)
}

// Providers is the set of registered providers, keyed by Type.
type Providers map[string]Provider

// Get dispatches to the provider for typ.
func (ps Providers) Get(ctx context.Context, typ, name string) (string, error) {
	p, ok := ps[typ]
	if !ok {
		return "", fmt.Errorf("no provider for type %q", typ)
	}
	return p.Get(ctx, name)
}

// Has reports whether (typ, name) is claimed by some provider.
func (ps Providers) Has(typ, name string) bool {
	p, ok := ps[typ]
	return ok && p.Has(name)
}
