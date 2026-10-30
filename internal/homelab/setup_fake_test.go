package homelab_test

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// fakeNode is a file or directory in fakeHost.
type fakeNode struct {
	dir  bool
	link bool // a symbolic link, possibly dangling
	data []byte
	mode homelab.FileMode
}

// fakeHost is an in-memory homelab.SetupHost keyed by absolute path. It records every write.
type fakeHost struct {
	roots map[string]bool
	nodes map[string]*fakeNode
	// writes logs "<op> <path>" for mkdir, create and write calls that changed something.
	writes []string
	// refuse makes Stat of these paths fail like an escaping symlink.
	refuse map[string]bool
	// beforeWrite, when set, runs at the start of every write call.
	beforeWrite func(op, p string)
}

var _ homelab.SetupHost = (*fakeHost)(nil)

func newFakeHost(roots ...string) *fakeHost {
	h := &fakeHost{roots: map[string]bool{}, nodes: map[string]*fakeNode{}, refuse: map[string]bool{}}
	for _, r := range roots {
		h.roots[r] = true
	}
	return h
}

// symlink adds a dangling symbolic link at the absolute path p, creating parent directories.
func (h *fakeHost) symlink(p string) {
	h.file(p, "", 0o777)
	h.nodes[p].link = true
}

// file adds a file at the absolute path p, creating parent directories.
func (h *fakeHost) file(p, data string, mode homelab.FileMode) {
	for d := path.Dir(p); !h.roots[d] && d != "/"; d = path.Dir(d) {
		if h.nodes[d] == nil {
			h.nodes[d] = &fakeNode{dir: true, mode: 0o755}
		}
	}
	h.nodes[p] = &fakeNode{data: []byte(data), mode: mode}
}

func (h *fakeHost) resolve(root, rel string) (string, error) {
	if !h.roots[root] {
		return "", fmt.Errorf("storage root %s does not exist: %w", root, homelab.ErrValidation)
	}
	c := path.Clean(rel)
	if rel == "" || path.IsAbs(rel) || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%s escapes the storage root: %w", rel, homelab.ErrValidation)
	}
	p := path.Join(root, c)
	if h.refuse[p] {
		return "", fmt.Errorf("%s escapes the storage root: %w", rel, homelab.ErrValidation)
	}
	return p, nil
}

// parentOK reports whether p's parent is the root or an existing directory.
func (h *fakeHost) parentOK(root, p string) error {
	d := path.Dir(p)
	if d == root {
		return nil
	}
	n := h.nodes[d]
	switch {
	case n == nil:
		return fmt.Errorf("parent of %s: %w", p, homelab.ErrNotFound)
	case !n.dir:
		return fmt.Errorf("parent of %s is a file: %w", p, homelab.ErrValidation)
	}
	return nil
}

func (h *fakeHost) Stat(_ context.Context, root, rel string) (homelab.HostInfo, error) {
	p, err := h.resolve(root, rel)
	if err != nil {
		return homelab.HostInfo{}, err
	}
	if p == root {
		return homelab.HostInfo{Exists: true, IsDir: true, Mode: 0o755}, nil
	}
	for d := path.Dir(p); d != root && d != "/"; d = path.Dir(d) {
		if n := h.nodes[d]; n != nil && !n.dir {
			return homelab.HostInfo{}, fmt.Errorf("stat %s: a parent is not a directory: %w", rel, homelab.ErrValidation)
		}
	}
	n := h.nodes[p]
	switch {
	case n == nil:
		return homelab.HostInfo{}, nil
	case n.link:
		return homelab.HostInfo{Exists: true, IsSymlink: true, Mode: n.mode}, nil
	}
	return homelab.HostInfo{Exists: true, IsDir: n.dir, Mode: n.mode}, nil
}

func (h *fakeHost) ReadFile(_ context.Context, root, rel string) ([]byte, error) {
	p, err := h.resolve(root, rel)
	if err != nil {
		return nil, err
	}
	n := h.nodes[p]
	switch {
	case n == nil:
		return nil, fmt.Errorf("read %s: %w", rel, homelab.ErrNotFound)
	case n.dir || n.link:
		return nil, fmt.Errorf("read %s: not a regular file: %w", rel, homelab.ErrValidation)
	}
	return slices.Clone(n.data), nil
}

func (h *fakeHost) write(op, root, rel string, node *fakeNode, exclusive bool) error {
	p, err := h.resolve(root, rel)
	if err != nil {
		return err
	}
	if h.beforeWrite != nil {
		h.beforeWrite(op, p)
	}
	if err := h.parentOK(root, p); err != nil {
		return err
	}
	if n := h.nodes[p]; n != nil && (exclusive || n.dir) {
		return fmt.Errorf("%s %s: already exists: %w", op, rel, homelab.ErrConflict)
	}
	h.nodes[p] = node
	h.writes = append(h.writes, op+" "+p)
	return nil
}

func (h *fakeHost) Mkdir(_ context.Context, root, rel string, mode homelab.FileMode) error {
	return h.write("mkdir", root, rel, &fakeNode{dir: true, mode: mode}, true)
}

func (h *fakeHost) CreateExclusive(_ context.Context, root, rel string, data []byte, mode homelab.FileMode) error {
	return h.write("create", root, rel, &fakeNode{data: slices.Clone(data), mode: mode}, true)
}

func (h *fakeHost) WriteFile(_ context.Context, root, rel string, data []byte, mode homelab.FileMode) error {
	return h.write("write", root, rel, &fakeNode{data: slices.Clone(data), mode: mode}, false)
}

// fakeStore is an in-memory homelab.SecretStore keyed by app.
type fakeStore struct {
	files   map[string][]byte
	tracked map[string]bool
	writes  int
}

var _ homelab.SecretStore = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{files: map[string][]byte{}, tracked: map[string]bool{}}
}

func (s *fakeStore) ReadEnv(_ context.Context, app string) ([]byte, error) {
	b, ok := s.files[app]
	if !ok {
		return nil, fmt.Errorf("read compose/%s/.env: %w", app, homelab.ErrNotFound)
	}
	return slices.Clone(b), nil
}

func (s *fakeStore) WriteEnv(_ context.Context, app string, content []byte) error {
	if s.tracked[app] {
		return fmt.Errorf("write compose/%s/.env: %w", app, homelab.ErrTracked)
	}
	s.files[app] = slices.Clone(content)
	s.writes++
	return nil
}
