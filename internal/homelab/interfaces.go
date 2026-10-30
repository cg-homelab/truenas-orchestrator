package homelab

import "context"

// TrueNAS is the subset of the TrueNAS middleware API the domain calls. *truenas.Client and
// truenas.Fake implement it. Methods are added as milestones need them.
type TrueNAS interface {
	// SystemVersion returns the TrueNAS SCALE version string.
	SystemVersion(ctx context.Context) (string, error)
}

// RepoStatus is the git state of the homelab repo.
type RepoStatus struct {
	// Branch is the checked-out branch, or "(detached)" for a detached HEAD.
	Branch string
	// Upstream is the tracking branch, empty when none is configured.
	Upstream string
	// Ahead and Behind count commits relative to Upstream; both are 0 without one.
	Ahead  int
	Behind int
}

// Repo is the homelab repo as the domain sees it. *gitrepo.Repo implements it against git on
// disk; tests use an in-memory fake.
type Repo interface {
	// Root returns the repo's root directory.
	Root() string
	// Status reports the branch and its divergence from upstream.
	Status(ctx context.Context) (RepoStatus, error)
}

// SecretStore reads and writes secret values (D5). The plaintext-file implementation lives in
// internal/gitrepo; an encrypted store can replace it without touching call sites.
type SecretStore interface {
	// Read returns the value of key in app's env file, or ErrNotFound.
	Read(ctx context.Context, app, key string) (Secret, error)
	// Write sets key in app's env file to value.
	Write(ctx context.Context, app, key string, value Secret) error
}

// ComposeFile is the parsed model of one compose.yaml. It is filled in by M1.
type ComposeFile struct {
	// Path is the file's path relative to the repo root.
	Path string
}

// ComposeParser parses compose.yaml bytes. *compose.Parser implements it; it is declared here
// because homelab cannot import compose.
type ComposeParser interface {
	Parse(path string, src []byte) (ComposeFile, error)
}
