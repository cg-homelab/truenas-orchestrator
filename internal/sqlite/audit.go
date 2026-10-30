package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

// AuditEntry is one row of the audit log. Details holds key names, paths and counts; it never
// holds a secret value, which is why it is a map of plain strings and not of homelab.Secret.
type AuditEntry struct {
	ID int64
	At time.Time
	// User is the username that made the change, or that started the job which made it.
	User string
	// Action is what happened, e.g. env.write, secret.write or setup.run.
	Action string
	// Target is what it happened to: a repo-relative or absolute path.
	Target  string
	Details map[string]string
}

// Audit appends e to the audit log. e.ID is ignored; a nil Details is stored as {}.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	details := e.Details
	if details == nil {
		details = map[string]string{}
	}
	b, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	err = s.q.AppendAudit(ctx, db.AppendAuditParams{
		At: toMillis(e.At), User: e.User, Action: e.Action, Target: e.Target, DetailsJson: string(b),
	})
	if err != nil {
		return fmt.Errorf("append audit entry %s: %w", e.Action, err)
	}
	return nil
}

// AuditEntries returns the newest limit entries, newest first.
func (s *Store) AuditEntries(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.q.ListAudit(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		e := AuditEntry{ID: r.ID, At: fromMillis(r.At), User: r.User, Action: r.Action, Target: r.Target}
		if err := json.Unmarshal([]byte(r.DetailsJson), &e.Details); err != nil {
			return nil, fmt.Errorf("decode details of audit entry %d: %w", r.ID, err)
		}
		if e.Details == nil {
			e.Details = map[string]string{}
		}
		out = append(out, e)
	}
	return out, nil
}
