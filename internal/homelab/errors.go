package homelab

import "errors"

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
)
