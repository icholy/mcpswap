package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/icholy/mcproxy/config"
	"github.com/icholy/mcproxy/provider"
	"github.com/icholy/mcproxy/provider/envprov"
	"github.com/icholy/mcproxy/provider/oauthprov"
	"github.com/icholy/mcproxy/provider/sopsprov"
	"github.com/icholy/mcproxy/proxy"
)

func main() {
	configPath := flag.String("config", "mcproxy.json", "path to the proxy config file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		slog.Error("mcproxy exited with error", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	bus := provider.NewBus()

	providers := provider.Providers{}
	for _, e := range cfg.Providers {
		p, err := buildProvider(e, bus)
		if err != nil {
			return fmt.Errorf("provider %q: %w", e.Type, err)
		}
		if prev, ok := providers[p.Type()]; ok {
			return fmt.Errorf("two providers claim type %q: %T and %T", p.Type(), prev, p)
		}
		providers[p.Type()] = p
	}

	pr, err := proxy.New(cfg, providers, bus)
	if err != nil {
		return fmt.Errorf("create proxy: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Proxy.Path, pr)
	srv := &http.Server{
		Addr:              cfg.Proxy.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 2)
	go func() {
		slog.Info("mcproxy listening", "addr", cfg.Proxy.Addr, "path", cfg.Proxy.Path)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("http: %w", err)
			return
		}
		errs <- nil
	}()
	go func() {
		errs <- pr.Run(ctx)
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}

func buildProvider(e config.ProviderEntry, bus *provider.Bus) (provider.Provider, error) {
	switch e.Type {
	case "env":
		return envprov.New(), nil
	case "sops":
		return sopsprov.New(e.Raw)
	case "oauth":
		return oauthprov.New(e.Raw)
	default:
		return nil, fmt.Errorf("unknown provider type %q", e.Type)
	}
}
