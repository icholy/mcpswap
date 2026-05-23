package mcproxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Proxy is an HTTP MCP server that forwards every list/call request to
// the active upstream session. It holds no static registry of its own;
// tools, prompts, and resources are served live from the upstream.
type Proxy struct {
	upstream *Upstream
	server   *mcp.Server
	handler  http.Handler
}

// NewProxy builds a Proxy that serves u over the given client-facing
// transport: "streamable" (default) or "sse".
func NewProxy(u *Upstream, transport string) (*Proxy, error) {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "mcproxy",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		// We have no statically-registered tools/prompts/resources;
		// HasXXX makes the SDK advertise the capability anyway.
		HasTools:     true,
		HasPrompts:   true,
		HasResources: true,
	})
	p := &Proxy{upstream: u, server: srv}
	srv.AddReceivingMiddleware(p.dispatchMiddleware)
	switch transport {
	case "", "streamable":
		p.handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.server }, nil)
	case "sse":
		p.handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return p.server }, nil)
	default:
		return nil, fmt.Errorf("unknown proxy transport %q", transport)
	}
	return p, nil
}

// ServeHTTP serves the MCP server over HTTP.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.ServeHTTP(w, r)
}

// dispatchMiddleware forwards list/call/get/read methods to the active
// upstream session. Everything else (initialize, ping) falls through to
// the SDK's default handler.
func (p *Proxy) dispatchMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch r := req.(type) {
		case *mcp.ListToolsRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.ListTools(ctx, r.Params) })
		case *mcp.CallToolRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) {
				return s.CallTool(ctx, &mcp.CallToolParams{
					Meta:      r.Params.Meta,
					Name:      r.Params.Name,
					Arguments: r.Params.Arguments,
				})
			})
		case *mcp.ListPromptsRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.ListPrompts(ctx, r.Params) })
		case *mcp.GetPromptRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.GetPrompt(ctx, r.Params) })
		case *mcp.ListResourcesRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.ListResources(ctx, r.Params) })
		case *mcp.ListResourceTemplatesRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.ListResourceTemplates(ctx, r.Params) })
		case *mcp.ReadResourceRequest:
			return forward(p, func(s *mcp.ClientSession) (mcp.Result, error) { return s.ReadResource(ctx, r.Params) })
		}
		return next(ctx, method, req)
	}
}

// forward resolves the active session and invokes call against it.
func forward(p *Proxy, call func(*mcp.ClientSession) (mcp.Result, error)) (mcp.Result, error) {
	sess, err := p.upstream.Session()
	if err != nil {
		return nil, err
	}
	return call(sess)
}
