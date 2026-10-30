// Package hostfs is the host filesystem as setup writes it: every operation is confined to a
// storage root by os.Root, so neither ".." nor a symlink can carry a write outside the root.
// It implements homelab.SetupHost.
package hostfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Host performs setup's filesystem operations. The zero value is ready to use; it holds no
// state, and each call opens its root afresh.
type Host struct{}

var _ homelab.SetupHost = (*Host)(nil)

// New returns a Host.
func New() *Host { return &Host{} }

// Stat describes rel under root without following a symlink at its final component, so a
// dangling link is reported as a link rather than as absent. An absent path is HostInfo{} with a
// nil error.
func (h *Host) Stat(_ context.Context, root, rel string) (homelab.HostInfo, error) {
	r, name, err := open(root, rel)
	if err != nil {
		return homelab.HostInfo{}, err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return homelab.HostInfo{}, nil
	}
	if err != nil {
		return homelab.HostInfo{}, translate("stat", rel, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return homelab.HostInfo{Exists: true, IsSymlink: true, Mode: homelab.FileMode(fi.Mode().Perm())}, nil
	}
	return homelab.HostInfo{Exists: true, IsDir: fi.IsDir(), Mode: homelab.FileMode(fi.Mode().Perm())}, nil
}

// ReadFile returns the content of the regular file rel under root: an error wrapping
// homelab.ErrNotFound when it is absent, and one wrapping homelab.ErrValidation when it is a
// symlink, a directory, a FIFO or anything else that is not a regular file.
func (h *Host) ReadFile(_ context.Context, root, rel string) ([]byte, error) {
	r, name, err := open(root, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, translate("read", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("read %s: not a regular file: %w", rel, homelab.ErrValidation)
	}
	b, err := r.ReadFile(name)
	if err != nil {
		return nil, translate("read", rel, err)
	}
	return b, nil
}

// Mkdir creates the directory rel under root and sets exactly mode, which the umask may have
// narrowed. An existing rel is an error wrapping homelab.ErrConflict and is not changed.
func (h *Host) Mkdir(_ context.Context, root, rel string, mode homelab.FileMode) error {
	r, name, err := open(root, rel)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	perm := fs.FileMode(mode) & fs.ModePerm
	if err := r.Mkdir(name, perm); err != nil {
		return translate("mkdir", rel, err)
	}
	if err := r.Chmod(name, perm); err != nil {
		return translate("chmod", rel, err)
	}
	return nil
}

// CreateExclusive creates the file rel under root with O_EXCL, writes data and sets exactly
// mode. An existing rel is an error wrapping homelab.ErrConflict and is left byte-identical.
func (h *Host) CreateExclusive(_ context.Context, root, rel string, data []byte, mode homelab.FileMode) error {
	r, name, err := open(root, rel)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return createFile(r, name, rel, data, fs.FileMode(mode)&fs.ModePerm)
}

// WriteFile atomically creates or replaces the file rel under root: it writes a temporary file
// in the same directory, sets exactly mode, and renames it over rel within the root.
func (h *Host) WriteFile(_ context.Context, root, rel string, data []byte, mode homelab.FileMode) error {
	r, name, err := open(root, rel)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	tmp, err := tempName(name)
	if err != nil {
		return err
	}
	if err := createFile(r, tmp, rel, data, fs.FileMode(mode)&fs.ModePerm); err != nil {
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		_ = r.Remove(tmp)
		return translate("rename", rel, err)
	}
	return nil
}

// createFile creates name exclusively, writes data, checks Close and sets perm. A file it
// created but could not complete is removed.
func createFile(r *os.Root, name, rel string, data []byte, perm fs.FileMode) error {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return translate("create", rel, err)
	}
	_, werr := f.Write(data)
	// Sync before Close: after a power loss, an empty secret file would be "present" and never
	// overwritten.
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = r.Remove(name)
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := r.Chmod(name, perm); err != nil {
		_ = r.Remove(name)
		return translate("chmod", rel, err)
	}
	return nil
}

// tempName returns a hidden, random sibling of name.
func tempName(name string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("temp name: %w", err)
	}
	dir, base := filepath.Split(name)
	return filepath.Join(dir, "."+base+".tmp-"+hex.EncodeToString(b[:])), nil
}

// open validates root and rel and opens root. root must be an absolute, existing directory
// other than "/"; rel must be a relative slash path with no ".." segment (os.Root would also
// refuse an escape, but only once it reaches it). Both failures wrap homelab.ErrValidation.
func open(root, rel string) (*os.Root, string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return nil, "", fmt.Errorf("storage root %s must be an absolute directory other than /: %w", root, homelab.ErrValidation)
	}
	if rel == "" || path.IsAbs(rel) || filepath.IsAbs(rel) || slices.Contains(strings.Split(rel, "/"), "..") {
		return nil, "", fmt.Errorf("path %q must be relative to its storage root, with no .. segment: %w", rel, homelab.ErrValidation)
	}
	fi, err := os.Stat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", fmt.Errorf("storage root %s does not exist: %w", root, homelab.ErrValidation)
	case err != nil:
		return nil, "", fmt.Errorf("stat storage root %s: %w", root, err)
	case !fi.IsDir():
		return nil, "", fmt.Errorf("storage root %s is not a directory: %w", root, homelab.ErrValidation)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", fmt.Errorf("open storage root %s: %w", root, err)
	}
	return r, filepath.FromSlash(rel), nil
}

// translate maps an os.Root error to the homelab sentinels. os.Root exposes no sentinel for an
// escape, so its message is matched, as gitrepo does.
func translate(op, rel string, err error) error {
	switch {
	case strings.Contains(err.Error(), "path escapes from parent"):
		return fmt.Errorf("%s %s: escapes the storage root: %w", op, rel, homelab.ErrValidation)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%s %s: already exists: %w", op, rel, homelab.ErrConflict)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s %s: %w", op, rel, homelab.ErrNotFound)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%s %s: a parent is not a directory: %w", op, rel, homelab.ErrValidation)
	default:
		return fmt.Errorf("%s %s: %w", op, rel, err)
	}
}
