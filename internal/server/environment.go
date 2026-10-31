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

// jobKindBootstrap runs the environment bootstrap plan.
const jobKindBootstrap = "environment.bootstrap"

// auditTargetEnvironment is the target of a bootstrap.run audit entry.
const auditTargetEnvironment = "environment"

// environmentDeps are what the environment operations need.
type environmentDeps struct {
	truenas truenasDeps
	repo    homelab.Repo
	host    homelab.SetupHost
}

// ReadinessItemView is one thing the readiness check compared.
type ReadinessItemView struct {
	Kind    string `json:"kind" enum:"version,docker,global_env,group,user,identity,dataset,state_dataset,permissions,media,networks,networks_app,self_app,deploy_key"`
	Target  string `json:"target" doc:"A path, a dataset, an account or an app; empty for version-less items such as docker"`
	State   string `json:"state" enum:"ok,missing,wrong,differs,unknown" doc:"missing: the plan fixes it; wrong: blocking, fix it by hand as the message says; differs: reported but accepted; unknown: could not be read, blocking"`
	Message string `json:"message"`
}

// BootstrapStepView is one TrueNAS change the bootstrap plan makes.
type BootstrapStepView struct {
	Kind   string            `json:"kind" enum:"group_create,user_create,dataset_create,permissions_set,app_create"`
	Target string            `json:"target" doc:"The group or user name, dataset name, path or app name"`
	Action string            `json:"action" enum:"create,change"`
	Detail map[string]string `json:"detail" doc:"Ids, dataset mountpoint and properties, and for permissions_set from_owner, from_mode, from_acl, to_owner, to_mode and stripacl (from_* empty for a directory the plan creates)"`
}

// EnvironmentView is the environment-readiness check and the bootstrap plan.
type EnvironmentView struct {
	Connection ConnectionView      `json:"connection"`
	Ready      bool                `json:"ready" doc:"Connected, and no item is missing, wrong or unknown"`
	Runnable   bool                `json:"runnable" doc:"Connected with no problems: run-bootstrap may run the steps"`
	Digest     string              `json:"digest" doc:"Identifies the steps; pass it as plan_digest to run-bootstrap. Empty when not connected"`
	Items      []ReadinessItemView `json:"items"`
	Steps      []BootstrapStepView `json:"steps"`
	Problems   []string            `json:"problems" doc:"Everything that forbids a run, by key name and path"`
}

func newEnvironmentView(conn ConnectionView, r homelab.Readiness) EnvironmentView {
	v := EnvironmentView{
		Connection: conn, Ready: r.Ready, Runnable: r.Runnable, Digest: r.Digest,
		Items: []ReadinessItemView{}, Steps: []BootstrapStepView{}, Problems: append([]string{}, r.Problems...),
	}
	for _, it := range r.Items {
		v.Items = append(v.Items, ReadinessItemView{Kind: string(it.Kind), Target: it.Target, State: string(it.State), Message: it.Message})
	}
	for _, s := range r.Steps {
		d := make(map[string]string, len(s.Detail))
		for k, val := range s.Detail {
			d[k] = val
		}
		v.Steps = append(v.Steps, BootstrapStepView{Kind: string(s.Kind), Target: s.Target, Action: string(s.Action), Detail: d})
	}
	return v
}

// environmentPlan is one observation of the environment and the plan made from it.
type environmentPlan struct {
	conn      ConnectionView
	connected bool
	spec      homelab.EnvironmentSpec
	readiness homelab.Readiness
}

// loadConfig is the configured environment plus compose/.global.env from the repo. A missing,
// unreadable or invalid file is a problem, not an error; the error is for a failing repo.
func (d environmentDeps) loadConfig(ctx context.Context) (homelab.EnvironmentConfig, []string, error) {
	cfg := d.truenas.env
	cfg.Global = homelab.DotEnv{}
	b, err := d.repo.ReadFile(ctx, homelab.GlobalEnvPath)
	switch {
	case errors.Is(err, homelab.ErrNotFound):
		return cfg, []string{homelab.GlobalEnvPath + " does not exist: copy example.global.env and set its keys"}, nil
	case errors.Is(err, homelab.ErrUnreadable):
		return cfg, []string{homelab.GlobalEnvPath + " is not a readable regular file"}, nil
	case err != nil:
		return cfg, nil, err
	}
	g, err := homelab.ParseDotEnv(b)
	cfg.Global = g
	if err != nil {
		return cfg, []string{homelab.GlobalEnvPath + ": " + err.Error()}, nil // names the line, never a value
	}
	return cfg, nil, nil
}

