package gitrepo

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// envRepo is a git repository in a temp dir with an empty compose/<app> directory and a
// .gitignore that covers compose/*/.env.
func envRepo(t *testing.T, app string) string {
	t.Helper()
	dir := bareEnvRepo(t, app)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("compose/*/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// bareEnvRepo is envRepo without a .gitignore.
func bareEnvRepo(t *testing.T, app string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, "compose", app), 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnvStoreRoundTrip(t *testing.T) {
	dir := envRepo(t, "authelia")
	s := NewEnvStore(New(dir))
	ctx := t.Context()
	if _, err := s.ReadEnv(ctx, "authelia"); !errors.Is(err, homelab.ErrNotFound) {
		t.Fatalf("ReadEnv(absent) error = %v, want ErrNotFound", err)
	}
	for _, content := range []string{"# c\nA=1\n", "A=2\r\nB=\"x y\"\n", ""} {
		if err := s.WriteEnv(ctx, "authelia", []byte(content)); err != nil {
			t.Fatalf("WriteEnv() error = %v", err)
		}
		got, err := s.ReadEnv(ctx, "authelia")
		if err != nil || string(got) != content {
			t.Errorf("ReadEnv() = %q, %v; want %q", got, err, content)
		}
		fi, err := os.Stat(filepath.Join(dir, "compose", "authelia", ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != envFileMode {
			t.Errorf("mode = %o, want %o", fi.Mode().Perm(), envFileMode)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "compose", "authelia"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("app dir holds %d entries, want only .env (no temp files left)", len(entries))
	}
}

func TestEnvStoreRefusesTrackedFile(t *testing.T) {
	dir := envRepo(t, "app")
	p := filepath.Join(dir, "compose", "app", ".env")
	if err := os.WriteFile(p, []byte("COMMITTED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-f", "compose/app/.env")
	err := NewEnvStore(New(dir)).WriteEnv(t.Context(), "app", []byte("NEW=secret-value\n"))
	if !errors.Is(err, homelab.ErrTracked) || !errors.Is(err, homelab.ErrConflict) {
		t.Fatalf("WriteEnv(tracked) error = %v, want ErrTracked (a conflict)", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "COMMITTED=1\n" {
		t.Errorf(".env = %q, want untouched", b)
	}
}

func TestEnvStoreRejects(t *testing.T) {
	dir := envRepo(t, "app")
	s := NewEnvStore(New(dir))
	ctx := t.Context()
	for _, app := range []string{"", "Bad", "../app", "app/../x", "-x"} {
		if _, err := s.ReadEnv(ctx, app); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("ReadEnv(%q) error = %v, want ErrValidation", app, err)
		}
		if err := s.WriteEnv(ctx, app, nil); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("WriteEnv(%q) error = %v, want ErrValidation", app, err)
		}
	}
	if err := s.WriteEnv(ctx, "missing", []byte("A=1\n")); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("WriteEnv(missing app dir) error = %v, want ErrNotFound", err)
	}
}

func TestEnvStoreSymlinkedAppDir(t *testing.T) {
	dir := envRepo(t, "app")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "compose", "linked")); err != nil {
		t.Fatal(err)
	}
	s := NewEnvStore(New(dir))
	if err := s.WriteEnv(t.Context(), "linked", []byte("A=1\n")); err == nil {
		t.Error("WriteEnv(symlinked app dir) = nil, want an error")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("write escaped the repo: %d entries outside", len(entries))
	}
}

func TestEnvStoreRefusesUnignoredFile(t *testing.T) {
	dir := bareEnvRepo(t, "app")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(dir)
	if ignored, err := r.Ignored(t.Context(), "compose/app/.env"); err != nil || ignored {
		t.Fatalf("Ignored() = %v, %v; want false", ignored, err)
	}
	err := NewEnvStore(r).WriteEnv(t.Context(), "app", []byte("A=1\n"))
	if !errors.Is(err, homelab.ErrNotIgnored) || errors.Is(err, homelab.ErrTracked) {
		t.Fatalf("WriteEnv(not ignored) error = %v, want ErrNotIgnored and not ErrTracked", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "compose", "app", ".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Error(".env was written")
	}
}

func TestIgnored(t *testing.T) {
	dir := envRepo(t, "app")
	r := New(dir)
	for rel, want := range map[string]bool{"compose/app/.env": true, "compose/app/compose.yaml": false, "compose/.global.env": false} {
		if got, err := r.Ignored(t.Context(), rel); err != nil || got != want {
			t.Errorf("Ignored(%s) = %v, %v; want %v", rel, got, err, want)
		}
	}
}

func TestEnvStoreRefusesSymlinkedEnv(t *testing.T) {
	dir := envRepo(t, "app")
	outside := filepath.Join(t.TempDir(), "elsewhere.env")
	if err := os.WriteFile(outside, []byte("OTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "compose", "app", ".env")); err != nil {
		t.Fatal(err)
	}
	err := NewEnvStore(New(dir)).WriteEnv(t.Context(), "app", []byte("A=1\n"))
	if !errors.Is(err, homelab.ErrUnreadable) {
		t.Fatalf("WriteEnv(symlinked .env) error = %v, want ErrUnreadable", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "OTHER=1\n" {
		t.Errorf("link target = %q, want untouched", b)
	}
}

func TestEnvStoreRemovesStaleTemps(t *testing.T) {
	dir := envRepo(t, "app")
	stale := filepath.Join(dir, "compose", "app", ".env.tmp-deadbeef")
	if err := os.WriteFile(stale, []byte("LEFT=over\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewEnvStore(New(dir)).WriteEnv(t.Context(), "app", []byte("A=1\n")); err != nil {
		t.Fatalf("WriteEnv() error = %v", err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Error("stale temp file was not removed")
	}
}
