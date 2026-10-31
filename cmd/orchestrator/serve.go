package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"golang.org/x/sync/errgroup"

	"github.com/cg-homelab/truenas-orchestrator/frontend"
	"github.com/cg-homelab/truenas-orchestrator/internal/gitrepo"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/hostfs"
	"github.com/cg-homelab/truenas-orchestrator/internal/server"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
	"github.com/cg-homelab/truenas-orchestrator/internal/truenas"
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

	// TrueNASURL is wss://host[:port], or fake[:scenario] for the in-process Fake (D35). Empty
	// leaves TrueNAS not configured. The API key is never configuration: it is entered in the UI
	// and kept in the database (D34).
	TrueNASURL string `env:"ORCH_TRUENAS_URL"`
	// TrueNASCAFile is a PEM file of the roots that verify TrueNAS, e.g. its UI certificate.
	TrueNASCAFile string `env:"ORCH_TRUENAS_CA_FILE"`
	// TrueNASServerName overrides the name the TrueNAS certificate is verified against
	// (localhost for TrueNAS's default self-signed certificate).
	TrueNASServerName string `env:"ORCH_TRUENAS_TLS_SERVER_NAME"`
	// TrueNASInsecureTLS skips certificate verification; logged as a warning on every connect.
	TrueNASInsecureTLS bool `env:"ORCH_TRUENAS_INSECURE_TLS" envDefault:"false"`
	// TrueNASFixtures is the fixtures/truenas directory the Fake reads its scenario from.
	TrueNASFixtures string `env:"ORCH_TRUENAS_FIXTURES" envDefault:"fixtures/truenas"`
	// TrueNASFakeMnt replaces @MNT@ in the Fake's scenario.
	TrueNASFakeMnt string `env:"ORCH_TRUENAS_FAKE_MNT" envDefault:"/mnt"`
	// StateStorage is the host path of the orchestrator's state dataset; optional, but the
	// environment check and the deploy key need it.
	StateStorage string `env:"ORCH_STATE_STORAGE"`
	// SelfApp is the orchestrator's own TrueNAS app, never modified. Empty means
	// homelab.DefaultSelfApp.
	SelfApp string `env:"ORCH_SELF_APP"`
}

