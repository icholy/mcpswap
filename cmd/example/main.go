package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/icholy/mcpswap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fileConfig is the example's on-disk config: where to listen, the
// client-facing transport, and the streamable-HTTP upstream to proxy.
type fileConfig struct {
	Addr      string            `json:"addr"`
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

func main() {
	configPath := flag.String("config", "mcpswap.json", "path to the config file")
	swapInterval := flag.Duration("swap-interval", 30*time.Second, "interval between upstream session swaps")
	flag.Parse()

	if err := run(*configPath, *swapInterval); err != nil {
		slog.Error("exited with error", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, swapInterval time.Duration) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var up mcpswap.Upstream
	if err := swap(ctx, &up, cfg); err != nil {
		slog.Warn("initial upstream connect failed; serving offline until it recovers", "err", err)
	}
	go swapLoop(ctx, &up, cfg, swapInterval)

	srv := mcp.NewServer(&mcp.Implementation{Name: "mcpswap", Version: "0.1.0"}, &mcp.ServerOptions{
		HasTools:     true,
		HasPrompts:   true,
		HasResources: true,
	})
	srv.AddReceivingMiddleware(up.Dispatch)

	var handler http.Handler
	switch cfg.Transport {
	case "", "streamable":
		handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	case "sse":
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	default:
		return fmt.Errorf("unknown transport %q", cfg.Transport)
	}

	httpSrv := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("http: %w", err)
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		up.Close()
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	up.Close()
	return <-errs
}

// swap connects a fresh upstream session. A new transport is built each
// call since Swap consumes it.
func swap(ctx context.Context, up *mcpswap.Upstream, cfg *fileConfig) error {
	swapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return up.Swap(swapCtx, upstreamTransport(cfg))
}

// swapLoop re-swaps the upstream session every interval until ctx is done.
func swapLoop(ctx context.Context, up *mcpswap.Upstream, cfg *fileConfig, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := swap(ctx, up, cfg); err != nil {
				slog.Warn("upstream swap failed", "err", err)
			}
		}
	}
}

// upstreamTransport builds a streamable-HTTP transport that adds the
// configured headers to every request.
func upstreamTransport(cfg *fileConfig) mcp.Transport {
	client := http.DefaultClient
	if len(cfg.Headers) > 0 {
		client = &http.Client{Transport: headerTransport(cfg.Headers)}
	}
	return &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client}
}

// headerTransport adds a fixed set of headers to every request.
type headerTransport map[string]string

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func loadConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	return &cfg, nil
}
