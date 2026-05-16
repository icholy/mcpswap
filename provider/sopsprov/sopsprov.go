// Package sopsprov is a Provider that reads values from a SOPS-encrypted
// file by shelling out to the `sops` CLI. The file is decrypted once at
// startup; values do not change for the life of the process.
package sopsprov

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Provider is the sops-backed provider.
type Provider struct {
	path   string
	values map[string]any
}

type rawConfig struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Bin  string `json:"bin,omitempty"`
}

// New decodes raw and decrypts the values file once.
func New(raw json.RawMessage) (*Provider, error) {
	var c rawConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("sopsprov: %w", err)
	}
	if c.Path == "" {
		return nil, fmt.Errorf("sopsprov: \"path\" is required")
	}
	bin := c.Bin
	if bin == "" {
		bin = "sops"
	}
	values, err := decrypt(context.Background(), bin, c.Path)
	if err != nil {
		return nil, fmt.Errorf("sopsprov: %w", err)
	}
	return &Provider{path: c.Path, values: values}, nil
}

// Type returns "sops".
func (p *Provider) Type() string { return "sops" }

// Has reports whether name was present in the decrypted file.
func (p *Provider) Has(name string) bool {
	_, ok := p.values[name]
	return ok
}

// Get returns the value of name. Errors if the decrypted value isn't a
// JSON string.
func (p *Provider) Get(_ context.Context, name string) (string, error) {
	v, ok := p.values[name]
	if !ok {
		return "", fmt.Errorf("sopsprov: name %q not found in %s", name, p.path)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("sopsprov: name %q in %s has non-string value (%T)", name, p.path, v)
	}
	return s, nil
}

// decrypt runs `sops decrypt --output-type json <path>` and parses the
// result as a top-level JSON object.
func decrypt(ctx context.Context, bin, path string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "decrypt", "--output-type", "json", path)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("sops decrypt %s: %w: %s", path, err, ee.Stderr)
		}
		return nil, fmt.Errorf("sops decrypt %s: %w", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse decrypted %s: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}
