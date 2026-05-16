// Package proxy implements the hot-swap MCP aggregating proxy described
// in DESIGN.md.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/icholy/mcproxy/config"
	"github.com/icholy/mcproxy/mcpx"
	"github.com/icholy/mcproxy/provider"
)

// Proxy is the single-endpoint MCP server. It owns a roster of
// Upstreams; every list/call request is dispatched live across the
// roster — nothing about the upstreams' tools/prompts/resources is
// cached.
type Proxy struct {
	logger    *slog.Logger
	upstreams map[string]*Upstream
	server    *mcp.Server
	handler   http.Handler
}

// New constructs a Proxy from cfg. It instantiates one Upstream per
// entry in cfg.Servers; their goroutines start when Run is called.
func New(cfg *config.Config, p provider.Providers, bus *provider.Bus) (*Proxy, error) {
	if err := validateRefs(cfg, p); err != nil {
		return nil, err
	}
	logger := slog.Default()
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
	pr := &Proxy{
		logger:    logger,
		upstreams: map[string]*Upstream{},
		server:    srv,
	}
	for name, sc := range cfg.Servers {
		pr.upstreams[name] = NewUpstream(name, sc, p, bus, logger)
	}
	srv.AddReceivingMiddleware(pr.dispatchMiddleware)
	switch cfg.Proxy.Transport {
	case "", "streamable":
		pr.handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return pr.server }, nil)
	case "sse":
		pr.handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return pr.server }, nil)
	default:
		return nil, fmt.Errorf("unknown proxy transport %q", cfg.Proxy.Transport)
	}
	return pr, nil
}

func validateRefs(cfg *config.Config, p provider.Providers) error {
	for name, sc := range cfg.Servers {
		for _, r := range sc.References {
			if !p.Has(r.Type, r.Name) {
				return fmt.Errorf("server %q references unknown ${%s:%s}", name, r.Type, r.Name)
			}
		}
	}
	return nil
}

// ServeHTTP serves the aggregate MCP server over HTTP.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.ServeHTTP(w, r)
}

// Run starts every Upstream's Run concurrently and blocks until ctx is
// canceled, then waits for them to return.
func (p *Proxy) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, u := range p.upstreams {
		wg.Go(func() {
			_ = u.Run(ctx)
		})
	}
	wg.Wait()
	return nil
}

// dispatchMiddleware intercepts list/call methods and fans them out
// across the roster. Anything else falls through to the SDK's default
// handler (which serves the empty static registry plus initialize/ping
// machinery).
func (p *Proxy) dispatchMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch r := req.(type) {
		case *mcp.ListToolsRequest:
			return p.listTools(ctx, r)
		case *mcp.CallToolRequest:
			return p.callTool(ctx, r)
		case *mcp.ListPromptsRequest:
			return p.listPrompts(ctx, r)
		case *mcp.GetPromptRequest:
			return p.getPrompt(ctx, r)
		case *mcp.ListResourcesRequest:
			return p.listResources(ctx, r)
		case *mcp.ListResourceTemplatesRequest:
			return p.listResourceTemplates(ctx, r)
		case *mcp.ReadResourceRequest:
			return p.readResource(ctx, r)
		}
		return next(ctx, method, req)
	}
}

// fanOut runs fn for every upstream concurrently. Each fn is given the
// upstream and its currently-active session. Upstreams whose Session()
// returns an error are logged and skipped.
func (p *Proxy) fanOut(ctx context.Context, fn func(u *Upstream, sess *mcp.ClientSession)) {
	var wg sync.WaitGroup
	for _, u := range p.upstreams {
		sess, err := u.Session()
		if err != nil {
			p.logger.Warn("upstream skipped during fan-out", "upstream", u.Name(), "err", err)
			continue
		}
		wg.Go(func() {
			fn(u, sess)
		})
	}
	wg.Wait()
}

// --- list handlers ---

func (p *Proxy) listTools(ctx context.Context, _ *mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	out := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
	var mu sync.Mutex
	p.fanOut(ctx, func(u *Upstream, sess *mcp.ClientSession) {
		if sess.InitializeResult().Capabilities.Tools == nil {
			return
		}
		res, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			p.logger.Warn("upstream ListTools failed", "upstream", u.Name(), "err", err)
			return
		}
		prefix := u.Name()
		mu.Lock()
		defer mu.Unlock()
		for _, t := range res.Tools {
			t.Title = withPrefixTitle(prefix, t.Title, t.Name)
			t.Name = mcpx.PrefixName(prefix, t.Name)
			out.Tools = append(out.Tools, t)
		}
	})
	return out, nil
}

