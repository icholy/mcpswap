# Hot-swap MCP proxy — design

Aggregate multiple upstream MCP servers (stdio / SSE / streamable-http)
into **a single logical MCP** exposed on one HTTP endpoint. Tools,
prompts, resources, and resource templates from each upstream are
prefixed with the upstream's name so they don't collide; the proxy
answers `ListTools` etc. by fanning out and aggregating, and
`CallTool` etc. by stripping the prefix and dispatching to the owning
upstream. Resolve credentials via pluggable providers using
`${var:KEY}` template syntax. Variable names live in a single global
namespace; each provider declares which keys it owns, and the proxy
refuses to start if two providers claim the same key. When a provider
signals rotation, open a fresh upstream client, wait for it to be
ready, then atomically swap that upstream in the proxy's roster.
In-flight calls drain on the old client; new-open failures take that
upstream offline (its prefixed tools error) without affecting others.

## Interfaces

### `provider.Bus`

A single server-wide bus carries rotation events. Providers Publish
to it when they observe a key change; consumers (the Proxy) Subscribe
to the keys they care about. Decouples publishers from subscribers;
the Provider interface stays small (no Watch).

```go
package provider

type Bus struct{ /* mutex, per-key subscriber list */ }

func NewBus() *Bus

// Publish announces that key has rotated to value. Non-blocking
// w.r.t. slow subscribers (full per-subscriber buffers coalesce —
// only the latest value matters).
func (b *Bus) Publish(key, value string)

// Subscribe returns a channel that emits Events for any of keys.
// Channel closes when ctx is canceled. Late subscribers do not see
// prior publishes (no replay).
func (b *Bus) Subscribe(ctx context.Context, keys []string) <-chan Event

type Event struct{ Key, Value string }
```

### `provider.Provider`

Resolves named values from some source and declares which keys it
owns. Providers that can rotate are given the `*Bus` at construction
and Publish when they observe changes; the Provider interface itself
only needs `Keys` and `Get`.

```go
package provider

type Provider interface {
    // Keys returns the variable names this provider currently
    // claims. Two providers claiming the same key is a startup error
    // (caught by NewAggregateProvider). key arguments to Get are
    // guaranteed to be one of those returned by Keys().
    Keys() []string

    // Get returns the current value of key. Side-effect-free; safe
    // to call concurrently and repeatedly.
    Get(ctx context.Context, key string) (string, error)
}

// AggregateProvider composes several Providers into one. Construction
// fails if any two children claim the same key. Get dispatches to
// the owning child by key. AggregateProvider itself implements
// Provider, so the rest of the system only sees a single Provider.
type AggregateProvider struct{ /* key -> child index, []Provider */ }

func NewAggregateProvider(children ...Provider) (*AggregateProvider, error)
```

Initial child implementations: `env` (keys configured explicitly,
never publishes — no `*Bus` needed), `file` (watched file; takes a
`*Bus` and Publishes on file change).

### `config`

Owns the on-disk schema, the templated string parser, and the
resolution step that produces a snapshot ready for `proxy.Open`.
Templates appear inside any string field of an MCP config (env
values, header values, URL, args); a field can contain multiple
`${var:KEY}` substitutions mixed with literal text. The template
machinery is an internal detail — callers see only the `Keys` field
and `Resolve()` method on a `ClientConfig`.

**`config` does not import `provider`.** Resolution takes a small
local interface; provider configs are stored as deferred-parsed
raw bytes that each concrete provider package decodes itself. This
keeps the dependency direction one-way (`main` and provider sub-
packages import `config`; `config` imports nothing from us).

```go
package config

// Config is the loaded proxy config. Templated string fields are
// parsed but not yet rendered.
type Config struct {
    Proxy     ProxyConfig
    Servers   map[string]*ClientConfig
    Providers []ProviderEntry
}

func Load(path string) (*Config, error)

// ProviderEntry is one provider's config. Type names a provider
// implementation; Raw is the type-specific payload that the matching
// provider package decodes itself (e.g., {"path": "/secrets.json"}
// for the file provider). config.Load does not look inside Raw.
type ProviderEntry struct {
    Type string
    Raw  json.RawMessage
}

// ClientConfig is one upstream MCP's templated config.
type ClientConfig struct {
    // Keys is every variable name referenced by this client's
    // templated fields, deduped. Populated at parse time.
    Keys []string
    /* templated transport-specific fields */
}

// Resolver is the minimal contract ClientConfig.Resolve needs from
// the value source. provider.Provider satisfies it for free.
type Resolver interface {
    Get(ctx context.Context, key string) (string, error)
}

// Resolve renders every template by calling r.Get for each
// referenced key. Returns an error if Get errors for any key.
func (c *ClientConfig) Resolve(ctx context.Context, r Resolver) (*ResolvedClientConfig, error)

// ResolvedClientConfig is the fully-rendered config consumed by
// proxy.Open.
type ResolvedClientConfig struct{ /* concrete strings, no templates */ }
```

### `proxy`

Two public types: `Upstream` owns one slot's lifecycle (subscribe to
the Bus, hold the current `*mcp.ClientSession`, reopen on rotation);
`Proxy` is the single MCP server that fans out across a roster of
Upstreams behind one `http.Handler`.

