package homelab_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// fakeRepo is an in-memory homelab.Repo: files by slash path, a tracked set and a status.
type fakeRepo struct {
	root    string
	files   map[string][]byte
	tracked map[string]bool
	status  homelab.RepoStatus
	// unreadable paths report homelab.ErrUnreadable, like a symlink leaving the repo.
	unreadable map[string]bool
	// notIgnored paths have no .gitignore rule; every other path is ignored.
	notIgnored map[string]bool
}

var _ homelab.Repo = (*fakeRepo)(nil)

// fixtureRoot is the shared fixture homelab repo, relative to this package.
const fixtureRoot = "../../fixtures/homelab-repo"

// loadFixture builds a fakeRepo from the directory tree at dir.
func loadFixture(t *testing.T, dir string) *fakeRepo {
	t.Helper()
	r := &fakeRepo{root: "/srv/homelab", files: map[string][]byte{}, tracked: map[string]bool{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r.files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("load fixture %s: %v", dir, err)
	}
	return r
}

func (r *fakeRepo) Root() string { return r.root }

func (r *fakeRepo) Status(context.Context) (homelab.RepoStatus, error) { return r.status, nil }

func (r *fakeRepo) Apps(context.Context) ([]string, error) {
	seen := map[string]bool{}
	for p := range r.files {
		rest, ok := strings.CutPrefix(p, "compose/")
		if !ok {
			continue
		}
		if dir, _, nested := strings.Cut(rest, "/"); nested {
			seen[dir] = true
		}
	}
	apps := make([]string, 0, len(seen))
	for a := range seen {
		apps = append(apps, a)
	}
	slices.Sort(apps)
	return apps, nil
}

func (r *fakeRepo) ReadFile(_ context.Context, rel string) ([]byte, error) {
	if r.unreadable[path.Clean(rel)] {
		return nil, fmt.Errorf("read %s: %w", rel, homelab.ErrUnreadable)
	}
	b, ok := r.files[path.Clean(rel)]
	if !ok {
		return nil, fmt.Errorf("read %s: %w", rel, homelab.ErrNotFound)
	}
	return b, nil
}

func (r *fakeRepo) Stat(_ context.Context, rel string) (homelab.FileInfo, error) {
	rel = path.Clean(rel)
	if r.unreadable[rel] {
		return homelab.FileInfo{}, fmt.Errorf("stat %s: %w", rel, homelab.ErrUnreadable)
	}
	if _, ok := r.files[rel]; ok {
		return homelab.FileInfo{Exists: true}, nil
	}
	for p := range r.files {
		if strings.HasPrefix(p, rel+"/") {
			return homelab.FileInfo{Exists: true, IsDir: true}, nil
		}
	}
	return homelab.FileInfo{}, nil
}

func (r *fakeRepo) Tracked(_ context.Context, rel string) (bool, error) {
	return r.tracked[path.Clean(rel)], nil
}

func (r *fakeRepo) Ignored(_ context.Context, rel string) (bool, error) {
	return !r.notIgnored[path.Clean(rel)], nil
}
