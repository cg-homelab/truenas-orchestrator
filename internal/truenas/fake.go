package truenas

import (
	"context"
	"fmt"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// DefaultFakeVersion is the version a zero-value Fake reports.
const DefaultFakeVersion = "TrueNAS-SCALE-25.04.0"

// Fake is an in-process implementation of homelab.TrueNAS backed by canned values. The zero
// value is ready to use. Recorded fixtures from fixtures/truenas/ replace the canned values as
// methods are added.
type Fake struct {
	// Version is returned by SystemVersion; empty means DefaultFakeVersion.
	Version string
}

var _ homelab.TrueNAS = (*Fake)(nil)

// SystemVersion returns f.Version, or DefaultFakeVersion when it is empty.
func (f *Fake) SystemVersion(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("system version: %w", err)
	}
	if f.Version == "" {
		return DefaultFakeVersion, nil
	}
	return f.Version, nil
}
