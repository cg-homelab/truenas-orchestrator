package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// ErrNotFastForward means a fast-forward-only pull was refused: the local branch has diverged from
// its upstream, or the incoming changes conflict with local ones. The working tree is untouched.
var ErrNotFastForward = errors.New("not a fast-forward")

// ErrNotRepoRoot means the repo's root directory is not the top level of a git repository, so a
// fetch would act on an enclosing repository instead.
var ErrNotRepoRoot = errors.New("not the top level of a git repository")

// stderrTail is how many trailing output lines accompany a failed command's error.
const stderrTail = 10

// credentials matches the "user:pass@" part of a URL.
var credentials = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://)[^/@\s]+@`)

// redact strips URL credentials from s.
func redact(s string) string {
	return credentials.ReplaceAllString(s, "${1}")
}

// Fetch runs `git fetch --prune` and, when the current branch has an upstream, a fast-forward-only
// pull. Every output line is passed to report (which may be nil) as it arrives. A pull that cannot
// fast-forward returns an error wrapping ErrNotFastForward and leaves the working tree untouched.
func (r *Repo) Fetch(ctx context.Context, report func(line string)) (homelab.RepoStatus, error) {
	if err := r.checkTopLevel(ctx); err != nil {
		return homelab.RepoStatus{}, err
	}
	env := r.sshEnv()
	if err := runStreaming(ctx, r.root, env, report, "fetch", "--prune"); err != nil {
		return homelab.RepoStatus{}, err
	}
	if _, err := r.git(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if err := runStreaming(ctx, r.root, env, report, "pull", "--ff-only"); err != nil {
			return homelab.RepoStatus{}, err
		}
	} else if ctx.Err() != nil {
		return homelab.RepoStatus{}, ctx.Err()
	}
	return r.Status(ctx)
}

// checkTopLevel refuses to proceed unless the repo root is itself the top level of a git
// repository, so a root inside someone else's checkout is never fetched or pulled.
func (r *Repo) checkTopLevel(ctx context.Context) error {
	out, err := r.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top := strings.TrimSpace(string(out))
	want, err := canonical(r.root)
	if err != nil {
		return err
	}
	got, err := canonical(top)
	if err != nil {
		return err
	}
	if want != got {
		return fmt.Errorf("%s is not the top level of a git repository (it is inside %s); refusing to fetch: %w", r.root, top, ErrNotRepoRoot)
	}
	return nil
}

func canonical(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	return filepath.Clean(resolved), nil
}

// Clone runs `git clone --progress url dest`, passing every output line to report (which may be
// nil). dest must not exist or must be an empty directory. Credentials in url are never reported
// or included in an error.
func Clone(ctx context.Context, url, dest string, report func(line string)) (*Repo, error) {
	abs, err := filepath.Abs(dest)
	if err != nil {
		return nil, fmt.Errorf("resolve clone destination: %w", err)
	}
	entries, err := os.ReadDir(abs)
	switch {
	case err == nil && len(entries) > 0:
		return nil, fmt.Errorf("clone into %s: directory is not empty", abs)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("clone into %s: %w", abs, err)
	}
	if err := runStreaming(ctx, "", nil, report, "clone", "--progress", "--", url, abs); err != nil {
		return nil, err
	}
	return New(abs), nil
}

// runStreaming runs git with args in dir (the current directory when empty) and env added to the
// process environment, passing each line of stdout and stderr to report as it arrives. Unlike
// Repo.git it allows git to take locks, and it never prompts for credentials. On failure the
// error carries the redacted tail of the output.
func runStreaming(ctx context.Context, dir string, env []string, report func(string), args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // callers pass fixed subcommands and flags
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.WaitDelay = 2 * time.Second
	lw := &lineWriter{report: report}
	cmd.Stdout, cmd.Stderr = lw, lw
	err := cmd.Run()
	lw.flush()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("git %s: %w", gitName(args), ctx.Err())
	}
	tail := lw.tail()
	base := error(fmt.Errorf("git %s: %w", gitName(args), err))
	if tail != "" {
		base = fmt.Errorf("%w: %s", base, tail)
	}
	if gitName(args) == "pull" && isNotFastForward(tail) {
		return fmt.Errorf("%w: %w", ErrNotFastForward, base)
	}
	return base
}

// isNotFastForward reports whether pull output describes a refused fast-forward or a conflict with
// local changes.
func isNotFastForward(out string) bool {
	for _, s := range []string{"fast-forward", "divergent branches", "would be overwritten", "unmerged files"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// lineWriter splits written bytes into lines on \n or \r (git redraws progress with \r), redacts
// credentials, passes each line to report and keeps the last few for error messages. It is safe for
// concurrent use because exec copies stdout and stderr on separate goroutines.
type lineWriter struct {
	report func(string)

	mu   sync.Mutex
	buf  []byte
	last []string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush emits any trailing partial line.
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// emit must be called with mu held.
func (w *lineWriter) emit(line string) {
	line = strings.TrimSpace(redact(line))
	if line == "" {
		return
	}
	if w.report != nil {
		w.report(line)
	}
	w.last = append(w.last, line)
	if len(w.last) > stderrTail {
		w.last = w.last[1:]
	}
}

func (w *lineWriter) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.last, "; ")
}