// fakeScheme selects the in-process Fake in ORCH_TRUENAS_URL; defaultFakeScenario is its
// scenario when none is named.
const (
	fakeScheme          = "fake"
	defaultFakeScenario = "fresh"
)

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

	vars := env.ToMap(environ)
	if err := refuseKeyVariables(vars); err != nil {
		return err
	}
	var cfg serveConfig
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: vars}); err != nil {
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
	state, err := stateStorage(cfg.StateStorage)
	if err != nil {
		return err
	}
	selfApp := cfg.SelfApp
	if selfApp == "" {
		selfApp = homelab.DefaultSelfApp
	}

	logger := slog.New(slog.NewJSONHandler(stdout, nil))

	dial, truenasURL, err := newTrueNASDialer(cfg, selfApp, logger)
	if err != nil {
		return err
	}
	switch {
	case dial == nil:
		logger.InfoContext(ctx, "truenas not configured")
	case cfg.TrueNASInsecureTLS && !isFakeURL(cfg.TrueNASURL):
		logger.WarnContext(ctx, "truenas tls verification disabled", "url", truenasURL)
	default:
		logger.InfoContext(ctx, "truenas configured", "url", truenasURL)
	}

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

	repo := gitrepo.New(storage)
	if state != "" {
		repo = gitrepo.NewWithOptions(storage, gitrepo.Options{
			SSHKey:     filepath.Join(state, homelab.DeployKeyRel),
			KnownHosts: filepath.Join(state, homelab.KnownHostsRel),
		})
	}
	handler := server.NewServer(server.Deps{
		Logger:          logger,
		SPA:             spa,
		Store:           store,
		Repo:            repo,
		Secrets:         gitrepo.NewEnvStore(repo),
		Host:            hostfs.New(),
		Validate:        validate,
		TrueNAS:         dial,
		TrueNASURL:      truenasURL,
		Credentials:     store,
		Environment:     environmentConfig(cfg.TrueNASURL, selfApp, storage, state),
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

// refuseKeyVariables fails start-up when the environment tries to configure the TrueNAS API key:
// it is entered in the UI and kept in the database (D34), never set in the app definition. The
// error names the variable, never its value.
func refuseKeyVariables(vars map[string]string) error {
	for name := range vars {
		if strings.HasPrefix(name, "ORCH_TRUENAS_") && strings.Contains(name, "KEY") {
			return fmt.Errorf("load serve config: %s is not supported: the TrueNAS API key is entered in the UI "+
				"and stored in the database, never in the environment; unset it", name)
		}
	}
	return nil
}

// stateStorage resolves ORCH_STATE_STORAGE to an absolute path that must be an existing
// directory. Empty stays empty: the environment check then reports it.
func stateStorage(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve ORCH_STATE_STORAGE: %w", err)
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("load serve config: ORCH_STATE_STORAGE %s does not exist; create the state dataset first", abs)
	case err != nil:
		return "", fmt.Errorf("load serve config: ORCH_STATE_STORAGE: %w", err)
	case !info.IsDir():
		return "", fmt.Errorf("load serve config: ORCH_STATE_STORAGE %s is not a directory", abs)
	}
	return abs, nil
}

// environmentConfig is the readiness check's configuration. The running-identity check (the
// process must run as PUID:PGID, D32) is enabled only for a real TrueNAS URL: against the fake, or
// with TrueNAS not configured, the process is a developer's own login (on a Mac, uid 501), which
// never matches the scenario's PUID, so the check would block every dev run for no reason.
func environmentConfig(truenasURL, selfApp, storage, state string) homelab.EnvironmentConfig {
	ec := homelab.EnvironmentConfig{SelfApp: selfApp, HomelabStorage: storage, StateStorage: state}
	if raw := strings.TrimSpace(truenasURL); raw != "" && !isFakeURL(raw) {
		ec.CheckIdentity, ec.ProcessUID, ec.ProcessGID = true, os.Getuid(), os.Getgid()
	}
	return ec
}

// isFakeURL reports whether ORCH_TRUENAS_URL selects the Fake: fake or fake:<scenario>.
func isFakeURL(raw string) bool {
	return raw == fakeScheme || strings.HasPrefix(raw, fakeScheme+":")
}

// newTrueNASDialer builds the dialer ORCH_TRUENAS_URL selects and the URL shown in the settings:
// nil for an empty URL (not configured), the Fake for fake[:scenario], and truenas.Dial for
// wss://. Every configuration error is reported here, at start-up, rather than on first connect.
func newTrueNASDialer(cfg serveConfig, selfApp string, logger *slog.Logger) (server.TrueNASDialer, string, error) {
	raw := strings.TrimSpace(cfg.TrueNASURL)
	switch {
	case raw == "":
		return nil, "", nil
	case isFakeURL(raw):
		scenario := strings.TrimPrefix(strings.TrimPrefix(raw, fakeScheme), ":")
		if scenario == "" {
			scenario = defaultFakeScenario
		}
		fixtures, err := filepath.Abs(cfg.TrueNASFixtures)
		if err != nil {
			return nil, "", fmt.Errorf("resolve ORCH_TRUENAS_FIXTURES: %w", err)
		}
		mnt, err := filepath.Abs(cfg.TrueNASFakeMnt)
		if err != nil {
			return nil, "", fmt.Errorf("resolve ORCH_TRUENAS_FAKE_MNT: %w", err)
		}
		fsys := os.DirFS(fixtures)
		opts := truenas.FakeOptions{Mnt: mnt, SelfApp: selfApp}
		// Load once now so that a typo in the scenario or a missing fixtures directory stops
		// start-up; FakeDialer loads its own copy on the first dial.
		if _, err := truenas.LoadScenario(fsys, scenario, opts); err != nil {
			return nil, "", fmt.Errorf("load serve config: ORCH_TRUENAS_URL %s (fixtures %s): %w", raw, fixtures, err)
		}
		return adaptDialer(truenas.FakeDialer(fsys, scenario, opts)), fakeScheme + ":" + scenario, nil
	}

	if err := checkTrueNASURL(raw, "ORCH_TRUENAS_URL"); err != nil {
		return nil, "", fmt.Errorf("load serve config: %w", err)
	}
	tlsCfg, err := truenasTLS(cfg.TrueNASCAFile, cfg.TrueNASServerName, cfg.TrueNASInsecureTLS, "ORCH_TRUENAS_")
	if err != nil {
		return nil, "", fmt.Errorf("load serve config: %w", err)
	}
	tcfg := truenas.Config{URL: raw, TLS: tlsCfg, SelfApp: selfApp, Logger: logger}
	insecure := cfg.TrueNASInsecureTLS
	return adaptDialer(func(ctx context.Context, creds homelab.TrueNASCredentials) (*truenas.Client, error) {
		if insecure {
			logger.WarnContext(ctx, "truenas tls verification disabled", "url", raw)
		}
		return truenas.Dial(ctx, tcfg, creds)
	}), raw, nil
}

// adaptDialer turns a dial function returning a concrete session into a server.TrueNASDialer.
// It returns a nil interface on error, never an interface holding a nil pointer.
func adaptDialer[C server.TrueNASConn](
	dial func(context.Context, homelab.TrueNASCredentials) (C, error),
) server.TrueNASDialer {
	return func(ctx context.Context, creds homelab.TrueNASCredentials) (server.TrueNASConn, error) {
		conn, err := dial(ctx, creds)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// checkTrueNASURL accepts wss://host[:port], and ws:// for a loopback host only: TrueNAS revokes
// an API key used over a connection it does not consider secure (D35). The client checks the
// same; this fails at start-up instead of on the first connect.
func checkTrueNASURL(raw, name string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("%s %q: want wss://host[:port]", name, raw)
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		host := u.Hostname()
		if ip := net.ParseIP(host); strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("%s %q: ws:// is allowed for a loopback host only, because TrueNAS revokes an API key "+
			"used over plain HTTP; use wss://", name, raw)
	default:
		return fmt.Errorf("%s %q: want wss://host[:port]", name, raw)
	}
}

// truenasTLS builds the TLS configuration for TrueNAS from a PEM roots file (read once, here), a
// server name override and the insecure switch. prefix names the variables in errors.
func truenasTLS(caFile, serverName string, insecure bool, prefix string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		// Opt-in for a lab without a usable certificate; logged as a warning on every connect.
		InsecureSkipVerify: insecure, //nolint:gosec // G402: explicit ORCH_TRUENAS_INSECURE_TLS/TRUENAS_INSECURE_TLS opt-in, warned on use
	}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // G304: the path is operator configuration, not request input
	if err != nil {
		return nil, fmt.Errorf("read %sCA_FILE: %w", prefix, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%sCA_FILE %s holds no PEM certificate", prefix, caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