// plan reads the configuration, connects, observes and checks. It returns an error only for
// failures that are the server's: no repo, a failing store, a cancelled request.
func (d environmentDeps) plan(ctx context.Context, logger *slog.Logger) (environmentPlan, error) {
	if d.repo == nil {
		return environmentPlan{}, errNoRepo
	}
	cfg, fileProblems, err := d.loadConfig(ctx)
	if err != nil {
		return environmentPlan{}, err
	}
	spec, specProblems := homelab.NewEnvironmentSpec(cfg)
	p := environmentPlan{spec: spec}

	conn, cv, err := d.truenas.connect(ctx, logger)
	if err != nil {
		return environmentPlan{}, err
	}
	p.conn = cv
	if conn == nil {
		problems := append(append([]string{}, fileProblems...), specProblems...)
		p.readiness = homelab.Readiness{Items: []homelab.ReadinessItem{}, Steps: []homelab.BootstrapStep{}, Problems: problems}
		return p, nil
	}
	defer closeConn(ctx, logger, conn)
	obs, err := homelab.ObserveEnvironment(ctx, spec, conn)
	if err != nil {
		return environmentPlan{}, err
	}
	obs.DeployKey = d.deployKeyState(ctx, logger, spec.StateStorage)
	r := homelab.CheckEnvironment(spec, obs)
	if len(fileProblems) > 0 {
		r.Problems = append(fileProblems, r.Problems...)
		r.Ready, r.Runnable = false, false
	}
	p.connected, p.readiness = true, r
	p.conn.DetectedVersion = r.Version.Detected
	p.conn.Message = "connected to TrueNAS " + r.Version.Detected
	return p, nil
}

// deployKeyState observes the deploy key for the readiness check; "" leaves the item out.
func (d environmentDeps) deployKeyState(ctx context.Context, logger *slog.Logger, stateRoot string) homelab.ItemState {
	if d.host == nil || stateRoot == "" {
		return ""
	}
	_, err := homelab.ReadDeployKey(ctx, d.host, stateRoot)
	switch {
	case err == nil:
		return homelab.ItemOK
	case errors.Is(err, homelab.ErrNotFound):
		return homelab.ItemMissing
	}
	logger.WarnContext(ctx, "read deploy key", "error", err)
	return homelab.ItemUnknown
}

// GetEnvironmentOutput is the get-environment response.
type GetEnvironmentOutput struct {
	Body EnvironmentView
}

var opGetEnvironment = huma.Operation{
	OperationID: "get-environment",
	Method:      http.MethodGet,
	Path:        "/api/environment",
	Summary:     "Check the TrueNAS environment and preview the bootstrap plan, without changing anything",
	Description: "Returns 200 also when TrueNAS is not connected: connection.state says why, and items and " +
		"steps are then empty.",
	Tags:     []string{"environment"},
	Security: authenticated(),
}

func handleGetEnvironment(
	logger *slog.Logger, d environmentDeps,
) func(context.Context, *struct{}) (*GetEnvironmentOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetEnvironmentOutput, error) {
		p, err := d.plan(ctx, logger)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &GetEnvironmentOutput{Body: newEnvironmentView(p.conn, p.readiness)}, nil
	}
}

// BootstrapRequest binds a bootstrap run to the plan the user reviewed.
type BootstrapRequest struct {
	PlanDigest string `json:"plan_digest" minLength:"1" maxLength:"64" doc:"The digest of the plan the user reviewed (get-environment)"`
}

// RunBootstrapInput is the run-bootstrap request.
type RunBootstrapInput struct {
	Body BootstrapRequest
}

// RunBootstrapOutput is the run-bootstrap response.
type RunBootstrapOutput struct {
	Body JobStarted
}

var opRunBootstrap = huma.Operation{
	OperationID: "run-bootstrap",
	Method:      http.MethodPost,
	Path:        "/api/environment/bootstrap",
	Summary:     "Start a job that runs the bootstrap plan",
	Description: "Plans first. plan_digest must be the digest of the plan the user reviewed: a different fresh " +
		"plan is a 409, and a plan with problems is a 422 listing them in errors; either way no job starts. " +
		"The job connects again, re-plans, fails if that plan's digest differs, and runs the steps; running " +
		"it twice changes nothing, and a plan with no steps succeeds with \"nothing to do\".",
	Tags:          []string{"environment"},
	Security:      authenticated(),
	DefaultStatus: http.StatusAccepted,
	Errors: []int{
		http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable,
	},
}

