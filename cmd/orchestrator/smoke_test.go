package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestRunSmokeConfigErrors covers smoke's configuration errors only: the checks themselves are
// truenas.Smoke's, tested against a replaying server in internal/truenas.
func TestRunSmokeConfigErrors(t *testing.T) {
	const key = "TRUENAS_API_KEY=hunter2-smoke"
	tests := []struct {
		name      string
		args      []string
		environ   []string
		wantMsg   string
		wantUsage bool
	}{
		{"missing key", nil, []string{"TRUENAS_URL=wss://nas.lan"}, "TRUENAS_API_KEY is not set", true},
		{"empty key", nil, []string{"TRUENAS_URL=wss://nas.lan", "TRUENAS_API_KEY="}, "TRUENAS_API_KEY is not set", true},
		{"api-key flag", []string{"--api-key", "hunter2-flag"}, []string{"TRUENAS_URL=wss://nas.lan"}, "api-key", true},
		{"api-key flag with key set", []string{"--api-key=hunter2-flag"}, []string{"TRUENAS_URL=wss://nas.lan", key}, "api-key", true},
		{"key flag", []string{"--key", "hunter2-flag"}, []string{"TRUENAS_URL=wss://nas.lan", key}, "key", true},
		{"missing url", nil, []string{key}, "TRUENAS_URL is not set", true},
		{"positional argument", []string{"nas.lan"}, []string{"TRUENAS_URL=wss://nas.lan", key}, "no arguments", true},
		{"plain ws to a remote host", nil, []string{"TRUENAS_URL=ws://nas.lan", key}, "loopback", true},
		{"https url", nil, []string{"TRUENAS_URL=https://nas.lan", key}, "wss://", true},
		{"bad insecure flag", nil, []string{"TRUENAS_URL=wss://nas.lan", key, "TRUENAS_INSECURE_TLS=maybe"}, "InsecureTLS", false},
		{"missing ca file", nil, []string{"TRUENAS_URL=wss://nas.lan", key, "TRUENAS_CA_FILE=/nonexistent/ca.pem"}, "TRUENAS_CA_FILE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			args := append([]string{"orchestrator", "smoke"}, tt.args...)
			err := run(t.Context(), args, tt.environ, &stdout)
			if err == nil {
				t.Fatalf("run(%q) = nil, want an error", args)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("run(%q) error = %q, want it to mention %q", args, err, tt.wantMsg)
			}
			if got := errors.Is(err, errUsage); got != tt.wantUsage {
				t.Errorf("errors.Is(%v, errUsage) = %v, want %v", err, got, tt.wantUsage)
			}
			wantCode := exitError
			if tt.wantUsage {
				wantCode = exitUsage
			}
			if got := exitCode(err); got != wantCode {
				t.Errorf("exitCode(%v) = %d, want %d", err, got, wantCode)
			}
			if out := err.Error() + stdout.String(); strings.Contains(out, "hunter2") {
				t.Errorf("run(%q) output or error holds the key: %q", args, out)
			}
		})
	}
}
