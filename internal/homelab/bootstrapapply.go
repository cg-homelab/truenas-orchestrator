package homelab

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
)

// BootstrapOutcome is what a bootstrap step did when it ran.
type BootstrapOutcome string

// Bootstrap outcomes.
const (
	OutcomeBootstrapCreated BootstrapOutcome = "created"
	OutcomeBootstrapChanged BootstrapOutcome = "changed"
	// OutcomeBootstrapUnchanged means the end state was already there at the point of write.
	OutcomeBootstrapUnchanged BootstrapOutcome = "unchanged"
)

// BootstrapStepResult is one step and its outcome.
type BootstrapStepResult struct {
	BootstrapStep
	Outcome BootstrapOutcome
}

// BootstrapResult is what a bootstrap run did, step by step, in plan order.
type BootstrapResult struct {
	Steps     []BootstrapStepResult
	Created   int
	Changed   int
	Unchanged int
}

// Summary is the result's final report line, e.g. "3 created, 1 changed, 0 unchanged".
func (r BootstrapResult) Summary() string {
	return fmt.Sprintf("%d created, %d changed, %d unchanged", r.Created, r.Changed, r.Unchanged)
}

func (r *BootstrapResult) add(s BootstrapStep, o BootstrapOutcome, report func(string)) {
	r.Steps = append(r.Steps, BootstrapStepResult{BootstrapStep: s, Outcome: o})
	switch o {
	case OutcomeBootstrapCreated:
		r.Created++
	case OutcomeBootstrapChanged:
		r.Changed++
	case OutcomeBootstrapUnchanged:
		r.Unchanged++
	}
	report(bootstrapLine(s, o))
}

// msgNothingToDo is the report of a run whose plan has no steps.
const msgNothingToDo = "nothing to do"

// ApplyBootstrap observes the environment again, plans, and runs the plan. report, which may be
// nil, receives one line per step and a final summary; no line holds a secret.
//
// A plan whose digest differs from expectDigest is an error wrapping ErrConflict, and one that is
// not runnable is an error wrapping ErrValidation; nothing is changed either way. An empty
// expectDigest skips the digest check. Each step re-reads its target at the point of write and is
// unchanged when the end state is already there, so running twice changes nothing the second time.
// CheckAppMutation is applied before CreateApp. The run stops at the first error, which names the
// failed step; the result holds the steps that completed.
func ApplyBootstrap(
	ctx context.Context, spec EnvironmentSpec, tn TrueNAS, expectDigest string, report func(line string),
) (BootstrapResult, error) {
	if report == nil {
		report = func(string) {}
	}
	obs, err := ObserveEnvironment(ctx, spec, tn)
	if err != nil {
		return BootstrapResult{}, err
	}
	plan := checkEnvironment(spec, obs)
	r := plan.readiness
	if expectDigest != "" && expectDigest != r.Digest {
		return BootstrapResult{}, fmt.Errorf("bootstrap: the plan changed since it was shown; review it again: %w", ErrConflict)
	}
	if !r.Runnable {
		return BootstrapResult{}, fmt.Errorf("bootstrap cannot run: %s: %w", strings.Join(r.Problems, "; "), ErrValidation)
	}
	res := BootstrapResult{Steps: []BootstrapStepResult{}}
	if len(r.Steps) == 0 {
		report(msgNothingToDo)
		return res, nil
	}
	for i, s := range r.Steps {
		o, err := runBootstrapStep(ctx, spec, tn, s, plan.payloads[i])
		if err != nil {
			report(fmt.Sprintf("failed %s: %v", stepLabel(s), err))
			report("stopped: " + res.Summary())
			return res, fmt.Errorf("bootstrap: %s: %w", stepLabel(s), err)
		}
		res.add(s, o, report)
	}
	report(res.Summary())
	return res, nil
}

