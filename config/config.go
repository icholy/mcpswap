package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/icholy/mcproxy/mcpx"
	"github.com/icholy/mcproxy/templates"
)

// Config is the loaded proxy config.
type Config struct {
	Proxy     ProxyConfig
	Servers   map[string]*ClientConfig
	Providers []ProviderEntry
}

// ProxyConfig holds top-level server settings.
type ProxyConfig struct {
	// Addr is the address the proxy's HTTP server listens on, e.g. ":8080".
	Addr string `json:"addr"`
	// Path is the URL path the MCP HTTP handler mounts at. Defaults to "/".
	Path string `json:"path"`
	// Transport selects the HTTP transport type: "streamable" (default) or "sse".
	Transport string `json:"transport"`
}

// ProviderEntry is one provider's config. Type names a provider
// implementation; Raw is the type-specific payload that the matching
// provider package decodes itself.
type ProviderEntry struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// ClientConfig is one upstream MCP's templated config.
type ClientConfig struct {
	// References is every (type, name) pair referenced by this client's
	// templated fields, deduped. Populated at parse time.
	References []templates.Reference

	// Transport is the upstream's transport type: "stdio", "sse", or "http".
	Transport string

	// stdio fields
	Command templates.TemplateString
	Args    []templates.TemplateString
	Env     map[string]templates.TemplateString

	// http / sse fields
	URL     templates.TemplateString
	Headers map[string]templates.TemplateString
}

// rawConfig matches the on-disk JSON shape.
type rawConfig struct {
	Proxy     ProxyConfig                 `json:"proxy"`
	Servers   map[string]*rawClientConfig `json:"servers"`
	Providers []rawProviderEntry          `json:"providers"`
}

type rawProviderEntry struct {
	Type string `json:"type"`
	// Raw is captured by re-marshaling the original provider object so
	// each provider can decode its own typed payload.
	rest json.RawMessage
}

func (e *rawProviderEntry) UnmarshalJSON(data []byte) error {
	var typed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	e.Type = typed.Type
	e.rest = append([]byte(nil), data...)
	return nil
}

type rawClientConfig struct {
	Transport string                              `json:"transport"`
	Command   *templates.TemplateString           `json:"command,omitempty"`
	Args      []templates.TemplateString          `json:"args,omitempty"`
	Env       map[string]templates.TemplateString `json:"env,omitempty"`
	URL       *templates.TemplateString           `json:"url,omitempty"`
	Headers   map[string]templates.TemplateString `json:"headers,omitempty"`
}

// Load reads and parses path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg := &Config{
		Proxy:   raw.Proxy,
		Servers: map[string]*ClientConfig{},
	}
	if cfg.Proxy.Path == "" {
		cfg.Proxy.Path = "/"
	}
	if cfg.Proxy.Transport == "" {
		cfg.Proxy.Transport = "streamable"
	}
	for name, rcc := range raw.Servers {
		if err := validateServerName(name); err != nil {
			return nil, err
		}
		cc, err := buildClientConfig(rcc)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		cfg.Servers[name] = cc
	}
	for _, rp := range raw.Providers {
		if rp.Type == "" {
			return nil, fmt.Errorf("provider entry missing \"type\"")
		}
		cfg.Providers = append(cfg.Providers, ProviderEntry{
			Type: rp.Type,
			Raw:  rp.rest,
		})
	}
	return cfg, nil
}

// validateServerName enforces the character set we permit for upstream
// names. Names appear as a prefix in tool/prompt names, so they must
// fit the SDK's tool-name rules; we tighten further to keep the
// "<upstream>__<original>" split unambiguous.
func validateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name cannot be empty")
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("server name %q: only [A-Za-z0-9.-] allowed", name)
		}
	}
	return nil
}

func buildClientConfig(rcc *rawClientConfig) (*ClientConfig, error) {
	if rcc == nil {
		return nil, fmt.Errorf("missing config")
	}
	cc := &ClientConfig{Transport: rcc.Transport}
	switch rcc.Transport {
	case "stdio":
		if rcc.Command == nil {
			return nil, fmt.Errorf("stdio transport requires \"command\"")
		}
		cc.Command = *rcc.Command
		cc.References = templates.MergeReferences(cc.References, cc.Command.References())
		cc.Args = rcc.Args
		for _, a := range cc.Args {
			cc.References = templates.MergeReferences(cc.References, a.References())
		}
		cc.Env = rcc.Env
		for _, v := range cc.Env {
			cc.References = templates.MergeReferences(cc.References, v.References())
		}
	case "sse", "http", "streamable":
		if rcc.URL == nil {
			return nil, fmt.Errorf("%s transport requires \"url\"", rcc.Transport)
		}
		cc.URL = *rcc.URL
		cc.References = templates.MergeReferences(cc.References, cc.URL.References())
		cc.Headers = rcc.Headers
		for _, v := range cc.Headers {
			cc.References = templates.MergeReferences(cc.References, v.References())
		}
	case "":
		return nil, fmt.Errorf("missing \"transport\"")
	default:
		return nil, fmt.Errorf("unknown transport %q", rcc.Transport)
	}
	return cc, nil
}

// Resolve renders every template by calling r.Get for each referenced
// key, returning the transport config ready for mcpx.BuildTransport.
func (c *ClientConfig) Resolve(ctx context.Context, r templates.Resolver) (*mcpx.TransportConfig, error) {
	out := &mcpx.TransportConfig{Transport: c.Transport}
	switch c.Transport {
	case "stdio":
		v, err := c.Command.Render(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Command = v
		out.Args = make([]string, len(c.Args))
		for i, a := range c.Args {
			v, err := a.Render(ctx, r)
			if err != nil {
				return nil, err
			}
			out.Args[i] = v
		}
		if len(c.Env) > 0 {
			out.Env = make(map[string]string, len(c.Env))
			for k, t := range c.Env {
				v, err := t.Render(ctx, r)
				if err != nil {
					return nil, err
				}
				out.Env[k] = v
			}
		}
	case "sse", "http", "streamable":
		v, err := c.URL.Render(ctx, r)
		if err != nil {
			return nil, err
		}
		out.URL = v
		if len(c.Headers) > 0 {
			out.Headers = make(http.Header, len(c.Headers))
			for k, t := range c.Headers {
				v, err := t.Render(ctx, r)
				if err != nil {
					return nil, err
				}
				out.Headers.Set(k, v)
			}
		}
	}
	return out, nil
}
