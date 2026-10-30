package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// jobKindSetupRun runs one stack's setup.toml.
const jobKindSetupRun = "setup.run"

// msgPlanChanged is the 409 for a run whose plan differs from the one the user reviewed.
const msgPlanChanged = "the plan changed since it was shown; review it again"

// msgSetupInvalid is the 422 for a setup.toml that does not parse or breaks its schema.
const msgSetupInvalid = "setup.toml has errors; see the stack's findings"

// PromptAnswer is the user's input for one [[prompts]] key: a value, or a request to generate one.
type PromptAnswer struct {
	Value    *secretField `json:"value,omitempty" doc:"The value to write; never echoed back"`
	Generate bool         `json:"generate,omitempty" doc:"Generate the value as the prompt declares"`
}

// SetupRequest is the user's input to a setup plan or run.
type SetupRequest struct {
	Answers    map[string]PromptAnswer `json:"answers,omitempty" doc:"Answers for [[prompts]], by key"`
	Overwrite  map[string]bool         `json:"overwrite,omitempty" doc:"Decisions for config files awaiting one, by step path: true overwrites, false keeps"`
	PlanDigest string                  `json:"plan_digest,omitempty" doc:"The digest of the plan the user reviewed; run-stack-setup requires it"`
}

// RequireView is one [app] requires entry.
type RequireView struct {
	Stack  string `json:"stack"`
	Status string `json:"status" doc:"Ready, NeedsSetup or Broken; empty when the stack does not exist"`
	OK     bool   `json:"ok"`
}

// PromptView is one [[prompts]] entry as the setup wizard shows it.
type PromptView struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	CanGenerate bool   `json:"can_generate"`
	State       string `json:"state" enum:"answered,set,missing"`
}

// SetupStepView is one planned write. It never holds a value.
type SetupStepView struct {
	Kind     string `json:"kind" enum:"env_file,env_key,dir,secret,config_file"`
	RootKind string `json:"root_kind" enum:"appconfig,database,app"`
	Path     string `json:"path" doc:"Absolute path written"`
	Key      string `json:"key" doc:"Env key of an env_key step; empty otherwise"`
	Mode     string `json:"mode" doc:"Mode of the created file or directory, e.g. 0750; empty for env keys"`
	Action   string `json:"action" enum:"create,keep,set,overwrite,unchanged,ask"`
	Reason   string `json:"reason"`
	Source   string `json:"source" doc:"Repo-relative template of a config file or a created env file; empty otherwise"`
}

// SetupPlanView is the exact set of writes a setup run would make.
type SetupPlanView struct {
	Declared  bool            `json:"declared" doc:"Whether setup.toml declares anything to run; when false everything else is empty"`
	Digest    string          `json:"digest" doc:"Identifies this plan's steps; pass it as plan_digest to run-stack-setup"`
	Runnable  bool            `json:"runnable" doc:"No problems; a run also needs every decision made"`
	Problems  []string        `json:"problems" doc:"Everything that forbids running, by key name or path"`
	Decisions []string        `json:"decisions" doc:"Paths of config files that need an overwrite decision"`
	Deferred  []string        `json:"deferred" doc:"Declared work a later milestone runs (e.g. provision.postgres); never run by setup"`
	Requires  []RequireView   `json:"requires"`
	Prompts   []PromptView    `json:"prompts"`
	Steps     []SetupStepView `json:"steps"`
}

func emptySetupPlanView() SetupPlanView {
	return SetupPlanView{
		Problems: []string{}, Decisions: []string{}, Deferred: []string{}, Requires: []RequireView{},
		Prompts: []PromptView{}, Steps: []SetupStepView{},
	}
}