// runBootstrapStep re-checks one step's target and changes it when it still needs changing.
func runBootstrapStep(
	ctx context.Context, spec EnvironmentSpec, tn TrueNAS, s BootstrapStep, p stepPayload,
) (BootstrapOutcome, error) {
	created := OutcomeBootstrapCreated
	if s.Action == BootstrapChange {
		created = OutcomeBootstrapChanged
	}
	switch s.Kind {
	case StepGroupCreate:
		groups, err := tn.Groups(ctx)
		if err != nil {
			return "", err
		}
		if slices.ContainsFunc(groups, func(g Group) bool { return g.GID == p.group.GID }) {
			return OutcomeBootstrapUnchanged, nil
		}
		return created, tn.CreateGroup(ctx, p.group)
	case StepUserCreate:
		users, err := tn.Users(ctx)
		if err != nil {
			return "", err
		}
		if slices.ContainsFunc(users, func(u User) bool { return u.UID == p.user.UID }) {
			return OutcomeBootstrapUnchanged, nil
		}
		return created, tn.CreateUser(ctx, p.user)
	case StepDatasetCreate:
		datasets, err := tn.Datasets(ctx)
		if err != nil {
			return "", err
		}
		if i := slices.IndexFunc(datasets, func(d Dataset) bool { return d.Name == p.dataset.Name }); i >= 0 {
			if mp := datasets[i].Mountpoint; mp == "" || path.Clean(mp) != s.Detail["mountpoint"] {
				return "", fmt.Errorf("dataset %s appeared since planning but is not mounted at %s: %w",
					p.dataset.Name, s.Detail["mountpoint"], ErrConflict)
			}
			return OutcomeBootstrapUnchanged, nil
		}
		return created, tn.CreateDataset(ctx, p.dataset)
	case StepPermissionsSet:
		st, err := tn.Stat(ctx, p.perm.Path)
		switch {
		case err != nil:
			return "", err
		case !st.Exists || st.Type != PathDirectory:
			return "", fmt.Errorf("%s is not a directory: %w", p.perm.Path, ErrConflict)
		case st.UID == p.perm.UID && st.GID == p.perm.GID && st.Mode == p.perm.Mode && !st.ExtendedACL:
			return OutcomeBootstrapUnchanged, nil
		}
		return created, tn.SetPermissions(ctx, p.perm)
	case StepAppCreate:
		if err := CheckAppMutation(spec.SelfApp, p.app.Name); err != nil {
			return "", err
		}
		apps, err := tn.Apps(ctx)
		if err != nil {
			return "", err
		}
		if slices.ContainsFunc(apps, func(a App) bool { return a.Name == p.app.Name }) {
			return OutcomeBootstrapUnchanged, nil
		}
		return created, tn.CreateApp(ctx, p.app)
	}
	return "", fmt.Errorf("unknown bootstrap step kind %q: %w", s.Kind, ErrValidation)
}

// stepLabel names a step for report lines and errors, e.g. "dataset tank/apps".
func stepLabel(s BootstrapStep) string {
	switch s.Kind {
	case StepGroupCreate:
		return "group " + s.Target
	case StepUserCreate:
		return "user " + s.Target
	case StepDatasetCreate:
		return "dataset " + s.Target
	case StepPermissionsSet:
		return "permissions " + s.Target
	case StepAppCreate:
		return "app " + s.Target
	}
	return string(s.Kind) + " " + s.Target
}

// bootstrapLine renders one step's outcome for job output, e.g. "create group homelab gid 1000",
// "change permissions /mnt/tank/apps 0:0 0755 -> 1000:1000 0750" or
// "unchanged dataset tank/apps (already present)". It holds names, ids and paths only.
func bootstrapLine(s BootstrapStep, o BootstrapOutcome) string {
	if o == OutcomeBootstrapUnchanged {
		return "unchanged " + stepLabel(s) + " (already present)"
	}
	verb := "create"
	if o == OutcomeBootstrapChanged {
		verb = "change"
	}
	line := verb + " " + stepLabel(s)
	d := s.Detail
	switch s.Kind {
	case StepGroupCreate:
		line += " gid " + d["gid"]
	case StepUserCreate:
		line += " uid " + d["uid"] + " gid " + d["gid"]
	case StepDatasetCreate:
		line += " at " + d["mountpoint"]
		if d["recordsize"] != "" {
			line += " recordsize " + d["recordsize"]
		}
	case StepPermissionsSet:
		if d["from_owner"] != "" {
			line += " " + d["from_owner"] + " " + d["from_mode"] + " ->"
		}
		line += " " + d["to_owner"] + " " + d["to_mode"]
	case StepAppCreate:
		line += " (networks " + d["networks"] + ")"
	}
	return line
}
