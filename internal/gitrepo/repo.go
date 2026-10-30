// Package gitrepo is the only package that touches the homelab repo on disk. It runs the git
// binary through os/exec, parses porcelain v2 output, and implements homelab.Repo.
package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Repo is a homelab repo checked out at a directory on disk.
type Repo struct {
	root string
}

var _ homelab.Repo = (*Repo)(nil)

// New returns a Repo for the working tree at root. It does not touch the disk.
func New(root string) *Repo {
	return &Repo{root: root}
}

// Root returns the working tree's root directory.
func (r *Repo) Root() string { return r.root }

// Status runs `git status --porcelain=v2 --branch` and reports the branch, its divergence from
// upstream, whether tracked files have uncommitted changes, and when the repo was last fetched.
func (r *Repo) Status(ctx context.Context) (homelab.RepoStatus, error) {
	out, err := r.git(ctx, "--no-optional-locks", "status", "--porcelain=v2", "--branch")
	if err != nil {
		return homelab.RepoStatus{}, err
	}
	st, err := parseStatus(out)
	if err != nil {
		return homelab.RepoStatus{}, fmt.Errorf("git status: %w", err)
	}
	st.LastFetch, err = r.lastFetch(ctx)
	if err != nil {
		return homelab.RepoStatus{}, err
	}
	return st, nil
}

// lastFetch returns the mtime of FETCH_HEAD in the repo's git directory, or the zero time when the
// repo has never been fetched.
func (r *Repo) lastFetch(ctx context.Context) (time.Time, error) {
	out, err := r.git(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return time.Time{}, err
	}
	fi, err := os.Stat(filepath.Join(strings.TrimSpace(string(out)), "FETCH_HEAD"))
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("stat FETCH_HEAD: %w", err)
	}
	return fi.ModTime(), nil
}

// Apps lists every directory directly under compose/, sorted. Names that break the layout
// contract are included; the validator reports them (COMPOSE006). It returns nil when compose/
// does not exist.
func (r *Repo) Apps(_ context.Context) ([]string, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, fmt.Errorf("open repo root: %w", err)
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open("compose")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open compose dir: %w", err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read compose dir: %w", err)
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() {
			apps = append(apps, e.Name())
		}
	}
	slices.Sort(apps)
	return apps, nil
}

// ReadFile returns the contents of rel, a slash path relative to the repo root. A missing file is
// homelab.ErrNotFound. A path that escapes the root (by ".." or symlink), a directory, or any other
// non-regular file is homelab.ErrUnreadable.
func (r *Repo) ReadFile(_ context.Context, rel string) ([]byte, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, fmt.Errorf("open repo root: %w", err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.FromSlash(rel)
	fi, err := root.Stat(name)
	if err != nil {
		return nil, readError("read", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("read %s: not a regular file: %w", rel, homelab.ErrUnreadable)
	}
	b, err := root.ReadFile(name)
	if err != nil {
		return nil, readError("read", rel, err)
	}
	return b, nil
}

// Stat reports whether rel exists and whether it is a directory. A missing path is
// {Exists: false}, nil; an escape from the repo root is homelab.ErrUnreadable.
func (r *Repo) Stat(_ context.Context, rel string) (homelab.FileInfo, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return homelab.FileInfo{}, fmt.Errorf("open repo root: %w", err)
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Stat(filepath.FromSlash(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return homelab.FileInfo{}, nil
	}
	if err != nil {
		return homelab.FileInfo{}, readError("stat", rel, err)
	}
	return homelab.FileInfo{Exists: true, IsDir: fi.IsDir()}, nil
}

// readError translates an os.Root error: not-exist becomes ErrNotFound and an escape from the root
// becomes ErrUnreadable. os.Root exposes no sentinel for escapes, so the message is matched.
func readError(op, rel string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s %s: %w", op, rel, homelab.ErrNotFound)
	case strings.Contains(err.Error(), "path escapes from parent"):
		return fmt.Errorf("%s %s: escapes the repo: %w", op, rel, homelab.ErrUnreadable)
	default:
		return fmt.Errorf("%s %s: %w", op, rel, err)
	}
}

// Tracked reports whether git tracks rel.
func (r *Repo) Tracked(ctx context.Context, rel string) (bool, error) {
	_, err := r.git(ctx, "ls-files", "--error-unmatch", "--", rel)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// git runs git in the repo root and returns stdout. Errors carry git's stderr and wrap the
// underlying *exec.ExitError.
func (r *Repo) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // callers pass fixed subcommands; user paths follow "--"
	cmd.Dir = r.root
	// Read-only: never let git refresh the index or take optional locks.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", gitName(args), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", gitName(args), err)
	}
	return out, nil
}

// errNoBranchHeader means the porcelain output had no "# branch.head" line.
var errNoBranchHeader = errors.New("no branch.head header in porcelain output")

// parseStatus reads the "# branch.*" header lines of `git status --porcelain=v2 --branch`.
// Changed-entry lines for tracked files ("1 ", "2 ", "u ") set Dirty; untracked ("? ") and
// ignored entries do not. Other lines are ignored.
func parseStatus(out []byte) (homelab.RepoStatus, error) {
	var st homelab.RepoStatus
	sawHead := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") || strings.HasPrefix(line, "u ") {
			st.Dirty = true
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(sc.Text(), "# "), " ")
		if !ok {
			continue
		}
		switch key {
		case "branch.head":
			st.Branch, sawHead = value, true
		case "branch.upstream":
			st.Upstream = value
		case "branch.ab":
			ahead, behind, err := parseAheadBehind(value)
			if err != nil {
				return homelab.RepoStatus{}, err
			}
			st.Ahead, st.Behind = ahead, behind
		}
	}
	if err := sc.Err(); err != nil {
		return homelab.RepoStatus{}, fmt.Errorf("read porcelain output: %w", err)
	}
	if !sawHead {
		return homelab.RepoStatus{}, errNoBranchHeader
	}
	return st, nil
}

// parseAheadBehind parses the value of a branch.ab line: "+<ahead> -<behind>".
func parseAheadBehind(v string) (ahead, behind int, err error) {
	a, b, ok := strings.Cut(v, " ")
	if !ok || !strings.HasPrefix(a, "+") || !strings.HasPrefix(b, "-") {
		return 0, 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	ahead, err = strconv.Atoi(a[1:])
	if err != nil {
		return 0, 0, fmt.Errorf("malformed branch.ab ahead %q: %w", v, err)
	}
	behind, err = strconv.Atoi(b[1:])
	if err != nil {
		return 0, 0, fmt.Errorf("malformed branch.ab behind %q: %w", v, err)
	}
	return ahead, behind, nil
}

// gitName returns the subcommand in args, skipping leading global options.
func gitName(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return args[0]
}