func newSetupPlanView(p homelab.SetupPlan) SetupPlanView {
	v := emptySetupPlanView()
	v.Declared, v.Runnable, v.Digest = true, p.Runnable, p.Digest
	v.Problems = append(v.Problems, p.Problems...)
	v.Decisions = append(v.Decisions, p.Decisions...)
	v.Deferred = append(v.Deferred, p.Deferred...)
	for _, r := range p.Requires {
		v.Requires = append(v.Requires, RequireView{Stack: r.Stack, Status: string(r.Status), OK: r.OK})
	}
	for _, pr := range p.Prompts {
		v.Prompts = append(v.Prompts, PromptView{
			Key: pr.Key, Label: pr.Label, Secret: pr.Secret, CanGenerate: pr.CanGenerate, State: string(pr.State),
		})
	}
	for _, s := range p.Steps {
		mode := ""
		if s.Mode != 0 {
			mode = s.Mode.String()
		}
		v.Steps = append(v.Steps, SetupStepView{
			Kind: string(s.Kind), RootKind: string(s.RootKind), Path: s.Path, Key: s.Key, Mode: mode,
			Action: string(s.Action), Reason: s.Reason, Source: s.Source,
		})
	}
	return v
}

// setupDeps are the adapters the setup endpoints need. Any of them nil means the server was
// built without a repo.
type setupDeps struct {
	repo    homelab.Repo
	secrets homelab.SecretStore
	host    homelab.SetupHost
}

func (d setupDeps) domain() homelab.SetupDeps {
	return homelab.SetupDeps{Repo: d.repo, Host: d.host, Secrets: d.secrets}
}

// prepareSetup builds the SetupInput for stack name from the cached report, its setup.toml and
// the request. declared is false when the stack has no setup.toml or one that declares nothing
// to run; in is then zero.
func prepareSetup(
	ctx context.Context, logger *slog.Logger, reports *reportCache, deps setupDeps, name string, req SetupRequest,
) (in homelab.SetupInput, declared bool, err error) {
	if deps.repo == nil || deps.secrets == nil || deps.host == nil {
		return homelab.SetupInput{}, false, apiError(ctx, logger, errNoRepo)
	}
	rep, st, err := findStack(ctx, logger, reports, name)
	if err != nil {
		return homelab.SetupInput{}, false, err
	}
	setup, err := loadSetup(ctx, deps.repo, name)
	switch {
	case err != nil:
		return homelab.SetupInput{}, false, apiError(ctx, logger, err)
	case !setup.present:
		return homelab.SetupInput{}, false, nil
	case !setup.valid:
		return homelab.SetupInput{}, false, huma.Error422UnprocessableEntity(msgSetupInvalid)
	case !homelab.SetupDeclared(setup.file):
		return homelab.SetupInput{}, false, nil
	}
	statuses := make(map[string]homelab.Status, len(rep.Stacks))
	for _, s := range rep.Stacks {
		statuses[s.Name] = s.Status
	}
	answers := make(map[string]homelab.PromptAnswer, len(req.Answers))
	for k, a := range req.Answers {
		pa := homelab.PromptAnswer{Generate: a.Generate}
		if a.Value != nil {
			pa.Value, pa.HasValue = a.Value.Secret, true
		}
		answers[k] = pa
	}
	return homelab.SetupInput{
		App: name, Setup: setup.file, Deploy: st.Interpolation,
		Answers: answers, Overwrite: req.Overwrite, Statuses: statuses, ExpectDigest: req.PlanDigest,
	}, true, nil
}

