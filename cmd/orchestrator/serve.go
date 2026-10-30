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
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
	"golang.org/x/sync/errgroup"

	"github.com/cg-homelab/truenas-orchestrator/frontend"
	"github.com/cg-homelab/truenas-orchestrator/internal/gitrepo"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/server"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

// shutdownTimeout bounds how long in-flight requests get to finish after a shutdown signal.
const shutdownTimeout = 10 * time.Second

// serveConfig is what `serve` needs; github.com/caarlos0/env/v11 fills it from environ.
type serveConfig struct {
	Listen       string `env:"ORCH_LISTEN" envDefault:":8080"`
	DatabasePath string `env:"ORCH_DB_PATH,required"`
	// HomelabStorage is the homelab repo checkout, and the value injected for HOMELAB_STORAGE.
	HomelabStorage string `env:"HOMELAB_STORAGE,required"`
	// SecureCookies sets Secure on the session cookie; false only for plain-HTTP local dev.
	SecureCookies bool `env:"ORCH_SECURE_COOKIES" envDefault:"true"`
	// SessionIdle ends a session after this long without a request.
	SessionIdle time.Duration `env:"ORCH_SESSION_IDLE" envDefault:"24h"`
	// SessionLifetime ends a session this long after login.
	SessionLifetime time.Duration `env:"ORCH_SESSION_LIFETIME" envDefault:"168h"`
	// CheckHostPaths enables PATH001: host paths in compose files must exist on this machine.
	CheckHostPaths bool `env:"ORCH_CHECK_HOST_PATHS" envDefault:"true"`
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
	if cfg.SessionIdle <= 0 || cfg.SessionLifetime <= 0 {
		return errors.New("load serve config: ORCH_SESSION_IDLE and ORCH_SESSION_LIFETIME must be positive")
	}
	// Absolute, like `orchestrator validate`, so the injected HOMELAB_STORAGE matches the CLI's.
	storage, err := filepath.Abs(cfg.HomelabStorage)
	if err != nil {
		return fmt.Errorf("resolve HOMELAB_STORAGE: %w", err)
	}
	validate := homelab.Options{HomelabStorage: storage}
	if cfg.CheckHostPaths {
		validate.HostPathExists = hostPathExists
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

	handler := server.NewServer(server.Deps{
		Logger:          logger,
		SPA:             spa,
		Store:           store,
		Repo:            gitrepo.New(storage),
		Validate:        validate,
		SecureCookies:   cfg.SecureCookies,
		SessionIdle:     cfg.SessionIdle,
		SessionLifetime: cfg.SessionLifetime,
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Requests must outlive the shutdown signal so in-flight ones can finish within
		// shutdownTimeout; Shutdown bounds them. Job streams end because the runner, stopped by
		// the signal, finishes every job it holds.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	logger.InfoContext(ctx, "listening", "addr", ln.Addr().String())

	g, gctx := errgroup.WithContext(ctx)
	// The job runner stops when gctx is done; g.Wait returns only after it has recorded the
	// outcome of the job it was running, and before the deferred store.Close.
	g.Go(func() error { return handler.Run(gctx) })
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
