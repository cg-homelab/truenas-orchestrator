package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

// Audit actions.
const (
	auditEnvReveal   = "env.reveal"
	auditEnvWrite    = "env.write"
	auditSecretWrite = "secret.write"
	auditConfigWrite = "config.write"
	auditDirCreate   = "dir.create"
	auditSetupRun    = "setup.run"

	auditTrueNASSettingsWrite  = "truenas.credentials.write"
	auditTrueNASSettingsDelete = "truenas.credentials.delete"
	auditGroupCreate           = "group.create"
	auditUserCreate            = "user.create"
	auditDatasetCreate         = "dataset.create"
	auditACLSet                = "acl.set"
	auditAppCreate             = "app.create"
	auditNetworkCreate         = "network.create"
	auditBootstrapRun          = "bootstrap.run"
	auditDeployKeyCreate       = "deploy_key.create"
)

// auditor appends to the audit log. It takes details as plain strings, never a homelab.Secret:
// every call site passes key names, paths and counts, so no value can reach the log.
type auditor struct {
	store  *sqlite.Store
	logger *slog.Logger
	now    func() time.Time
}

// record appends an entry attributed to the authenticated caller of the request in ctx.
func (a *auditor) record(ctx context.Context, action, target string, details map[string]string) {
	u, _ := userFrom(ctx)
	a.recordAs(ctx, u.Username, action, target, details)
}

// recordAs appends an entry attributed to user, for work that runs outside the request that
// asked for it (a job captures the username when it is enqueued). The write the entry describes
// has already happened, so a failure to record it is logged rather than returned: failing the
// request would suggest the write did not happen.
func (a *auditor) recordAs(ctx context.Context, user, action, target string, details map[string]string) {
	err := a.store.Audit(ctx, sqlite.AuditEntry{At: a.now(), User: user, Action: action, Target: target, Details: details})
	if err != nil {
		a.logger.ErrorContext(ctx, "audit entry not recorded", "action", action, "target", target, "error", err)
	}
}

// AuditEntryView is one audit log entry.
type AuditEntryView struct {
	ID      int64             `json:"id"`
	At      time.Time         `json:"at"`
	User    string            `json:"user" doc:"Who made the change, or started the job that made it"`
	Action  string            `json:"action" doc:"What happened, e.g. env.write, secret.write, setup.run, dataset.create, bootstrap.run"`
	Target  string            `json:"target" doc:"The repo-relative or absolute path written, or the TrueNAS object changed"`
	Details map[string]string `json:"details" doc:"Key names, paths and counts; never a value"`
}

// AuditList is the list-audit response body.
type AuditList struct {
	Entries []AuditEntryView `json:"entries"`
}

// ListAuditInput is the list-audit request.
type ListAuditInput struct {
	Limit int `query:"limit" default:"50" minimum:"1" maximum:"500" doc:"How many entries, newest first"`
}

// ListAuditOutput is the list-audit response.
type ListAuditOutput struct {
	Body AuditList
}

var opListAudit = huma.Operation{
	OperationID: "list-audit",
	Method:      http.MethodGet,
	Path:        "/api/audit",
	Summary:     "List audit log entries, newest first",
	Tags:        []string{"audit"},
	Security:    authenticated(),
}

func handleListAudit(
	logger *slog.Logger, store *sqlite.Store,
) func(context.Context, *ListAuditInput) (*ListAuditOutput, error) {
	return func(ctx context.Context, in *ListAuditInput) (*ListAuditOutput, error) {
		entries, err := store.AuditEntries(ctx, in.Limit)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		out := &ListAuditOutput{Body: AuditList{Entries: make([]AuditEntryView, 0, len(entries))}}
		for _, e := range entries {
			out.Body.Entries = append(out.Body.Entries, AuditEntryView{
				ID: e.ID, At: e.At.UTC(), User: e.User, Action: e.Action, Target: e.Target, Details: e.Details,
			})
		}
		return out, nil
	}
}
