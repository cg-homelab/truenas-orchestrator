package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/caarlos0/env/v11"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/truenas"
)

// smokeKeyVar is the only way to give smoke the API key: no flag accepts one, so it never lands in
// shell history or a process listing.
const smokeKeyVar = "TRUENAS_API_KEY"

// smokeConfig is what `smoke` reads from environ, apart from the API key.
type smokeConfig struct {
	// URL is wss://host[:port] (`just smoke <host>` sets it).
	URL string `env:"TRUENAS_URL"`
	// Username owns the API key.
	Username string `env:"TRUENAS_USERNAME" envDefault:"truenas_admin"`
	// CAFile, ServerName and InsecureTLS are as ORCH_TRUENAS_CA_FILE and friends for serve.
	CAFile      string `env:"TRUENAS_CA_FILE"`
	ServerName  string `env:"TRUENAS_TLS_SERVER_NAME"`
	InsecureTLS bool   `env:"TRUENAS_INSECURE_TLS" envDefault:"false"`
}

// runSmoke checks a live TrueNAS against the shapes the client decodes (design section 10) and
// prints one row per check. It fails when the run could not complete or any check failed.
func runSmoke(ctx context.Context, args, environ []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	record := fset.String("record", "", "write the scrubbed replies to this directory (fixtures/truenas/wire)")
	if err := fset.Parse(args); err != nil {
		return fmt.Errorf("smoke: %w: %w", err, errUsage)
	}
	if fset.NArg() != 0 {
		return fmt.Errorf("smoke takes no arguments: %w", errUsage)
	}

	vars := env.ToMap(environ)
	apiKey := homelab.NewSecret(vars[smokeKeyVar])
	delete(vars, smokeKeyVar)
	if apiKey.Reveal() == "" {
		return fmt.Errorf("%s is not set: %w", smokeKeyVar, errUsage)
	}

	var cfg smokeConfig
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: vars}); err != nil {
		return fmt.Errorf("load smoke config: %w", err)
	}
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return fmt.Errorf("TRUENAS_URL is not set (want wss://host[:port]): %w", errUsage)
	}
	if err := checkTrueNASURL(url, "TRUENAS_URL"); err != nil {
		return fmt.Errorf("%w: %w", err, errUsage)
	}
	tlsCfg, err := truenasTLS(cfg.CAFile, cfg.ServerName, cfg.InsecureTLS, "TRUENAS_")
	if err != nil {
		return fmt.Errorf("load smoke config: %w", err)
	}
	if cfg.InsecureTLS {
		_, _ = fmt.Fprintln(stdout, "warning: TRUENAS_INSECURE_TLS is set: the server certificate is not verified")
	}

	report, err := truenas.Smoke(ctx,
		truenas.Config{URL: url, TLS: tlsCfg},
		homelab.TrueNASCredentials{Username: cfg.Username, APIKey: apiKey},
		stdout, *record)
	if err != nil {
		return fmt.Errorf("smoke: %w", err)
	}
	if report.Failed > 0 {
		return fmt.Errorf("smoke: %d of %d checks failed", report.Failed, len(report.Rows))
	}
	return nil
}
