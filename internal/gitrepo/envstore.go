package gitrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// envFileMode is the mode of every env file EnvStore writes.
const envFileMode = 0o600

// EnvStore is the plaintext homelab.SecretStore: each app's secrets live in compose/<app>/.env
// in the repo's working tree, which must be gitignored (D5). Every read and write goes through
// os.Root.
type EnvStore struct {
	repo *Repo
}

var _ homelab.SecretStore = (*EnvStore)(nil)

// NewEnvStore returns the env file store of repo.
func NewEnvStore(repo *Repo) *EnvStore { return &EnvStore{repo: repo} }

func envRel(app string) (string, error) {
	if !homelab.ValidAppName(app) {
		return "", fmt.Errorf("app name %q is not a valid app directory name: %w", app, homelab.ErrValidation)
	}
	return "compose/" + app + "/.env", nil
}

// ReadEnv returns the content of compose/<app>/.env: homelab.ErrNotFound when it is absent,
// homelab.ErrUnreadable when it is a symlink leaving the repo or not a regular file.
func (s *EnvStore) ReadEnv(ctx context.Context, app string) ([]byte, error) {
	rel, err := envRel(app)
	if err != nil {
		return nil, err
	}
	return s.repo.ReadFile(ctx, rel)
}

// WriteEnv atomically replaces compose/<app>/.env with content, mode 0600. It refuses, with an
// error wrapping homelab.ErrTracked, when git tracks the file, and with homelab.ErrNotIgnored
// when no .gitignore rule covers it (GIT001); with homelab.ErrUnreadable when the existing file
// is a symlink or not a regular file, since ReadEnv would follow it; and with homelab.ErrNotFound
// when the app directory does not exist. Errors never hold content.
//
// The content is written to compose/<app>/.env.tmp-<random>, synced, and renamed over .env.
// The temporary name is not covered by a compose/*/.env ignore rule, so a crash between create
// and rename can leave an un-ignored plaintext file; the window is the write itself, and the
// next WriteEnv for the app removes any such leftovers before writing.
func (s *EnvStore) WriteEnv(ctx context.Context, app string, content []byte) error {
	rel, err := envRel(app)
	if err != nil {
		return err
	}
	tracked, err := s.repo.Tracked(ctx, rel)
	if err != nil {
		return fmt.Errorf("check whether %s is tracked: %w", rel, err)
	}
	if tracked {
		return fmt.Errorf("write %s: %w", rel, homelab.ErrTracked)
	}
	ignored, err := s.repo.Ignored(ctx, rel)
	if err != nil {
		return fmt.Errorf("check whether %s is gitignored: %w", rel, err)
	}
	if !ignored {
		return fmt.Errorf("write %s: %w", rel, homelab.ErrNotIgnored)
	}
	root, err := os.OpenRoot(s.repo.root)
	if err != nil {
		return fmt.Errorf("open repo root: %w", err)
	}
	defer func() { _ = root.Close() }()

	name := filepath.FromSlash(rel)
	switch fi, err := root.Lstat(name); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return readError("write", rel, err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("write %s: existing file is a symlink or not a regular file: %w", rel, homelab.ErrUnreadable)
	}
	dir := filepath.Dir(name)
	if err := removeStaleTemps(root, dir); err != nil {
		return readError("write", rel, err)
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("temp name: %w", err)
	}
	tmp := filepath.Join(dir, envTempPrefix+hex.EncodeToString(b[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, envFileMode)
	if err != nil {
		return readError("write", rel, err)
	}
	_, werr := f.Write(content)
	serr := f.Sync()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := root.Chmod(tmp, envFileMode); err != nil {
		_ = root.Remove(tmp)
		return readError("chmod", rel, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return readError("rename", rel, err)
	}
	return nil
}

// envTempPrefix names WriteEnv's temporary files.
const envTempPrefix = ".env.tmp-"

// removeStaleTemps deletes regular files named envTempPrefix* in dir, left by an interrupted
// WriteEnv.
func removeStaleTemps(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), envTempPrefix) && e.Type().IsRegular() {
			if err := root.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
