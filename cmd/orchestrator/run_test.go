package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestRunOpenAPI(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"orchestrator", "openapi"}, nil, &stdout); err != nil {
		t.Fatalf("run(openapi) error = %v", err)
	}
	out := stdout.Bytes()
	if !json.Valid(out) {
		t.Fatalf("run(openapi) output is not valid JSON:\n%s", out)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Errorf("run(openapi) output does not end with a newline")
	}
	var doc struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Errorf("openapi version field is empty")
	}
	if _, ok := doc.Paths["/api/health"]; !ok {
		t.Errorf("openapi paths = %v, want /api/health", slices.Sorted(maps.Keys(doc.Paths)))
	}
}

func TestRunOpenAPIIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	for _, buf := range []*bytes.Buffer{&a, &b} {
		if err := run(t.Context(), []string{"orchestrator", "openapi"}, nil, buf); err != nil {
			t.Fatalf("run(openapi) error = %v", err)
		}
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Errorf("two runs of openapi produced different output")
	}
}

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no subcommand", []string{"orchestrator"}},
		{"unknown subcommand", []string{"orchestrator", "frobnicate"}},
		{"validate without path", []string{"orchestrator", "validate"}},
		{"validate with json but no path", []string{"orchestrator", "validate", "--json"}},
		{"validate with two paths", []string{"orchestrator", "validate", "a", "b"}},
		{"validate with unknown flag", []string{"orchestrator", "validate", "--nope", "a"}},
		{"openapi with an argument", []string{"orchestrator", "openapi", "extra"}},
		{"serve with an argument", []string{"orchestrator", "serve", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(t.Context(), tt.args, nil, &stdout)
			if !errors.Is(err, errUsage) {
				t.Fatalf("run(%q) error = %v, want errUsage", tt.args, err)
			}
			if got := exitCode(err); got != exitUsage {
				t.Errorf("exitCode(%v) = %d, want %d", err, got, exitUsage)
			}
		})
	}
}