func (p *Proxy) listPrompts(ctx context.Context, _ *mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	out := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
	var mu sync.Mutex
	p.fanOut(ctx, func(u *Upstream, sess *mcp.ClientSession) {
		if sess.InitializeResult().Capabilities.Prompts == nil {
			return
		}
		res, err := sess.ListPrompts(ctx, &mcp.ListPromptsParams{})
		if err != nil {
			p.logger.Warn("upstream ListPrompts failed", "upstream", u.Name(), "err", err)
			return
		}
		prefix := u.Name()
		mu.Lock()
		defer mu.Unlock()
		for _, pr := range res.Prompts {
			pr.Title = withPrefixTitle(prefix, pr.Title, pr.Name)
			pr.Name = mcpx.PrefixName(prefix, pr.Name)
			out.Prompts = append(out.Prompts, pr)
		}
	})
	return out, nil
}

func (p *Proxy) listResources(ctx context.Context, _ *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	out := &mcp.ListResourcesResult{Resources: []*mcp.Resource{}}
	var mu sync.Mutex
	p.fanOut(ctx, func(u *Upstream, sess *mcp.ClientSession) {
		if sess.InitializeResult().Capabilities.Resources == nil {
			return
		}
		res, err := sess.ListResources(ctx, &mcp.ListResourcesParams{})
		if err != nil {
			p.logger.Warn("upstream ListResources failed", "upstream", u.Name(), "err", err)
			return
		}
		prefix := u.Name()
		mu.Lock()
		defer mu.Unlock()
		for _, r := range res.Resources {
			newURI := mcpx.PrefixURI(prefix, r.URI)
			if newURI == "" {
				p.logger.Warn("upstream resource URI cannot be re-prefixed; skipping", "upstream", prefix, "uri", r.URI)
				continue
			}
			r.URI = newURI
			out.Resources = append(out.Resources, r)
		}
	})
	return out, nil
}

func (p *Proxy) listResourceTemplates(ctx context.Context, _ *mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	out := &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{}}
	var mu sync.Mutex
	p.fanOut(ctx, func(u *Upstream, sess *mcp.ClientSession) {
		if sess.InitializeResult().Capabilities.Resources == nil {
			return
		}
		res, err := sess.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
		if err != nil {
			p.logger.Warn("upstream ListResourceTemplates failed", "upstream", u.Name(), "err", err)
			return
		}
		prefix := u.Name()
		mu.Lock()
		defer mu.Unlock()
		for _, t := range res.ResourceTemplates {
			newURI := mcpx.PrefixURI(prefix, t.URITemplate)
			if newURI == "" {
				p.logger.Warn("upstream resource template URI cannot be re-prefixed; skipping", "upstream", prefix, "uri", t.URITemplate)
				continue
			}
			t.URITemplate = newURI
			out.ResourceTemplates = append(out.ResourceTemplates, t)
		}
	})
	return out, nil
}

// --- targeted dispatch ---

func (p *Proxy) callTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	upstream, name, ok := mcpx.SplitName(req.Params.Name)
	if !ok {
		return nil, fmt.Errorf("tool name %q missing %q upstream prefix", req.Params.Name, mcpx.NameSeparator)
	}
	sess, err := p.targetSession(upstream)
	if err != nil {
		return nil, err
	}
	params := &mcp.CallToolParams{
		Meta:      req.Params.Meta,
		Name:      name,
		Arguments: req.Params.Arguments,
	}
	return sess.CallTool(ctx, params)
}

func (p *Proxy) getPrompt(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	upstream, name, ok := mcpx.SplitName(req.Params.Name)
	if !ok {
		return nil, fmt.Errorf("prompt name %q missing %q upstream prefix", req.Params.Name, mcpx.NameSeparator)
	}
	sess, err := p.targetSession(upstream)
	if err != nil {
		return nil, err
	}
	params := &mcp.GetPromptParams{
		Meta:      req.Params.Meta,
		Name:      name,
		Arguments: req.Params.Arguments,
	}
	return sess.GetPrompt(ctx, params)
}

func (p *Proxy) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	upstream, uri, ok := mcpx.SplitURI(req.Params.URI)
	if !ok {
		return nil, fmt.Errorf("resource uri %q missing upstream prefix", req.Params.URI)
	}
	sess, err := p.targetSession(upstream)
	if err != nil {
		return nil, err
	}
	params := &mcp.ReadResourceParams{
		Meta: req.Params.Meta,
		URI:  uri,
	}
	return sess.ReadResource(ctx, params)
}

func (p *Proxy) targetSession(upstream string) (*mcp.ClientSession, error) {
	u, ok := p.upstreams[upstream]
	if !ok {
		return nil, fmt.Errorf("unknown upstream %q", upstream)
	}
	return u.Session()
}

// withPrefixTitle decorates the upstream's title for display.
func withPrefixTitle(upstream, title, fallback string) string {
	if title == "" {
		title = fallback
	}
	if title == "" {
		return ""
	}
	return upstream + ": " + title
}
