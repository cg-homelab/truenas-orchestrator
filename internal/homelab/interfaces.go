package homelab

import (
	"context"
	"fmt"
	"time"
)

// TrueNAS is the subset of the TrueNAS middleware API the domain calls. *truenas.Client and
// truenas.Fake implement it. Methods are added as milestones need them.
type TrueNAS interface {
	// SystemVersion returns the TrueNAS SCALE version string.
	SystemVersion(ctx context.Context) (string, error)
}

// RepoStatus is the git state of the homelab repo.
type RepoStatus struct {
	// Branch is the checked-out branch, or "(detached)" for a detached HEAD.
	Branch string `json:"branch"`
	// Upstream is the tracking branch, empty when none is configured.
	Upstream string `json:"upstream"`
	// Ahead and Behind count commits relative to Upstream; both are 0 without one.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Dirty reports uncommitted changes to tracked files.
	Dirty bool `json:"dirty"`
	// LastFetch is when the repo last fetched from its remote; zero when unknown.
	LastFetch time.Time `json:"last_fetch,omitzero"`
}

// FileInfo is what the domain needs to know about a path in the repo.
type FileInfo struct {
	Exists bool `json:"exists"`
	IsDir  bool `json:"is_dir"`
}

// Repo is the homelab repo as the domain sees it. *gitrepo.Repo implements it against git on
// disk; tests use an in-memory fake. Every rel path is slash-separated and relative to Root.
type Repo interface {
	// Root returns the repo's root directory.
	Root() string
	// Status reports the branch, its divergence from upstream and the dirty state.
	Status(ctx context.Context) (RepoStatus, error)
	// Apps returns the sorted directory names under compose/.
	Apps(ctx context.Context) ([]string, error)
	// ReadFile returns the content of rel, or an error wrapping ErrNotFound when it is absent.
	ReadFile(ctx context.Context, rel string) ([]byte, error)
	// Stat describes rel; an absent path is FileInfo{} with a nil error.
	Stat(ctx context.Context, rel string) (FileInfo, error)
	// Tracked reports whether git tracks rel.
	Tracked(ctx context.Context, rel string) (bool, error)
	// Ignored reports whether a .gitignore rule matches rel, whether or not git tracks it.
	Ignored(ctx context.Context, rel string) (bool, error)
}

// SecretStore is the only path to reading and writing compose/<app>/.env (D5). The
// plaintext-file implementation is gitrepo.EnvStore; an encrypted store can replace it without
// touching call sites. Content is the whole file, because a .env keeps comments and key order
// that a key-value interface would lose.
type SecretStore interface {
	// ReadEnv returns the content of app's env file, or an error wrapping ErrNotFound.
	ReadEnv(ctx context.Context, app string) ([]byte, error)
	// WriteEnv replaces app's env file, creating it with mode 0600. It returns an error wrapping
	// ErrTracked when git tracks the file, and ErrNotIgnored when no .gitignore rule covers it: an
	// env file that is not safely ignored is never written (GIT001).
	WriteEnv(ctx context.Context, app string, content []byte) error
}

// FileMode is a Unix permission mode such as 0o750. homelab cannot import io/fs.
type FileMode uint32

// String formats m as four octal digits, e.g. 0750.
func (m FileMode) String() string { return fmt.Sprintf("%04o", uint32(m)) }

// HostInfo describes a path under a storage root. The final component is not followed: a
// symbolic link there is reported as IsSymlink, with IsDir false, whatever it points at.
type HostInfo struct {
	Exists    bool
	IsDir     bool
	IsSymlink bool
	// Mode holds the permission bits; zero when the path does not exist.
	Mode FileMode
}

// SetupHost performs the filesystem reads and writes of a setup run, each confined to a storage
// root. root is an absolute directory that must already exist (setup never creates a root); rel
// is a non-empty slash path relative to it. A root that is relative, "/", missing or not a
// directory, and a rel that escapes the root (by "..", an absolute name or a symlink), are
// errors wrapping ErrValidation. *hostfs.Host implements it with os.Root; tests use a fake.
type SetupHost interface {
	// Stat describes rel without following a symlink at its final component; an absent path is
	// HostInfo{} with a nil error. A parent that is not a directory is an error wrapping
	// ErrValidation.
	Stat(ctx context.Context, root, rel string) (HostInfo, error)
	// ReadFile returns the content of the regular file rel, an error wrapping ErrNotFound when it
	// is absent, or one wrapping ErrValidation when it is not a regular file (a symlink, a FIFO).
	ReadFile(ctx context.Context, root, rel string) ([]byte, error)
	// Mkdir creates the single directory rel with exactly mode; its parent must exist. It returns
	// an error wrapping ErrConflict when rel already exists.
	Mkdir(ctx context.Context, root, rel string, mode FileMode) error
	// CreateExclusive creates the file rel holding data with exactly mode. It returns an error
	// wrapping ErrConflict, and leaves the file untouched, when rel already exists.
	CreateExclusive(ctx context.Context, root, rel string, data []byte, mode FileMode) error
	// WriteFile atomically creates or replaces the file rel with data and exactly mode.
	WriteFile(ctx context.Context, root, rel string, data []byte, mode FileMode) error
}

// ComposeParser parses compose.yaml bytes. *compose.Parser implements it; it is declared here
// because homelab cannot import compose.
//
// A YAML syntax error is returned as a *ParseError; the pipeline reports any error as COMPOSE002.
// Spec violations are not errors: they are carried in ComposeFile.SchemaErrors.
type ComposeParser interface {
	Parse(path string, src []byte) (ComposeFile, error)
}
