package gitrepo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// fixtureRepo materialises fixtures/homelab-repo as a git repository in a temp dir.
func fixtureRepo(t *testing.T) *Repo {
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
	cmd.Env = isolatedEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init fixture repo: %v\n%s", err, out)
	}
	return New(dir)
}

func TestApps(t *testing.T) {
	r := fixtureRepo(t)
	got, err := r.Apps(t.Context())
	if err != nil {
		t.Fatalf("Apps() error = %v", err)
	}
	if !slices.IsSorted(got) {
		t.Errorf("Apps() = %v, want sorted", got)
	}
	want := []string{
		"authelia", "bad-yaml", "broken-networks", "broken-paths", "broken-setup", "db-client",
		"db-postgres", "db-prometheus", "db-redis", "db-timescale", "env-drift", "legacy-name", "ok-control",
	}
	want = append(want, "Bad_Name")
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("Apps() = %v, missing %q", got, name)
		}
	}
}

func TestAppsListsEveryDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"compose/good-1", "compose/Bad", "compose/-lead", "compose/.hidden", "compose/_x"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "compose", "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Apps(t.Context())
	if err != nil {
		t.Fatalf("Apps() error = %v", err)
	}
	if want := []string{"-lead", ".hidden", "Bad", "_x", "good-1"}; !slices.Equal(got, want) {
		t.Errorf("Apps() = %v, want %v", got, want)
	}
	if got, err := New(t.TempDir()).Apps(t.Context()); err != nil || got != nil {
		t.Errorf("Apps() without compose/ = %v, %v, want nil, nil", got, err)
	}
}

func TestTracked(t *testing.T) {
	r := fixtureRepo(t)
	tests := []struct {
		rel  string
		want bool
	}{
		{"compose/env-drift/.env", true},
		{"compose/db-postgres/.env", false},
		{"compose/ok-control/compose.yaml", true},
		{"compose/nope.yaml", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, err := r.Tracked(t.Context(), tt.rel)
			if err != nil {
				t.Fatalf("Tracked(%q) error = %v", tt.rel, err)
			}
			if got != tt.want {
				t.Errorf("Tracked(%q) = %v, want %v", tt.rel, got, tt.want)
			}
		})
	}
}

func TestReadFile(t *testing.T) {
	r := fixtureRepo(t)
	b, err := r.ReadFile(t.Context(), "compose/.global.env")
	if err != nil || len(b) == 0 {
		t.Errorf("ReadFile(.global.env) = %d bytes, %v, want content", len(b), err)
	}
	if _, err := r.ReadFile(t.Context(), "compose/missing.env"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("ReadFile(missing) error = %v, want ErrNotFound", err)
	}
	for _, rel := range []string{"../etc/passwd", "compose/../../etc/passwd", "/etc/passwd"} {
		if b, err := r.ReadFile(t.Context(), rel); !errors.Is(err, homelab.ErrUnreadable) {
			t.Errorf("ReadFile(%q) = %d bytes, %v, want ErrUnreadable", rel, len(b), err)
		}
	}
	if b, err := r.ReadFile(t.Context(), "compose/ok-control"); !errors.Is(err, homelab.ErrUnreadable) {
		t.Errorf("ReadFile(directory) = %d bytes, %v, want ErrUnreadable", len(b), err)
	}
}

func TestReadFileRefusesSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	r := New(dir)
	if _, err := r.ReadFile(t.Context(), "link/secret"); !errors.Is(err, homelab.ErrUnreadable) {
		t.Errorf("ReadFile through an escaping symlink error = %v, want ErrUnreadable", err)
	}
	if _, err := r.Stat(t.Context(), "link/secret"); !errors.Is(err, homelab.ErrUnreadable) {
		t.Errorf("Stat through an escaping symlink error = %v, want ErrUnreadable", err)
	}
}

func TestStat(t *testing.T) {
	r := fixtureRepo(t)
	tests := []struct {
		rel  string
		want homelab.FileInfo
	}{
		{"compose", homelab.FileInfo{Exists: true, IsDir: true}},
		{"compose/ok-control/compose.yaml", homelab.FileInfo{Exists: true}},
		{"compose/nothing-here", homelab.FileInfo{}},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, err := r.Stat(t.Context(), tt.rel)
			if err != nil {
				t.Fatalf("Stat(%q) error = %v", tt.rel, err)
			}
			if got != tt.want {
				t.Errorf("Stat(%q) = %+v, want %+v", tt.rel, got, tt.want)
			}
		})
	}
}

func TestStatusDirtyAndLastFetch(t *testing.T) {
	r := fixtureRepo(t)
	st, err := r.Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.Dirty {
		t.Error("fresh fixture repo is Dirty")
	}
	if !st.LastFetch.IsZero() {
		t.Errorf("LastFetch = %v, want zero without FETCH_HEAD", st.LastFetch)
	}

	if err := os.WriteFile(filepath.Join(r.Root(), "untracked.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err = r.Status(t.Context()); err != nil || st.Dirty {
		t.Errorf("Status() with untracked file = Dirty %v, %v, want clean", st.Dirty, err)
	}

	if err := os.WriteFile(filepath.Join(r.Root(), "compose", "ok-control", "compose.yaml"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err = r.Status(t.Context()); err != nil || !st.Dirty {
		t.Errorf("Status() with modified tracked file = Dirty %v, %v, want dirty", st.Dirty, err)
	}

	if err := os.WriteFile(filepath.Join(r.Root(), ".git", "FETCH_HEAD"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err = r.Status(t.Context()); err != nil || st.LastFetch.IsZero() {
		t.Errorf("Status() with FETCH_HEAD = LastFetch %v, %v, want non-zero", st.LastFetch, err)
	}
}

// gitDirSnapshot records the path, size and mtime of everything under dir/.git.
func gitDirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, ".git"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		snap[p] = fmt.Sprintf("%d %d", fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("walk .git: %v", err)
	}
	return snap
}

func TestStatusAndTrackedDoNotWriteGitDir(t *testing.T) {
	r := fixtureRepo(t)
	// Touch a tracked file so git would normally refresh the index during status.
	p := filepath.Join(r.Root(), "compose", "ok-control", "compose.yaml")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	before := gitDirSnapshot(t, r.Root())
	if _, err := r.Status(t.Context()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if _, err := r.Tracked(t.Context(), "compose/env-drift/.env"); err != nil {
		t.Fatalf("Tracked() error = %v", err)
	}
	if diff := cmp.Diff(before, gitDirSnapshot(t, r.Root())); diff != "" {
		t.Errorf(".git changed (-before +after):\n%s", diff)
	}
}
