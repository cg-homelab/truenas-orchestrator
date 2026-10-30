package homelab

import (
	"context"
	"fmt"
)

// ValidateRepo runs every validator check over repo, parsing compose files with parse. Both
// `orchestrator validate` and internal/server call it, so the CLI and the UI cannot disagree.
//
// M0 stub: it returns an empty Report. The checks arrive in M1.
func ValidateRepo(ctx context.Context, repo Repo, parse ComposeParser) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("validate repo: %w", err)
	}
	_, _ = repo, parse // used by the M1 checks
	return Report{}, nil
}
