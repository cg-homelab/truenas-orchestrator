package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runGit runs git in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "--quiet", "-m", "add "+name)
}

// lines collects reported lines.
type lines struct {
	mu sync.Mutex
	s  []string
}

func (l *lines) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s = append(l.s, s)
}

func (l *lines) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.s, "\n")
}

// remoteSetup builds a bare remote, a pushed work tree and a clone of the remote.
func remoteSetup(t *testing.T) (remote, work string, clone *Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "HOME": base,
		"GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		t.Setenv(k, v)
	}
	remote = filepath.Join(base, "remote.git")
	runGit(t, base, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "init-fixture-repo.sh"))
	if err != nil {
		t.Fatal(err)
	}
	work = filepath.Join(base, "work")
	if out, err := exec.CommandContext(t.Context(), "bash", script, work).CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %v\n%s", err, out)
	}
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "push", "--quiet", "-u", "origin", "main")
	clone, err = Clone(t.Context(), remote, filepath.Join(base, "clone2"), nil)
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	return remote, work, clone
}

func TestFetchNothingNew(t *testing.T) {
	_, _, clone := remoteSetup(t)
	var got lines
	st, err := clone.Fetch(t.Context(), got.add)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if st.Ahead != 0 || st.Behind != 0 || st.Upstream != "origin/main" {
		t.Errorf("Fetch() status = %+v, want upstream origin/main, ahead 0, behind 0", st)
	}
	if got.joined() == "" {
		t.Error("Fetch() reported no lines")
	}
}

func TestFetchFastForwards(t *testing.T) {
	_, work, clone := remoteSetup(t)
	commitFile(t, work, "new.txt", "hello\n")
	runGit(t, work, "push", "--quiet")
	st, err := clone.Fetch(t.Context(), nil)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if st.Behind != 0 || st.Ahead != 0 {
		t.Errorf("Fetch() status = %+v, want in sync", st)
	}
	if _, err := os.Stat(filepath.Join(clone.Root(), "new.txt")); err != nil {
		t.Errorf("new file missing after fast-forward: %v", err)
	}
}

func TestFetchDivergedIsNotFastForward(t *testing.T) {
	_, work, clone := remoteSetup(t)
	commitFile(t, work, "remote.txt", "remote\n")
	runGit(t, work, "push", "--quiet")
	commitFile(t, clone.Root(), "local.txt", "local\n")
	dirty := filepath.Join(clone.Root(), "compose", "ok-control", "compose.yaml")
	if err := os.WriteFile(dirty, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := clone.Fetch(t.Context(), nil)
	if !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("Fetch() error = %v, want ErrNotFastForward", err)
	}
	if b, err := os.ReadFile(dirty); err != nil || string(b) != "dirty\n" {
		t.Errorf("dirty file = %q, %v, want it intact", b, err)
	}
	if _, err := os.Stat(filepath.Join(clone.Root(), "local.txt")); err != nil {
		t.Errorf("local commit lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone.Root(), "remote.txt")); err == nil {
		t.Error("remote commit was merged into the working tree")
	}
	st, err := clone.Status(t.Context())
	if err != nil || st.Ahead != 1 || st.Behind != 1 || !st.Dirty {
		t.Errorf("Status() = %+v, %v, want ahead 1, behind 1, dirty", st, err)
	}
}

func TestFetchRequiresRepoTopLevel(t *testing.T) {
	_, _, clone := remoteSetup(t)
	sub := filepath.Join(clone.Root(), "compose")
	if _, err := New(sub).Fetch(t.Context(), nil); !errors.Is(err, ErrNotRepoRoot) {
		t.Errorf("Fetch() in a subdirectory error = %v, want ErrNotRepoRoot", err)
	}
	if _, err := New(t.TempDir()).Fetch(t.Context(), nil); err == nil || errors.Is(err, ErrNotRepoRoot) {
		t.Errorf("Fetch() in a non-repo directory error = %v, want a git error", err)
	}
	if _, err := clone.Fetch(t.Context(), nil); err != nil {
		t.Errorf("Fetch() at the repo root error = %v", err)
	}
}

func TestCloneIntoNonEmptyDir(t *testing.T) {
	remote, _, _ := remoteSetup(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(t.Context(), remote, dest, nil); err == nil {
		t.Error("Clone() into a non-empty directory succeeded")
	}
	if _, err := Clone(t.Context(), remote, t.TempDir(), nil); err != nil {
		t.Errorf("Clone() into an empty directory error = %v", err)
	}
}

func TestCancelledContextAborts(t *testing.T) {
	_, _, clone := remoteSetup(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if _, err := clone.Fetch(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch() error = %v, want context.Canceled", err)
	}
	if _, err := Clone(ctx, "https://127.0.0.1:1/x.git", filepath.Join(t.TempDir(), "c"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Clone() error = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cancelled calls took %v", d)
	}
}

func TestCloneNeverLeaksCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	var got lines
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := Clone(ctx, "https://user:secret@127.0.0.1:1/x.git", filepath.Join(t.TempDir(), "c"), got.add)
	if err == nil {
		t.Fatal("Clone() of an invalid host succeeded")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(got.joined(), "secret") {
		t.Errorf("credentials leaked: err = %v, lines = %q", err, got.joined())
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"fatal: unable to access 'https://user:secret@127.0.0.1:1/x.git/'": "fatal: unable to access 'https://127.0.0.1:1/x.git/'",
		"Cloning into 'x'...":  "Cloning into 'x'...",
		"ssh://git@host/x.git": "ssh://host/x.git",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}
