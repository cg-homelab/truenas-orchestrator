package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/testutil/golden"
)

// fixtureDir materialises fixtures/homelab-repo as a git repository in a temp dir.
func fixtureDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "init-fixture-repo.sh"))
	if err != nil {
		t.Fatalf("locate script: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	cmd := exec.CommandContext(t.Context(), "bash", script, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init fixture repo: %v\n%s", err, out)
	}
	return dir
}

func TestRunValidateGolden(t *testing.T) {
	dir := fixtureDir(t)
	tests := []struct {
		name  string
		extra []string
	}{
		{"validate-text", nil},
		{"validate-json", []string{"--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"orchestrator", "validate", "--homelab-storage", "/srv/homelab"}
			args = append(args, tt.extra...)
			args = append(args, dir)
			var buf bytes.Buffer
			err := run(t.Context(), args, nil, &buf)
			if !errors.Is(err, errFindings) {
				t.Errorf("run(validate) error = %v, want errFindings", err)
			}
			if got := exitCode(err); got != exitError {
				t.Errorf("exitCode(%v) = %d, want %d", err, got, exitError)
			}
			if strings.Contains(buf.String(), dir) {
				t.Errorf("output contains the repo temp path %q", dir)
			}
			golden.Assert(t, tt.name, buf.Bytes())
		})
	}
}

func TestRunValidateReadOnlyRepo(t *testing.T) {
	dir := fixtureDir(t)
	chmod := func(mode string) {
		if out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "chmod", "-R", mode, dir).CombinedOutput(); err != nil {
			t.Fatalf("chmod -R %s: %v\n%s", mode, err, out)
		}
	}
	chmod("a-w")
	t.Cleanup(func() { chmod("u+w") })

	var buf bytes.Buffer
	err := run(t.Context(), []string{"orchestrator", "validate", "--homelab-storage", "/srv/homelab", dir}, nil, &buf)
	if !errors.Is(err, errFindings) {
		t.Fatalf("run(validate) on a read-only repo error = %v, want errFindings", err)
	}
	if !strings.Contains(buf.String(), "== ok-control") {
		t.Errorf("report is missing the stacks:\n%s", buf.String())
	}
}

func TestRunValidateRelativeHomelabStorage(t *testing.T) {
	dir := fixtureDir(t)
	t.Chdir(filepath.Dir(dir))
	base := filepath.Base(dir)

	var abs, rel bytes.Buffer
	if err := run(t.Context(), []string{"orchestrator", "validate", "--homelab-storage", dir, dir}, nil, &abs); !errors.Is(err, errFindings) {
		t.Fatalf("run(validate) absolute error = %v, want errFindings", err)
	}
	if err := run(t.Context(), []string{"orchestrator", "validate", "--homelab-storage", base, base}, nil, &rel); !errors.Is(err, errFindings) {
		t.Fatalf("run(validate) relative error = %v, want errFindings", err)
	}
	if diff := cmp.Diff(abs.String(), rel.String()); diff != "" {
		t.Errorf("relative --homelab-storage changes the report (-abs +rel):\n%s", diff)
	}
}
