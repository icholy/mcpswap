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
	srv.AddReceivingMiddleware(p.dispatch)
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

// dispatch forwards list/call/get/read methods to the active
// upstream session. The initialize result is rewritten to advertise the
// upstream's real capabilities. Everything else (e.g. ping) falls
// through to the SDK's default handler.
func (p *Proxy) dispatch(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch r := req.(type) {
		case *mcp.ServerRequest[*mcp.InitializeParams]:
			// Use the capabilities reported by the upstream
			res, err := next(ctx, method, req)
			if init, ok := res.(*mcp.InitializeResult); ok {
				if sess, err := p.upstream.Session(); err == nil {
					if up := sess.InitializeResult(); up != nil {
						init.Capabilities = up.Capabilities
						init.Instructions = up.Instructions
					}
				}
			}
			return res, err
		case *mcp.ListToolsRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.ListTools(ctx, r.Params)
		case *mcp.CallToolRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.CallTool(ctx, &mcp.CallToolParams{
				Meta:      r.Params.Meta,
				Name:      r.Params.Name,
				Arguments: r.Params.Arguments,
			})
		case *mcp.ListPromptsRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.ListPrompts(ctx, r.Params)
		case *mcp.GetPromptRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.GetPrompt(ctx, r.Params)
		case *mcp.ListResourcesRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.ListResources(ctx, r.Params)
		case *mcp.ListResourceTemplatesRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.ListResourceTemplates(ctx, r.Params)
		case *mcp.ReadResourceRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.ReadResource(ctx, r.Params)
		case *mcp.CompleteRequest:
			sess, err := p.upstream.Session()
			if err != nil {
				return nil, err
			}
			return sess.Complete(ctx, r.Params)
		}
		return next(ctx, method, req)
	}
}