func handleRunBootstrap(
	logger *slog.Logger, d environmentDeps, jobs *jobRunner, audit *auditor,
) func(context.Context, *RunBootstrapInput) (*RunBootstrapOutput, error) {
	return func(ctx context.Context, in *RunBootstrapInput) (*RunBootstrapOutput, error) {
		p, err := d.plan(ctx, logger)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		if !p.connected {
			if p.conn.State == connUnreachable {
				return nil, huma.Error502BadGateway(p.conn.Message)
			}
			return nil, huma.Error422UnprocessableEntity(p.conn.Message)
		}
		r := p.readiness
		if r.Digest != in.Body.PlanDigest {
			return nil, huma.Error409Conflict(msgPlanChanged)
		}
		if !r.Runnable {
			errs := make([]error, 0, len(r.Problems))
			for _, pr := range r.Problems {
				errs = append(errs, &huma.ErrorDetail{Location: "body", Message: pr})
			}
			return nil, huma.Error422UnprocessableEntity("the bootstrap cannot run; see errors", errs...)
		}

		u, _ := userFrom(ctx)
		user, digest := u.Username, in.Body.PlanDigest
		j, err := jobs.enqueue(ctx, jobKindBootstrap, func(ctx context.Context, report func(string)) error {
			id := jobIDFrom(ctx)
			spec, res, err := runBootstrapJob(ctx, logger, d, digest, report)
			// The changes happened even if the job is being cancelled: record them regardless.
			actx := context.WithoutCancel(ctx)
			recordBootstrapRun(actx, audit, user, id, spec, res, err)
			counts := []any{"job_id", id, "created", res.Created, "changed", res.Changed, "unchanged", res.Unchanged}
			if err != nil {
				logger.WarnContext(actx, "bootstrap failed", append(counts, "error", err)...)
				return err
			}
			logger.InfoContext(actx, "bootstrap finished", counts...)
			return nil
		})
		switch {
		case errors.Is(err, homelab.ErrConflict):
			return nil, huma.Error409Conflict("a bootstrap job is already running")
		case errors.Is(err, errShuttingDown):
			return nil, huma.Error503ServiceUnavailable("the server is shutting down")
		case err != nil:
			return nil, apiError(ctx, logger, err)
		}
		return &RunBootstrapOutput{Body: JobStarted{JobID: j.ID}}, nil
	}
}

// runBootstrapJob is the work of a bootstrap job. Jobs ahead of it in the queue may have changed
// the repo or TrueNAS since the request was checked, so it reads compose/.global.env again,
// opens its own session and lets ApplyBootstrap re-plan; the digest then refuses a plan that
// changed.
func runBootstrapJob(
	ctx context.Context, logger *slog.Logger, d environmentDeps, digest string, report func(string),
) (homelab.EnvironmentSpec, homelab.BootstrapResult, error) {
	if d.repo == nil {
		return homelab.EnvironmentSpec{}, homelab.BootstrapResult{}, errNoRepo
	}
	cfg, fileProblems, err := d.loadConfig(ctx)
	if err != nil {
		return homelab.EnvironmentSpec{}, homelab.BootstrapResult{}, err
	}
	spec, _ := homelab.NewEnvironmentSpec(cfg)
	if len(fileProblems) > 0 {
		return spec, homelab.BootstrapResult{}, fmt.Errorf("bootstrap cannot run: %s: %w", strings.Join(fileProblems, "; "), homelab.ErrValidation)
	}
	conn, err := d.truenas.dialStored(ctx)
	if err != nil {
		return spec, homelab.BootstrapResult{}, fmt.Errorf("connect to TrueNAS: %w", err)
	}
	defer closeConn(ctx, logger, conn)
	res, err := homelab.ApplyBootstrap(ctx, spec, conn, digest, report)
	return spec, res, err
}

// recordBootstrapRun records what a bootstrap run changed: one entry per step that created or
// changed something (and one network.create per network the networks app created), then one
// bootstrap.run with the counts. Every detail is a name, an id, a path or a count.
func recordBootstrapRun(
	ctx context.Context, audit *auditor, user, jobID string, spec homelab.EnvironmentSpec,
	res homelab.BootstrapResult, runErr error,
) {
	for _, s := range res.Steps {
		if s.Outcome == homelab.OutcomeBootstrapUnchanged {
			continue
		}
		details := map[string]string{"job_id": jobID, "outcome": string(s.Outcome)}
		switch s.Kind {
		case homelab.StepGroupCreate:
			details["gid"] = s.Detail["gid"]
			audit.recordAs(ctx, user, auditGroupCreate, s.Target, details)
		case homelab.StepUserCreate:
			details["uid"], details["gid"] = s.Detail["uid"], s.Detail["gid"]
			audit.recordAs(ctx, user, auditUserCreate, s.Target, details)
		case homelab.StepDatasetCreate:
			for k, v := range s.Detail {
				details[k] = v
			}
			audit.recordAs(ctx, user, auditDatasetCreate, s.Target, details)
		case homelab.StepPermissionsSet:
			details["uid"], details["gid"] = strconv.Itoa(spec.PUID), strconv.Itoa(spec.PGID)
			details["mode"] = s.Detail["to_mode"]
			details["from_owner"], details["from_mode"] = s.Detail["from_owner"], s.Detail["from_mode"]
			details["stripacl"] = s.Detail["stripacl"]
			audit.recordAs(ctx, user, auditACLSet, s.Target, details)
		case homelab.StepAppCreate:
			audit.recordAs(ctx, user, auditAppCreate, s.Target, details)
			for _, n := range strings.Split(s.Detail["networks"], ",") {
				if n != "" {
					audit.recordAs(ctx, user, auditNetworkCreate, n, map[string]string{"job_id": jobID, "app": s.Target})
				}
			}
		}
	}
	state := "succeeded"
	if runErr != nil {
		state = "failed"
	}
	audit.recordAs(ctx, user, auditBootstrapRun, auditTargetEnvironment, map[string]string{
		"job_id": jobID, "state": state, "steps": strconv.Itoa(len(res.Steps)),
		"created": strconv.Itoa(res.Created), "changed": strconv.Itoa(res.Changed), "unchanged": strconv.Itoa(res.Unchanged),
	})
}
