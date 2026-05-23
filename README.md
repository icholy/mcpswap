# mcproxy

A single-upstream MCP adapter. It fronts one upstream MCP server
(stdio, SSE, or streamable-HTTP) and exposes it over an HTTP endpoint,
forwarding tools, prompts, and resources through unchanged. The active
upstream session can be hot-swapped at runtime, which lets an embedder
rotate credentials (e.g. refresh an OAuth token) without dropping
in-flight requests.

`mcproxy` is primarily a library; the bundled `cmd/mcproxy` is a thin
static binary that connects once and serves.

## Library

```go
up := mcproxy.NewUpstream(slog.Default())
if err := up.Swap(ctx, mcproxy.TransportConfig{
    Transport: "streamable",
    URL:       "https://api.example.com/mcp/",
    Headers:   http.Header{"Authorization": {"Bearer " + token}},
}); err != nil {
    return err
}

pr, err := mcproxy.NewProxy(up, "streamable")
if err != nil {
    return err
}
http.ListenAndServe(":8080", pr)
```

`Upstream.Swap` connects a new session and atomically replaces the
active one, closing the previous session in the background. On failure
the active session is left untouched, so callers may retry or keep
serving on the old session. To rotate credentials, build a fresh
`TransportConfig` and call `Swap` again — the mechanism is identical
across transports. `mcproxy` ships no rotation policy: when and how you
re-`Swap` is up to you.

## Command

```
mcproxy -config mcproxy.json
```

The command connects once at startup and serves. It does not rotate
credentials.

### Config

```json
{
  "proxy":    { "addr": ":8080", "path": "/", "transport": "streamable" },
  "upstream": { "transport": "streamable", "url": "https://api.example.com/mcp/", "headers": { "Authorization": "Bearer ..." } }
}
```

- `proxy.addr`: listen address.
- `proxy.path`: mount path for the MCP HTTP handler. Defaults to `/`.
- `proxy.transport`: client-facing transport, `streamable` (default) or `sse`.
- `upstream`: the single upstream MCP (see below).

### Upstream transports

**stdio**

```json
"upstream": {
  "transport": "stdio",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
  "env": { "LOG_LEVEL": "info" }
}
```

**streamable-HTTP**

```json
"upstream": {
  "transport": "streamable",
  "url": "https://api.githubcopilot.com/mcp/",
  "headers": { "Authorization": "Bearer ..." }
}
```

**SSE**

```json
"upstream": {
  "transport": "sse",
  "url": "https://mcp.example.com/sse",
  "headers": { "X-API-Key": "..." }
}
```
