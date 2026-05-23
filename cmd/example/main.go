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

// fileConfig is the on-disk config: one proxy block and one upstream.
type fileConfig struct {
	Proxy struct {
		Addr      string `json:"addr"`
		Path      string `json:"path"`
		Transport string `json:"transport"`
	} `json:"proxy"`
	Upstream struct {
		Transport string            `json:"transport"`
		Command   string            `json:"command,omitempty"`
		Args      []string          `json:"args,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		URL       string            `json:"url,omitempty"`
		Headers   map[string]string `json:"headers,omitempty"`
	} `json:"upstream"`
}

func main() {
	configPath := flag.String("config", "mcproxy.json", "path to the proxy config file")
	swapInterval := flag.Duration("swap-interval", 30*time.Second, "interval between upstream session swaps")
	flag.Parse()

	if err := run(*configPath, *swapInterval); err != nil {
		slog.Error("mcproxy exited with error", "err", err)
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

	up := mcpswap.NewUpstream(slog.Default())
	if err := swap(ctx, up, cfg); err != nil {
		slog.Warn("initial upstream connect failed; serving offline until it recovers", "err", err)
	}
	go swapLoop(ctx, up, cfg, swapInterval)

	mcpSrv := mcp.NewServer(&mcp.Implementation{
		Name:    "mcproxy",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		// We have no statically-registered tools/prompts/resources;
		// HasXXX makes the SDK advertise the capability anyway.
		HasTools:     true,
		HasPrompts:   true,
		HasResources: true,
	})
	mcpSrv.AddReceivingMiddleware(up.Dispatch)

	var handler http.Handler
	switch cfg.Proxy.Transport {
	case "", "streamable":
		handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)
	case "sse":
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)
	default:
		return fmt.Errorf("unknown proxy transport %q", cfg.Proxy.Transport)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Proxy.Path, handler)
	srv := &http.Server{
		Addr:              cfg.Proxy.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		slog.Info("mcproxy listening", "addr", cfg.Proxy.Addr, "path", cfg.Proxy.Path)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
	up.Close()
	return <-errs
}

// swap builds a fresh transport from cfg and swaps the upstream session.
// A new transport is built each call since Swap consumes it.
func swap(ctx context.Context, up *mcpswap.Upstream, cfg *fileConfig) error {
	transport, err := mcpswap.BuildTransport(upstreamConfig(cfg))
	if err != nil {
		return fmt.Errorf("build transport: %w", err)
	}
	swapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return up.Swap(swapCtx, transport)
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

func loadConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Proxy.Path == "" {
		cfg.Proxy.Path = "/"
	}
	if cfg.Proxy.Transport == "" {
		cfg.Proxy.Transport = "streamable"
	}
	if cfg.Upstream.Transport == "" {
		return nil, fmt.Errorf("upstream.transport is required")
	}
	return &cfg, nil
}

func upstreamConfig(cfg *fileConfig) mcpswap.TransportConfig {
	tc := mcpswap.TransportConfig{
		Transport: cfg.Upstream.Transport,
		Command:   cfg.Upstream.Command,
		Args:      cfg.Upstream.Args,
		Env:       cfg.Upstream.Env,
		URL:       cfg.Upstream.URL,
	}
	if len(cfg.Upstream.Headers) > 0 {
		tc.Headers = http.Header{}
		for k, v := range cfg.Upstream.Headers {
			tc.Headers.Set(k, v)
		}
	}
	return tc
}