```go
package proxy

// Upstream owns one configured upstream's lifecycle. It subscribes
// to the Bus for cfg.Keys, opens the initial mcp.ClientSession (via
// mcp.Client.Connect using the resolved transport config), and
// reopens on rotation events — atomically swapping the active
// session. Concurrent dispatch is lock-free; reopens are serialized.
type Upstream struct {
    /*
        name     string                  // immutable; prefix in aggregate
        cfg      *config.ClientConfig    // immutable
        provider provider.Provider       // for cfg.Resolve
        bus      *provider.Bus           // for Subscribe
        swapMu   sync.Mutex              // serializes reopens
        current  atomic.Pointer[sessionState]
    */
}

// sessionState is the unit atomically swapped in current. After each
// open attempt (initial or rotation), the result is stored here:
// session set on success, err set on failure. Nil pointer = the
// initial open hasn't completed yet.
//
//   type sessionState struct {
//       session *mcp.ClientSession   // nil iff err != nil
//       err     error                // nil iff session != nil
//   }

func NewUpstream(name string, cfg *config.ClientConfig, p provider.Provider, bus *provider.Bus) *Upstream

// Name returns the prefix used in the aggregate.
func (u *Upstream) Name() string

// Session returns the currently-active session. Returns a non-nil
// error if the Upstream is offline — initial open hasn't completed
// yet, initial open failed, or the most recent reopen failed. The
// error wraps the underlying cause so logs can distinguish them.
// Lock-free.
func (u *Upstream) Session() (*mcp.ClientSession, error)

// Run subscribes to bus.Subscribe(cfg.Keys), runs the initial open
// via cfg.Resolve(ctx, p) → mcp.Client.Connect, then loops on Events
// (coalesced through a small debounce window) by re-resolving and
// reopening. On a successful reopen the new session becomes current
// atomically and the old session's Close runs in a goroutine (the
// SDK drains in-flight requests inside Close, so no refcount). On a
// reopen failure the slot goes offline and the old is asynchronously
// closed. Blocks until ctx is canceled. Initial-open failures leave
// the slot offline but do not return an error from Run.
func (u *Upstream) Run(ctx context.Context) error

// Proxy is the single-endpoint MCP server. Holds a roster of
// Upstreams; constructs one mcp.Server + http.Handler that
// implements MCP callbacks by fanning out across the roster.
type Proxy struct {
    /*
        upstreams map[string]*Upstream  // keyed by Upstream.Name
        server    *mcp.Server
        handler   http.Handler          // SSE or streamable-http
    */
}

func New(cfg *config.Config, p provider.Provider, bus *provider.Bus) (*Proxy, error)

// ServeHTTP serves the aggregate MCP server. The SDK's HTTP handler
// dispatches downstream MCP calls into the Proxy's MCP callbacks,
// which fan out to upstreams.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)

// MCP server callbacks. ListTools fans out across the roster,
// prefixing each upstream's tool names with "<upstream.Name>__";
// CallTool strips the prefix and dispatches via the matching
// Upstream.Session(). Same pattern for prompts and resources.
// During aggregation, an Upstream whose Session() returns an error
// is logged and skipped (its tools are absent from the response).
// CallTool against an offline Upstream returns the wrapped error
// to the downstream caller.
//
// The Proxy never caches a session reference. Every fan-out and
// dispatch calls Upstream.Session() at the point of use, so an
// in-progress reopen is transparent — the next call lands on
// whichever session is current at that instant.
func (p *Proxy) ListTools(ctx context.Context) ([]mcp.Tool, error)
func (p *Proxy) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error)
// ... ListPrompts / GetPrompt / ListResources / ReadResource / ListResourceTemplates

// Run runs every Upstream's Run concurrently (one goroutine each)
// and blocks until ctx is canceled, then waits for them to return.
//
// Reopens are opaque to downstream: a credential rotation swaps the
// session but does not change the tool/prompt/resource set, so the
// Proxy emits no notifications of its own. List-change notifications
// come from the real upstreams and are forwarded as-is — when an
// upstream emits notifications/tools/list_changed (or the analogues),
// the Proxy re-emits the same notification on every active downstream
// session. (Notification re-emission is wired per upstream session
// in Upstream.Run, since a new session needs the handler re-attached.)
func (p *Proxy) Run(ctx context.Context) error
```

## Wiring (one paragraph)

`main` calls `config.Load`, creates a `*provider.Bus`, then iterates
`cfg.Providers`: switches on each entry's `Type` to call into the
matching provider sub-package's constructor (e.g., `envprov.New(raw)`,
`fileprov.New(raw, bus)`), which decodes the raw bytes into its own
typed config. Composes the constructed providers with
`provider.NewAggregateProvider(...)` — **fail to start on any key
collision** or on a `${var:KEY}` referenced by no provider. Calls
`proxy.New(cfg, agg, bus)` and uses the returned `*proxy.Proxy` as
the `http.Handler` of an `http.Server`. Runs `proxy.Run(ctx)` in a
goroutine alongside `http.Server.ListenAndServe` and waits on
SIGINT/SIGTERM to trigger graceful shutdown.
