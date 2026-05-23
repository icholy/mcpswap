package mcproxy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Upstream holds the currently-active session to the upstream MCP
// server. Swap connects a new session and atomically replaces the
// active one; readers see either the old or the new session, never a
// torn state. Upstream knows nothing about credentials or rotation —
// the caller decides when to Swap and with what config.
type Upstream struct {
	logger  *slog.Logger
	swapMu  sync.Mutex
	session atomic.Pointer[mcp.ClientSession]
}

// NewUpstream returns an Upstream with no active session. Call Swap to
// open one.
func NewUpstream(logger *slog.Logger) *Upstream {
	if logger == nil {
		logger = slog.Default()
	}
	return &Upstream{logger: logger}
}

// Session returns the active session, or an error if none is open.
func (u *Upstream) Session() (*mcp.ClientSession, error) {
	s := u.session.Load()
	if s == nil {
		return nil, fmt.Errorf("upstream: no active session")
	}
	return s, nil
}

// Swap connects a new session over transport and atomically makes it
// the active session, closing the previous one in the background. On
// failure the active session is left untouched and the error is
// returned, so callers may retry or keep serving on the old session.
//
// transport is consumed by a single connect attempt; pass a fresh
// transport on each call.
func (u *Upstream) Swap(ctx context.Context, transport mcp.Transport) error {
	u.swapMu.Lock()
	defer u.swapMu.Unlock()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcproxy", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	prev := u.session.Swap(session)
	closeInBackground(prev)
	u.logger.Info("upstream session opened")
	return nil
}

// Close closes the active session and clears it.
func (u *Upstream) Close() {
	u.swapMu.Lock()
	defer u.swapMu.Unlock()
	if s := u.session.Swap(nil); s != nil {
		_ = s.Close()
	}
}

func closeInBackground(s *mcp.ClientSession) {
	if s == nil {
		return
	}
	go func() { _ = s.Close() }()
}
