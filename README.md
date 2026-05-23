# mcpswap

A single-upstream MCP adapter. It fronts one upstream MCP server
(stdio, SSE, or streamable-HTTP) and exposes it over an HTTP endpoint,
forwarding tools, prompts, and resources through unchanged. The active
upstream session can be hot-swapped at runtime, which lets an embedder
rotate credentials (e.g. refresh an OAuth token) without dropping
in-flight requests.

## Usage

```go
up := mcpswap.NewUpstream(slog.Default())
transport, err := mcpswap.BuildTransport(mcpswap.TransportConfig{
    Transport: "streamable",
    URL:       "https://api.example.com/mcp/",
    Headers:   http.Header{"Authorization": {"Bearer " + token}},
})
if err != nil {
    return err
}
if err := up.Swap(ctx, transport); err != nil {
    return err
}

srv := mcp.NewServer(&mcp.Implementation{Name: "mcpswap", Version: "0.1.0"}, &mcp.ServerOptions{
    HasTools:     true,
    HasPrompts:   true,
    HasResources: true,
})
srv.AddReceivingMiddleware(up.Dispatch)
http.ListenAndServe(":8080", mcp.NewStreamableHTTPHandler(
    func(*http.Request) *mcp.Server { return srv }, nil,
))
```

`Upstream.Dispatch` is an `mcp` receiving middleware that forwards
list/call/get/read requests to the active upstream session; add it to
your own `mcp.Server`.

`Upstream.Swap` connects a new session and atomically replaces the
active one, closing the previous session in the background. On failure
the active session is left untouched, so callers may retry or keep
serving on the old session. To rotate credentials, build a fresh
transport and call `Swap` again — the mechanism is identical across
transports. `mcpswap` ships no rotation policy: when and how you
re-`Swap` is up to you.

## Transports

`BuildTransport` builds the upstream transport from a `TransportConfig`:

- **stdio** — `Command`, `Args`, `Env`.
- **streamable-HTTP** (`"streamable"`) — `URL`, `Headers`.
- **SSE** (`"sse"`) — `URL`, `Headers`.
