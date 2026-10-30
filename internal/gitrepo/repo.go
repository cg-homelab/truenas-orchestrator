// Package gitrepo is the only package that touches the homelab repo on disk. It runs the git
// binary through os/exec, parses porcelain v2 output, and implements homelab.Repo.
package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

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

// Status runs `git status --porcelain=v2 --branch` and reports the branch and its divergence
// from upstream. M0 parses only the branch header lines.
func (r *Repo) Status(ctx context.Context) (homelab.RepoStatus, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v2", "--branch")
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return homelab.RepoStatus{}, fmt.Errorf("git status: %w: %s", err, msg)
		}
		return homelab.RepoStatus{}, fmt.Errorf("git status: %w", err)
	}
	st, err := parseStatus(out)
	if err != nil {
		return homelab.RepoStatus{}, fmt.Errorf("git status: %w", err)
	}
	return st, nil
}

// errNoBranchHeader means the porcelain output had no "# branch.head" line.
var errNoBranchHeader = errors.New("no branch.head header in porcelain output")

// parseStatus reads the "# branch.*" header lines of `git status --porcelain=v2 --branch`.
// Lines it does not need (branch.oid, changed entries) are ignored.
func parseStatus(out []byte) (homelab.RepoStatus, error) {
	var st homelab.RepoStatus
	sawHead := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
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
