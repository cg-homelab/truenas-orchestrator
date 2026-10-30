package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/caarlos0/env/v11"
	"golang.org/x/sync/errgroup"

	"github.com/cg-homelab/truenas-orchestrator/frontend"
	"github.com/cg-homelab/truenas-orchestrator/internal/server"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

// shutdownTimeout bounds how long in-flight requests get to finish after a shutdown signal.
const shutdownTimeout = 10 * time.Second

// serveConfig is what `serve` needs; github.com/caarlos0/env/v11 fills it from environ.
type serveConfig struct {
	Listen         string `env:"ORCH_LISTEN" envDefault:":8080"`
	DatabasePath   string `env:"ORCH_DB_PATH,required"`
	HomelabStorage string `env:"HOMELAB_STORAGE,required"`
	SecureCookies  bool   `env:"ORCH_SECURE_COOKIES" envDefault:"true"`
}

// runServe opens and migrates the database, then serves HTTP until ctx is cancelled.
func runServe(ctx context.Context, args, environ []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	if err := fset.Parse(args); err != nil {
		return fmt.Errorf("serve: %w: %w", err, errUsage)
	}
	if fset.NArg() != 0 {
		return fmt.Errorf("serve takes no arguments: %w", errUsage)
	}

	var cfg serveConfig
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: env.ToMap(environ)}); err != nil {
		return fmt.Errorf("load serve config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(stdout, nil))

	store, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	spa, err := frontend.FS()
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: server.NewServer(server.Deps{
			Logger: logger,
			SPA:    spa,
			Store:  store,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	logger.InfoContext(ctx, "listening", "addr", ln.Addr().String())

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve http: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		// The parent ctx is already cancelled; shutdown gets its own bounded context.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http: %w", err)
		}
		logger.InfoContext(shutdownCtx, "server stopped")
		return nil
	})
	return g.Wait()
}
