package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/icholy/mcproxy/config"
	"github.com/icholy/mcproxy/mcpx"
	"github.com/icholy/mcproxy/provider"
)

// sessionState is the unit atomically swapped in current. After each
// open attempt (initial or rotation), the result is stored here:
// session set on success, err set on failure. Nil pointer = the
// initial open hasn't completed yet.
type sessionState struct {
	session *mcp.ClientSession
	err     error
}

// Upstream owns one configured upstream's lifecycle. It subscribes to
// the Bus for cfg.References, opens the initial session via cfg.Resolve
// → mcp.Client.Connect, and reopens on rotation events — atomically
// swapping the active session.
type Upstream struct {
	name      string
	cfg       *config.ClientConfig
	providers provider.Providers
	bus       *provider.Bus
	logger    *slog.Logger

	swapMu  sync.Mutex
	current atomic.Pointer[sessionState]
}

// NewUpstream creates a not-yet-open Upstream. Run drives its lifecycle.
func NewUpstream(name string, cfg *config.ClientConfig, p provider.Providers, bus *provider.Bus, logger *slog.Logger) *Upstream {
	if logger == nil {
		logger = slog.Default()
	}
	return &Upstream{
		name:      name,
		cfg:       cfg,
		providers: p,
		bus:       bus,
		logger:    logger,
	}
}

// Name returns the prefix used in the aggregate.
func (u *Upstream) Name() string { return u.name }

// Session returns the currently-active session, or a non-nil error if
// the Upstream is offline.
func (u *Upstream) Session() (*mcp.ClientSession, error) {
	st := u.current.Load()
	if st == nil {
		return nil, fmt.Errorf("upstream %q: not yet connected", u.name)
	}
	if st.err != nil {
		return nil, fmt.Errorf("upstream %q: offline: %w", u.name, st.err)
	}
	return st.session, nil
}

// Run drives the upstream's lifecycle until ctx is canceled.
func (u *Upstream) Run(ctx context.Context) error {
	keys := make([]provider.Key, len(u.cfg.References))
	for i, r := range u.cfg.References {
		keys[i] = provider.Key{Type: r.Type, Name: r.Name}
	}
	events := u.bus.Subscribe(ctx, keys)
	u.reopen(ctx)
	for {
		select {
		case <-ctx.Done():
			st := u.current.Load()
			if st != nil && st.session != nil {
				go func(s *mcp.ClientSession) {
					_ = s.Close()
				}(st.session)
			}
			return nil
		case _, ok := <-events:
			if !ok {
				continue
			}
			u.reopen(ctx)
		}
	}
}

// reopen resolves a fresh client config, opens a new session, and on
// success atomically swaps it into u.current. The previously-active
// session is closed in the background.
func (u *Upstream) reopen(ctx context.Context) {
	u.swapMu.Lock()
	defer u.swapMu.Unlock()

	prev := u.current.Load()

	resolved, err := u.cfg.Resolve(ctx, u.providers)
	if err != nil {
		u.markOffline(prev, fmt.Errorf("resolve: %w", err))
		return
	}
	transport, err := mcpx.BuildTransport(*resolved)
	if err != nil {
		u.markOffline(prev, fmt.Errorf("build transport: %w", err))
		return
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcproxy",
		Version: "0.1.0",
	}, nil)
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := client.Connect(openCtx, transport, nil)
	if err != nil {
		u.markOffline(prev, fmt.Errorf("connect: %w", err))
		return
	}
	u.current.Store(&sessionState{session: session})
	if prev != nil && prev.session != nil {
		go func(s *mcp.ClientSession) {
			_ = s.Close()
		}(prev.session)
	}
	u.logger.Info("upstream session opened", "upstream", u.name)
}

func (u *Upstream) markOffline(prev *sessionState, err error) {
	u.current.Store(&sessionState{err: err})
	if prev != nil && prev.session != nil {
		go func(s *mcp.ClientSession) {
			_ = s.Close()
		}(prev.session)
	}
	u.logger.Warn("upstream offline", "upstream", u.name, "err", err)
}
