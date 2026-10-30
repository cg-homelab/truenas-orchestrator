package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
		})
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
