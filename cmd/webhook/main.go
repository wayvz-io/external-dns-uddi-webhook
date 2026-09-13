// Command webhook is an external-dns webhook provider for Infoblox Universal DDI.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/config"
	"github.com/wayvz-io/external-dns-uddi-webhook/internal/provider"
	"github.com/wayvz-io/external-dns-uddi-webhook/internal/server"
	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, env.ToMap(os.Environ()), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run wires config -> UDDI client -> provider -> server and blocks until ctx
// is cancelled. environ is the process environment as a map.
func run(ctx context.Context, environ map[string]string, out io.Writer) error {
	cfg, err := config.Load(environ)
	if err != nil {
		return err
	}
	logger := cfg.Logger(out)
	slog.SetDefault(logger)
	logger.Info("starting external-dns-uddi-webhook", "config", cfg)

	tags, err := cfg.Tags()
	if err != nil {
		return err
	}
	client := uddi.New(uddi.Options{
		APIKey:      cfg.PortalKey,
		PortalURL:   cfg.PortalURL,
		DefaultTags: tags,
		PageLimit:   cfg.PageLimit,
		RecordTypes: provider.SupportedTypes,
	})
	p, err := provider.New(ctx, provider.Config{
		Client:        client,
		View:          cfg.View,
		DomainFilter:  cfg.DomainFilter(),
		DryRun:        cfg.DryRun,
		DefaultTTL:    cfg.DefaultTTL,
		RecordComment: cfg.RecordComment,
		Tags:          tags,
		ZoneCacheTTL:  cfg.ZoneCacheTTL,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("initialise provider: %w", err)
	}
	return server.Run(ctx, server.Config{
		ServerAddr:   cfg.ServerAddr(),
		HealthzAddr:  cfg.HealthzAddr(),
		ReadTimeout:  cfg.ServerReadTimeout,
		WriteTimeout: cfg.ServerWriteTimeout,
		Logger:       logger,
	}, p)
}