func TestRunServeConfigErrors(t *testing.T) {
	dbPath := "ORCH_DB_PATH=" + filepath.Join(t.TempDir(), "o.db")
	missing := filepath.Join(t.TempDir(), "missing")
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtures := filepath.Join("..", "..", "fixtures", "truenas")
	tests := []struct {
		name    string
		environ []string
		wantMsg string
	}{
		{"missing database path", []string{"HOMELAB_STORAGE=/x"}, "ORCH_DB_PATH"},
		{"missing homelab storage", []string{dbPath}, "HOMELAB_STORAGE"},
		{"bad secure cookies", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_SECURE_COOKIES=maybe"}, "SecureCookies"},
		{"bad session idle", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_SESSION_IDLE=soon"}, "SessionIdle"},
		{"bad session lifetime", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_SESSION_LIFETIME=1week"}, "SessionLifetime"},
		{"zero session idle", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_SESSION_IDLE=0s"}, "ORCH_SESSION_IDLE"},
		{"negative session lifetime", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_SESSION_LIFETIME=-1h"}, "ORCH_SESSION_LIFETIME"},
		{"bad check host paths", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_CHECK_HOST_PATHS=sometimes"}, "CheckHostPaths"},
		{"api key variable", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_API_KEY=hunter2"}, "ORCH_TRUENAS_API_KEY is not supported"},
		{"plain ws to a remote host", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=ws://nas.lan"}, "loopback"},
		{"https truenas url", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=https://nas.lan"}, "wss://"},
		{"truenas url with a path", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=wss://nas.lan/api/current"}, "wss://host"},
		{"bad insecure tls", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_INSECURE_TLS=perhaps"}, "TrueNASInsecureTLS"},
		{"missing ca file", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=wss://nas.lan", "ORCH_TRUENAS_CA_FILE=" + missing}, "ORCH_TRUENAS_CA_FILE"},
		{"ca file without pem", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=wss://nas.lan", "ORCH_TRUENAS_CA_FILE=" + notPEM}, "no PEM certificate"},
		{"unknown fake scenario", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=fake:nope", "ORCH_TRUENAS_FIXTURES=" + fixtures}, "fake:nope"},
		{"missing fixtures", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_TRUENAS_URL=fake", "ORCH_TRUENAS_FIXTURES=" + missing}, "ORCH_TRUENAS_URL fake"},
		{"missing state storage", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_STATE_STORAGE=" + missing}, "ORCH_STATE_STORAGE"},
		{"state storage is a file", []string{dbPath, "HOMELAB_STORAGE=/x", "ORCH_STATE_STORAGE=" + notPEM}, "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(t.Context(), []string{"orchestrator", "serve"}, tt.environ, io.Discard)
			if err == nil {
				t.Fatalf("run(serve) with %q = nil, want error", tt.environ)
			}
			if errors.Is(err, errUsage) {
				t.Errorf("run(serve) config error is a usage error: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("run(serve) error = %q, want it to mention %s", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("run(serve) error holds a key value: %q", err)
			}
		})
	}
}

func TestNewTrueNASDialerFake(t *testing.T) {
	fixtures := filepath.Join("..", "..", "fixtures", "truenas")
	logger := slog.New(slog.DiscardHandler)
	creds := homelab.TrueNASCredentials{Username: "truenas_admin", APIKey: homelab.NewSecret("hunter2")}
	tests := []struct {
		name    string
		url     string
		wantURL string
		wantErr error
	}{
		{"default scenario", "fake", "fake:fresh", nil},
		{"named scenario", "fake:ready", "fake:ready", nil},
		{"rejected key", "fake:auth-failure", "fake:auth-failure", homelab.ErrUnauthenticated},
		{"unsupported version", "fake:version-out-of-range", "fake:version-out-of-range", homelab.ErrValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := serveConfig{TrueNASURL: tt.url, TrueNASFixtures: fixtures, TrueNASFakeMnt: "/mnt"}
			dial, url, err := newTrueNASDialer(cfg, homelab.DefaultSelfApp, logger)
			if err != nil {
				t.Fatalf("newTrueNASDialer(%q) error = %v", tt.url, err)
			}
			if url != tt.wantURL {
				t.Errorf("newTrueNASDialer(%q) url = %q, want %q", tt.url, url, tt.wantURL)
			}
			conn, err := dial(t.Context(), creds)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("dial error = %v, want %v", err, tt.wantErr)
				}
				if conn != nil { // an interface holding a nil *Fake would pass a nil check by the caller
					t.Fatalf("dial on error returned a non-nil conn %#v", conn)
				}
				return
			}
			if err != nil {
				t.Fatalf("dial error = %v", err)
			}
			defer func() { _ = conn.Close() }()
			if _, err := conn.SystemVersion(t.Context()); err != nil {
				t.Errorf("SystemVersion error = %v", err)
			}
		})
	}
}

func TestEnvironmentConfigIdentityCheck(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want homelab.EnvironmentConfig
	}{
		{"not configured", "", homelab.EnvironmentConfig{}},
		{"fake", "fake", homelab.EnvironmentConfig{}},
		{"fake scenario", "fake:ready", homelab.EnvironmentConfig{}},
		{"wss", "wss://nas.lan", homelab.EnvironmentConfig{CheckIdentity: true, ProcessUID: os.Getuid(), ProcessGID: os.Getgid()}},
		{"loopback ws", "ws://127.0.0.1:6000", homelab.EnvironmentConfig{CheckIdentity: true, ProcessUID: os.Getuid(), ProcessGID: os.Getgid()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			want.SelfApp, want.HomelabStorage, want.StateStorage = homelab.DefaultSelfApp, "/mnt/tank/apps/homelab", "/mnt/tank/orchestrator"
			got := environmentConfig(tt.url, homelab.DefaultSelfApp, "/mnt/tank/apps/homelab", "/mnt/tank/orchestrator")
			if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(homelab.DotEnv{})); diff != "" {
				t.Errorf("environmentConfig(%q) mismatch (-want +got):\n%s", tt.url, diff)
			}
		})
	}
}

func TestNewTrueNASDialerNotConfigured(t *testing.T) {
	dial, url, err := newTrueNASDialer(serveConfig{}, homelab.DefaultSelfApp, slog.New(slog.DiscardHandler))
	if err != nil || dial != nil || url != "" {
		t.Errorf("newTrueNASDialer(empty URL) = %v, %q, %v; want nil, \"\", nil", dial != nil, url, err)
	}
}

func TestNewTrueNASDialerWSS(t *testing.T) {
	for _, raw := range []string{"wss://nas.lan", "wss://10.0.0.2:8443", "ws://127.0.0.1:6000", "ws://localhost"} {
		cfg := serveConfig{TrueNASURL: raw, TrueNASInsecureTLS: true}
		dial, url, err := newTrueNASDialer(cfg, homelab.DefaultSelfApp, slog.New(slog.DiscardHandler))
		if err != nil || dial == nil || url != raw {
			t.Errorf("newTrueNASDialer(%q) = dialer %v, %q, %v; want a dialer, %q, nil", raw, dial != nil, url, err, raw)
		}
	}
}

func TestRunServeStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	environ := []string{
		"ORCH_DB_PATH=" + filepath.Join(t.TempDir(), "o.db"),
		"HOMELAB_STORAGE=" + t.TempDir(),
		"ORCH_LISTEN=127.0.0.1:0",
	}

	logR, logW := io.Pipe()
	addrc := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(logR)
		for sc.Scan() {
			var line struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal(sc.Bytes(), &line) == nil && line.Msg == "listening" {
				addrc <- line.Addr
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"orchestrator", "serve"}, environ, logW)
		_ = logW.Close()
	}()

	var addr string
	select {
	case addr = <-addrc:
	case err := <-done:
		t.Fatalf("run(serve) returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("run(serve) did not log its listen address within 10s")
	}

	for path, want := range map[string]int{
		"/api/health":      http.StatusOK,
		"/api/auth/status": http.StatusOK,
		"/api/stacks":      http.StatusUnauthorized,
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, http.NoBody)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s status = %d, want %d", path, resp.StatusCode, want)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run(serve) after cancel = %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run(serve) did not stop within 15s of cancel")
	}
}
