// Package oauthprov is a Provider that obtains access tokens via the
// OAuth 2.0 client credentials flow. The token is cached and eagerly
// refreshed when its remaining lifetime falls below MinTTL.
package oauthprov

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// Provider is the oauth client-credentials-backed provider.
type Provider struct {
	name   string
	minTTL time.Duration
	cfg    *clientcredentials.Config

	mu    sync.Mutex
	token *oauth2.Token
}

type rawConfig struct {
	Type         string `json:"type"`
	Name         string `json:"name,omitempty"`
	Endpoint     string `json:"endpoint"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	MinTTL       string `json:"min_ttl,omitempty"`
}

// New decodes raw and returns a configured provider. No network call
// is made at construction time; the first Get fetches the initial
// token.
func New(raw json.RawMessage) (*Provider, error) {
	var c rawConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("oauthprov: %w", err)
	}
	if c.Endpoint == "" {
		return nil, fmt.Errorf("oauthprov: \"endpoint\" is required")
	}
	if c.ClientID == "" {
		return nil, fmt.Errorf("oauthprov: \"client_id\" is required")
	}
	if c.ClientSecret == "" {
		return nil, fmt.Errorf("oauthprov: \"client_secret\" is required")
	}
	name := c.Name
	if name == "" {
		name = "token"
	}
	var minTTL time.Duration
	if c.MinTTL != "" {
		d, err := time.ParseDuration(c.MinTTL)
		if err != nil {
			return nil, fmt.Errorf("oauthprov: parse min_ttl: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("oauthprov: min_ttl must be non-negative")
		}
		minTTL = d
	}
	return &Provider{
		name:   name,
		minTTL: minTTL,
		cfg: &clientcredentials.Config{
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret,
			TokenURL:     c.Endpoint,
		},
	}, nil
}

// Type returns "oauth".
func (*Provider) Type() string { return "oauth" }

// Has reports whether name matches the configured token name.
func (p *Provider) Has(name string) bool { return name == p.name }

// Get returns the current access token, fetching a new one if the
// cached token is missing or its remaining lifetime is below MinTTL.
func (p *Provider) Get(ctx context.Context, name string) (string, error) {
	if name != p.name {
		return "", fmt.Errorf("oauthprov: name %q not claimed", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fresh() {
		return p.token.AccessToken, nil
	}
	tok, err := p.cfg.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("oauthprov: fetch token: %w", err)
	}
	p.token = tok
	return tok.AccessToken, nil
}

// fresh reports whether the cached token is present and has more
// than MinTTL of life left. A token with no Expiry is treated as
// always fresh.
func (p *Provider) fresh() bool {
	if p.token == nil || p.token.AccessToken == "" {
		return false
	}
	if p.token.Expiry.IsZero() {
		return true
	}
	return time.Until(p.token.Expiry) > p.minTTL
}
