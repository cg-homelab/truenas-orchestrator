package homelab

import (
	"errors"
	"fmt"
)

// Sentinel errors that callers branch on with errors.Is. Implementation packages translate
// their own errors into these at the boundary, and internal/server maps them to HTTP status.
var (
	// ErrNotFound means the requested object does not exist.
	ErrNotFound = errors.New("not found")
	// ErrValidation means the input was well-formed but violates a rule.
	ErrValidation = errors.New("validation failed")
	// ErrConflict means the operation conflicts with the current state.
	ErrConflict = errors.New("conflict")
	// ErrForbidden means the caller is authenticated but not allowed to do this.
	ErrForbidden = errors.New("forbidden")
	// ErrUnauthenticated means the caller is not authenticated.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrUnreadable is returned by Repo.ReadFile and Repo.Stat for a path that is a symlink
	// escaping the repo, a directory where a file was expected, or otherwise not a regular file.
	ErrUnreadable = errors.New("not a readable regular file")
	// ErrTracked is returned by SecretStore.WriteEnv for an env file that git tracks (GIT001).
	// It wraps ErrConflict, so callers that map conflicts need no extra case.
	ErrTracked = fmt.Errorf("env file is tracked by git: %w", ErrConflict)
	// ErrNotIgnored is returned by SecretStore.WriteEnv for an env file no .gitignore rule
	// covers, which one git add away from being committed (GIT001). It wraps ErrConflict.
	ErrNotIgnored = fmt.Errorf("env file is not gitignored: %w", ErrConflict)
)