// setupError maps a PlanSetup error. Domain validation messages name keys and paths only, so
// they are passed through.
func setupError(ctx context.Context, logger *slog.Logger, err error) error {
	if errors.Is(err, homelab.ErrValidation) || errors.Is(err, homelab.ErrUnreadable) {
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return apiError(ctx, logger, err)
}

// PlanStackSetupInput is the plan-stack-setup request.
type PlanStackSetupInput struct {
	Name string `path:"name" maxLength:"255" doc:"App directory name under compose/"`
	Body SetupRequest
}

// PlanStackSetupOutput is the plan-stack-setup response.
type PlanStackSetupOutput struct {
	Body SetupPlanView
}

var opPlanStackSetup = huma.Operation{
	OperationID: "plan-stack-setup",
	Method:      http.MethodPost,
	Path:        "/api/stacks/{name}/setup/plan",
	Summary:     "Preview the writes a setup run would make, without writing anything",
	Description: "Returns 200 also when the plan cannot run: problems and decisions say why.",
	Tags:        []string{"stacks"},
	Security:    authenticated(),
	Errors:      []int{http.StatusNotFound, http.StatusUnprocessableEntity},
}

func handlePlanStackSetup(
	logger *slog.Logger, reports *reportCache, deps setupDeps,
) func(context.Context, *PlanStackSetupInput) (*PlanStackSetupOutput, error) {
	return func(ctx context.Context, in *PlanStackSetupInput) (*PlanStackSetupOutput, error) {
		sin, declared, err := prepareSetup(ctx, logger, reports, deps, in.Name, in.Body)
		if err != nil {
			return nil, err
		}
		if !declared {
			return &PlanStackSetupOutput{Body: emptySetupPlanView()}, nil
		}
		plan, err := homelab.PlanSetup(ctx, sin, deps.domain())
		if err != nil {
			return nil, setupError(ctx, logger, err)
		}
		return &PlanStackSetupOutput{Body: newSetupPlanView(plan)}, nil
	}
}

// RunStackSetupInput is the run-stack-setup request.
type RunStackSetupInput struct {
	Name string `path:"name" maxLength:"255" doc:"App directory name under compose/"`
	Body SetupRequest
}

// RunStackSetupOutput is the run-stack-setup response.
type RunStackSetupOutput struct {
	Body JobStarted
}

var opRunStackSetup = huma.Operation{
	OperationID: "run-stack-setup",
	Method:      http.MethodPost,
	Path:        "/api/stacks/{name}/setup/run",
	Summary:     "Start a job that runs a stack's setup.toml",
	Description: "Plans first. plan_digest must be the digest of the plan the user reviewed: a different fresh " +
		"plan is a 409, and a plan with problems or undecided config files is a 422 listing them in errors; " +
		"either way no job starts. The job re-plans from fresh state, fails if that plan's digest differs, and " +
		"writes; running it twice changes nothing.",
	Tags:          []string{"stacks"},
	Security:      authenticated(),
	DefaultStatus: http.StatusAccepted,
	Errors: []int{
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable,
	},
}

func handleRunStackSetup(
	logger *slog.Logger, reports *reportCache, deps setupDeps, jobs *jobRunner, audit *auditor, locks *envLocks,
) func(context.Context, *RunStackSetupInput) (*RunStackSetupOutput, error) {
	return func(ctx context.Context, in *RunStackSetupInput) (*RunStackSetupOutput, error) {
		sin, declared, err := prepareSetup(ctx, logger, reports, deps, in.Name, in.Body)
		if err != nil {
			return nil, err
		}
		if !declared {
			return nil, huma.Error422UnprocessableEntity(in.Name + " declares no setup in setup.toml")
		}
		if in.Body.PlanDigest == "" {
			return nil, huma.Error422UnprocessableEntity("plan_digest is required: plan first and review the plan",
				&huma.ErrorDetail{Location: "body.plan_digest", Message: "is required"})
		}
		plan, err := homelab.PlanSetup(ctx, sin, deps.domain())
		if err != nil {
			return nil, setupError(ctx, logger, err)
		}
		if plan.Digest != in.Body.PlanDigest {
			return nil, huma.Error409Conflict(msgPlanChanged)
		}
		if !plan.Runnable || len(plan.Decisions) > 0 {
			errs := make([]error, 0, len(plan.Problems)+len(plan.Decisions))
			for _, p := range plan.Problems {
				errs = append(errs, &huma.ErrorDetail{Location: "body", Message: p})
			}
			for _, d := range plan.Decisions {
				errs = append(errs, &huma.ErrorDetail{
					Location: "body.overwrite", Message: d + " differs from its source and needs an overwrite decision",
				})
			}
			return nil, huma.Error422UnprocessableEntity("setup cannot run; see errors", errs...)
		}

		u, _ := userFrom(ctx)
		user, name, req := u.Username, in.Name, in.Body
		j, err := jobs.enqueue(ctx, jobKindSetupRun, func(ctx context.Context, report func(string)) error {
			id := jobIDFrom(ctx)
			res, err := runSetupJob(ctx, logger, reports, deps, locks, name, req, report)
			// The writes happened even if the job is being cancelled: record them regardless.
			actx := context.WithoutCancel(ctx)
			recordSetupRun(actx, audit, user, id, name, res, err)
			counts := []any{
				"stack", name, "job_id", id,
				"created", res.Created, "kept", res.Kept, "overwritten", res.Overwritten, "unchanged", res.Unchanged,
			}
			if err != nil {
				logger.WarnContext(actx, "setup failed", append(counts, "error", err)...)
				return err
			}
			logger.InfoContext(actx, "setup finished", counts...)
			return nil
		})
		switch {
		case errors.Is(err, homelab.ErrConflict):
			return nil, huma.Error409Conflict("a setup job is already running")
		case errors.Is(err, errShuttingDown):
			return nil, huma.Error503ServiceUnavailable("the server is shutting down")
		case err != nil:
			return nil, apiError(ctx, logger, err)
		}
		return &RunStackSetupOutput{Body: JobStarted{JobID: j.ID}}, nil
	}
}

// runSetupJob is the work of a setup job. Jobs ahead of it in the queue, such as a fetch, may
// have changed the repo since the request was checked, so it rebuilds the input from a fresh
// report and setup.toml with the request's answers, decisions and digest; the digest then
// refuses a plan that changed. It holds the app's env file lock for the whole run.
func runSetupJob(
	ctx context.Context, logger *slog.Logger, reports *reportCache, deps setupDeps, locks *envLocks,
	name string, req SetupRequest, report func(string),
) (homelab.SetupResult, error) {
	unlock, err := locks.lock(ctx, name)
	if err != nil {
		return homelab.SetupResult{}, err
	}
	defer unlock()
	in, declared, err := prepareSetup(ctx, logger, reports, deps, name, req)
	switch {
	case err != nil:
		return homelab.SetupResult{}, fmt.Errorf("setup %s: %w", name, err)
	case !declared:
		return homelab.SetupResult{}, fmt.Errorf("setup %s: setup.toml no longer declares a setup", name)
	}
	return homelab.ApplySetup(ctx, in, deps.domain(), report)
}

// recordSetupRun records what a setup run wrote: one env.write for the env file, one entry per
// directory, secret file and config file written, then one setup.run with the counts. Every
// detail is a key name, a path or a count.
func recordSetupRun(
	ctx context.Context, audit *auditor, user, jobID, app string, res homelab.SetupResult, runErr error,
) {
	written := func(o homelab.SetupOutcome) bool {
		return o == homelab.OutcomeCreated || o == homelab.OutcomeOverwritten
	}
	var keysSet []string
	envCreated, envWritten := false, false
	for _, s := range res.Steps {
		switch s.Kind {
		case homelab.StepEnvFile:
			envCreated = s.Outcome == homelab.OutcomeCreated
			envWritten = envWritten || envCreated
		case homelab.StepEnvKey:
			if written(s.Outcome) {
				keysSet = append(keysSet, s.Key)
				envWritten = true
			}
		case homelab.StepDir, homelab.StepSecret, homelab.StepConfigFile:
		}
	}
	if envWritten {
		audit.recordAs(ctx, user, auditEnvWrite, envPath(app), map[string]string{
			"job_id": jobID, "keys_set": strings.Join(keysSet, ","), "keys_unset": "",
			"created": strconv.FormatBool(envCreated),
		})
	}
	for _, s := range res.Steps {
		if !written(s.Outcome) {
			continue
		}
		details := map[string]string{"job_id": jobID, "stack": app, "outcome": string(s.Outcome), "mode": s.Mode.String()}
		switch s.Kind {
		case homelab.StepDir:
			audit.recordAs(ctx, user, auditDirCreate, s.Path, details)
		case homelab.StepSecret:
			audit.recordAs(ctx, user, auditSecretWrite, s.Path, details)
		case homelab.StepConfigFile:
			audit.recordAs(ctx, user, auditConfigWrite, s.Path, details)
		case homelab.StepEnvFile, homelab.StepEnvKey:
		}
	}
	state := "succeeded"
	if runErr != nil {
		state = "failed"
	}
	audit.recordAs(ctx, user, auditSetupRun, app, map[string]string{
		"job_id": jobID, "state": state,
		"created": strconv.Itoa(res.Created), "kept": strconv.Itoa(res.Kept),
		"overwritten": strconv.Itoa(res.Overwritten), "unchanged": strconv.Itoa(res.Unchanged),
	})
}
