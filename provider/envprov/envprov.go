// Package envprov is a Provider that reads values from process environment
// variables. Any name resolves to its same-named environment variable;
// a missing variable is reported as an error at resolve time.
package envprov

import (
	"context"
	"fmt"
	"os"
)

// Provider is the env-backed provider.
type Provider struct{}

// New returns the env Provider.
func New() *Provider { return &Provider{} }

// Type returns "env".
func (*Provider) Type() string { return "env" }

// Has reports whether $name is set in the environment.
func (*Provider) Has(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

// Get reads $name from the process environment.
func (*Provider) Get(_ context.Context, name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("envprov: $%s is not set", name)
	}
	return v, nil
}
