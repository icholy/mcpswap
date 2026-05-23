// Package mcproxy is a single-upstream MCP adapter. It fronts one
// upstream MCP server (stdio, SSE, or streamable-HTTP) and exposes it
// over an HTTP endpoint, atomically hot-swapping the upstream session
// on demand via Upstream.Swap. Credential/rotation policy lives with
// the caller: build a fresh TransportConfig and call Swap.
package mcproxy

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TransportConfig is a resolved, template-free upstream config.
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

// BuildTransport returns the right mcp.Transport for c.
func BuildTransport(c TransportConfig) (mcp.Transport, error) {
	switch c.Transport {
	case "stdio":
		cmd := exec.Command(c.Command, c.Args...)
		if len(c.Env) > 0 {
			cmd.Env = append(cmd.Env, os.Environ()...)
			for k, v := range c.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http", "streamable":
		return &mcp.StreamableClientTransport{
			Endpoint:   c.URL,
			HTTPClient: clientWithHeaders(c.Headers),
		}, nil
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   c.URL,
			HTTPClient: clientWithHeaders(c.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", c.Transport)
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
