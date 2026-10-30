package hostfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/hostfs"
)

// restrictiveUmask sets umask 077 for the test, so that only an explicit chmod can produce
// group or other bits. The umask is process-wide; tests in this package do not run in parallel.
func restrictiveUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
}

func perm(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return fi.Mode().Perm()
}

func TestModesAreExact(t *testing.T) {
	restrictiveUmask(t)
	root := t.TempDir()
	h := hostfs.New()
	ctx := t.Context()
	steps := []struct {
		name string
		do   func() error
		rel  string
		mode fs.FileMode
	}{
		{"mkdir 0755", func() error { return h.Mkdir(ctx, root, "a", 0o755) }, "a", 0o755},
		{"mkdir 0700", func() error { return h.Mkdir(ctx, root, "a/s", 0o700) }, "a/s", 0o700},
		{"create 0600", func() error { return h.CreateExclusive(ctx, root, "a/s/k", []byte("v"), 0o600) }, "a/s/k", 0o600},
		{"create 0444", func() error { return h.CreateExclusive(ctx, root, "a/r", nil, 0o444) }, "a/r", 0o444},
		{"write 0640", func() error { return h.WriteFile(ctx, root, "a/c.yml", []byte("x: 1\n"), 0o640) }, "a/c.yml", 0o640},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if got := perm(t, filepath.Join(root, s.rel)); got != s.mode {
			t.Errorf("%s: mode = %o, want %o", s.name, got, s.mode)
		}
	}
	info, err := h.Stat(ctx, root, "a/s")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if diff := cmp.Diff(homelab.HostInfo{Exists: true, IsDir: true, Mode: 0o700}, info); diff != "" {
		t.Errorf("Stat() mismatch (-want +got):\n%s", diff)
	}
	b, err := h.ReadFile(ctx, root, "a/s/k")
	if err != nil || string(b) != "v" {
		t.Errorf("ReadFile() = %q, %v; want v", b, err)
	}
}

func TestExclusiveKeepsExisting(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "secret")
	if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := hostfs.New()
	err := h.CreateExclusive(t.Context(), root, "secret", []byte("new"), 0o600)
	if !errors.Is(err, homelab.ErrConflict) {
		t.Fatalf("CreateExclusive(existing) error = %v, want ErrConflict", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "original" {
		t.Errorf("content = %q, want original", b)
	}
	if got := perm(t, p); got != 0o644 {
		t.Errorf("mode = %o, want 0644 untouched", got)
	}
	if err := h.Mkdir(t.Context(), root, "secret", 0o700); !errors.Is(err, homelab.ErrConflict) {
		t.Errorf("Mkdir(existing) error = %v, want ErrConflict", err)
	}
}

func TestWriteFileReplacesAtomically(t *testing.T) {
	root := t.TempDir()
	h := hostfs.New()
	ctx := t.Context()
	for _, content := range []string{"one\n", "two\n"} {
		if err := h.WriteFile(ctx, root, "c.yml", []byte(content), 0o640); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(root, "c.yml")); string(b) != content {
			t.Errorf("content = %q, want %q", b, content)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("root holds %d entries, want only c.yml (no temp files left)", len(entries))
	}
}

func TestConfinement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "filelink")); err != nil {
		t.Fatal(err)
	}
	h := hostfs.New()
	ctx := t.Context()
	tests := []struct {
		name string
		do   func() error
		want error
	}{
		{"mkdir through dir symlink", func() error { return h.Mkdir(ctx, root, "dirlink/x", 0o700) }, homelab.ErrValidation},
		{"create through dir symlink", func() error { return h.CreateExclusive(ctx, root, "dirlink/new", []byte("s"), 0o600) }, homelab.ErrValidation},
		{"write through dir symlink", func() error { return h.WriteFile(ctx, root, "dirlink/target", []byte("s"), 0o600) }, homelab.ErrValidation},
		{"create over file symlink", func() error { return h.CreateExclusive(ctx, root, "filelink", []byte("s"), 0o600) }, homelab.ErrConflict},
		{"stat through dir symlink", func() error { _, err := h.Stat(ctx, root, "dirlink/target"); return err }, homelab.ErrValidation},
		{"read file symlink", func() error { _, err := h.ReadFile(ctx, root, "filelink"); return err }, homelab.ErrValidation},
		{"dot-dot mkdir", func() error { return h.Mkdir(ctx, root, "../escaped", 0o700) }, homelab.ErrValidation},
		{"dot-dot create", func() error { return h.CreateExclusive(ctx, root, "a/../../escaped", nil, 0o600) }, homelab.ErrValidation},
		{"absolute rel", func() error { return h.Mkdir(ctx, root, filepath.Join(base, "abs"), 0o700) }, homelab.ErrValidation},
		{"empty rel", func() error { return h.Mkdir(ctx, root, "", 0o700) }, homelab.ErrValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.do(); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
	if b, _ := os.ReadFile(target); string(b) != "outside" {
		t.Errorf("outside target = %q, want untouched", b)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Errorf("outside holds %d entries, want only target", len(entries))
	}
	for _, p := range []string{filepath.Join(base, "escaped"), filepath.Join(base, "abs")} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists: a write escaped the root", p)
		}
	}
}

