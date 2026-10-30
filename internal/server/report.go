package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/compose"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// reportTTL is how long a validation report is reused, so that one page load, which calls
// several endpoints, validates the repo once.
const reportTTL = 2 * time.Second

// validateTimeout bounds one validation of the repo.
const validateTimeout = 60 * time.Second

// errNoRepo is returned when the server was built without a homelab repo.
var errNoRepo = errors.New("no homelab repo configured")

// reportCache runs homelab.ValidateRepo and keeps the result for reportTTL. The mutex is held
// while validating, so concurrent requests wait for one validation instead of starting their
// own.
type reportCache struct {
	repo homelab.Repo
	opts homelab.Options
	now  func() time.Time

	mu    sync.Mutex
	rep   homelab.Report
	at    time.Time
	valid bool
}

func newReportCache(repo homelab.Repo, opts homelab.Options, now func() time.Time) *reportCache {
	return &reportCache{repo: repo, opts: opts, now: now}
}

// get returns a report no older than reportTTL, validating the repo when needed.
func (c *reportCache) get(ctx context.Context) (homelab.Report, error) {
	if c.repo == nil {
		return homelab.Report{}, errNoRepo
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.valid && now.Sub(c.at) < reportTTL {
		return c.rep, nil
	}
	// Other requests wait on this validation, so one client disconnecting must not fail it for
	// all of them: detach from the caller's cancellation and bound it instead.
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), validateTimeout)
	defer cancel()
	rep, err := homelab.ValidateRepo(vctx, c.repo, &compose.Parser{}, c.opts)
	if err != nil {
		return homelab.Report{}, fmt.Errorf("validate: %w", err)
	}
	c.rep, c.at, c.valid = rep, now, true
	return rep, nil
}

// invalidate drops the cached report, for example after a fetch changed the repo.
func (c *reportCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
	c.rep = homelab.Report{}
}
