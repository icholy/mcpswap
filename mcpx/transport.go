// Package mcpx holds helpers used by mcproxy that augment the official
// MCP Go SDK: transport construction, name/URI prefix surgery, etc.
package mcpx

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TransportConfig is the resolved (template-free) config consumed by
// BuildTransport. It mirrors the fields of config.ResolvedClientConfig
// without taking a dependency on that package.
type TransportConfig struct {
	// Transport selects the upstream transport: "stdio", "http"
	// (alias: "streamable"), or "sse".
	Transport string

	// stdio fields
	Command string
	Args    []string
	Env     map[string]string

	// http / sse fields
	URL     string
	Headers http.Header
}

// BuildTransport returns the right mcp.Transport for r.
func BuildTransport(r TransportConfig) (mcp.Transport, error) {
	switch r.Transport {
	case "stdio":
		cmd := exec.Command(r.Command, r.Args...)
		if len(r.Env) > 0 {
			cmd.Env = append(cmd.Env, os.Environ()...)
			for k, v := range r.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http", "streamable":
		return &mcp.StreamableClientTransport{
			Endpoint:   r.URL,
			HTTPClient: clientWithHeaders(r.Headers),
		}, nil
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   r.URL,
			HTTPClient: clientWithHeaders(r.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", r.Transport)
	}
}

// clientWithHeaders returns an http.Client that adds h to every
// outgoing request. If h is empty, returns http.DefaultClient.
func clientWithHeaders(h http.Header) *http.Client {
	if len(h) == 0 {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &headerTransport{base: http.DefaultTransport, headers: h},
	}
}

// headerTransport is an http.RoundTripper that adds a fixed set of
// headers to every request before delegating to base.
type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, vs := range t.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}
