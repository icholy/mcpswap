package mcproxy

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Middleware returns an MCP receiving middleware that forwards
// list/call/get/read requests to u's active upstream session. The
// initialize result advertises only the capabilities the proxy can
// fulfill. Stateful methods (subscribe/unsubscribe/setLevel) are not
// proxied, and unrecognized methods fall through to the next handler.
//
// Add it to a server with (*mcp.Server).AddReceivingMiddleware.
func Middleware(u *Upstream) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch r := req.(type) {
			case *mcp.ServerRequest[*mcp.InitializeParams]:
				res, err := next(ctx, method, req)
				if init, ok := res.(*mcp.InitializeResult); ok {
					if sess, serr := u.Session(); serr == nil {
						if up := sess.InitializeResult(); up != nil && up.Capabilities != nil {
							// Advertise only what we actually proxy: drop subscribe,
							// listChanged, and logging — we forward no notifications
							// and keep no per-session state across hot-swaps.
							c := up.Capabilities
							caps := &mcp.ServerCapabilities{
								Completions:  c.Completions,
								Experimental: c.Experimental,
								Extensions:   c.Extensions,
							}
							if c.Tools != nil {
								caps.Tools = &mcp.ToolCapabilities{}
							}
							if c.Prompts != nil {
								caps.Prompts = &mcp.PromptCapabilities{}
							}
							if c.Resources != nil {
								caps.Resources = &mcp.ResourceCapabilities{}
							}
							init.Capabilities = caps
							init.Instructions = up.Instructions
						}
					}
				}
				return res, err
			case *mcp.ListToolsRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.ListTools(ctx, r.Params)
			case *mcp.CallToolRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.CallTool(ctx, &mcp.CallToolParams{
					Meta:      r.Params.Meta,
					Name:      r.Params.Name,
					Arguments: r.Params.Arguments,
				})
			case *mcp.ListPromptsRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.ListPrompts(ctx, r.Params)
			case *mcp.GetPromptRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.GetPrompt(ctx, r.Params)
			case *mcp.ListResourcesRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.ListResources(ctx, r.Params)
			case *mcp.ListResourceTemplatesRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.ListResourceTemplates(ctx, r.Params)
			case *mcp.ReadResourceRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.ReadResource(ctx, r.Params)
			case *mcp.CompleteRequest:
				sess, err := u.Session()
				if err != nil {
					return nil, err
				}
				return sess.Complete(ctx, r.Params)
			case *mcp.SubscribeRequest, *mcp.UnsubscribeRequest, *mcp.ServerRequest[*mcp.SetLoggingLevelParams]:
				// Stateful methods we deliberately don't proxy: resource
				// subscriptions and the logging level are per-session and would be
				// lost on a hot-swap, and we forward no notifications. We mask
				// these capabilities at initialize, so a conformant client won't
				// reach here; let the SDK reject/no-op them locally.
				return next(ctx, method, req)
			default:
				// initialized/ping/cancelled/progress and anything else are served
				// by the SDK; surface them for debugging.
				slog.Debug("unhandled method", "method", method)
				return next(ctx, method, req)
			}
		}
	}
}