func TestRoots(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := hostfs.New()
	for _, root := range []string{filepath.Join(base, "missing"), file, "relative/root", "/", ""} {
		if _, err := h.Stat(t.Context(), root, "x"); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("Stat(root %q) error = %v, want ErrValidation", root, err)
		}
		if err := h.Mkdir(t.Context(), root, "x", 0o700); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("Mkdir(root %q) error = %v, want ErrValidation", root, err)
		}
	}
}

func TestAbsentAndBlocked(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := hostfs.New()
	ctx := t.Context()
	if info, err := h.Stat(ctx, root, "nope"); err != nil || info != (homelab.HostInfo{}) {
		t.Errorf("Stat(absent) = %+v, %v; want zero, nil", info, err)
	}
	if _, err := h.ReadFile(ctx, root, "nope"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("ReadFile(absent) error = %v, want ErrNotFound", err)
	}
	if _, err := h.Stat(ctx, root, "f/child"); !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("Stat(under a file) error = %v, want ErrValidation", err)
	}
	if err := h.CreateExclusive(ctx, root, "f/child", nil, 0o600); !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("CreateExclusive(under a file) error = %v, want ErrValidation, not a conflict that reads as present", err)
	}
	if err := h.Mkdir(ctx, root, "missing/child", 0o700); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("Mkdir(missing parent) error = %v, want ErrNotFound", err)
	}
	if info, err := h.Stat(ctx, root, "."); err != nil || !info.IsDir {
		t.Errorf("Stat(root .) = %+v, %v; want a directory", info, err)
	}
}

func TestSymlinksAreReported(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dangling": "nowhere", "inside": "real", "outside": "/etc"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	h := hostfs.New()
	ctx := t.Context()
	for _, name := range []string{"dangling", "inside", "outside"} {
		info, err := h.Stat(ctx, root, name)
		if err != nil || !info.Exists || !info.IsSymlink || info.IsDir {
			t.Errorf("Stat(%s) = %+v, %v; want an existing symlink", name, info, err)
		}
		if _, err := h.ReadFile(ctx, root, name); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("ReadFile(%s) error = %v, want ErrValidation", name, err)
		}
	}
	if err := h.CreateExclusive(ctx, root, "dangling", []byte("s"), 0o600); !errors.Is(err, homelab.ErrConflict) {
		t.Errorf("CreateExclusive(dangling link) error = %v, want ErrConflict", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "nowhere")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("CreateExclusive followed the dangling link")
	}
}

func TestReadFileRefusesFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := hostfs.New().ReadFile(t.Context(), root, "fifo"); !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("ReadFile(fifo) error = %v, want ErrValidation without blocking", err)
	}
}
