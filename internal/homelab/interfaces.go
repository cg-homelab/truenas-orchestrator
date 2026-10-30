package homelab

import (
	"context"
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
}

// SecretStore reads and writes secret values (D5). The plaintext-file implementation lives in
// internal/gitrepo; an encrypted store can replace it without touching call sites.
type SecretStore interface {
	// Read returns the value of key in app's env file, or ErrNotFound.
	Read(ctx context.Context, app, key string) (Secret, error)
	// Write sets key in app's env file to value.
	Write(ctx context.Context, app, key string, value Secret) error
}

// ComposeParser parses compose.yaml bytes. *compose.Parser implements it; it is declared here
// because homelab cannot import compose.
//
// A YAML syntax error is returned as a *ParseError; the pipeline reports any error as COMPOSE002.
// Spec violations are not errors: they are carried in ComposeFile.SchemaErrors.
type ComposeParser interface {
	Parse(path string, src []byte) (ComposeFile, error)
}
