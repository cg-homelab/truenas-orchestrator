package homelab

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SetupOutcome is what a step did when it ran.
type SetupOutcome string

// Setup outcomes.
const (
	OutcomeCreated     SetupOutcome = "created"
	OutcomeKept        SetupOutcome = "kept"
	OutcomeOverwritten SetupOutcome = "overwritten"
	OutcomeUnchanged   SetupOutcome = "unchanged"
)

// SetupResult is what a setup run did, step by step, in plan order.
type SetupResult struct {
	Steps       []SetupStepResult `json:"steps"`
	Created     int               `json:"created"`
	Kept        int               `json:"kept"`
	Overwritten int               `json:"overwritten"`
	Unchanged   int               `json:"unchanged"`
}

// SetupStepResult is one step and its outcome. Reason is updated when the outcome differs from
// the plan, such as a file that appeared between planning and writing.
type SetupStepResult struct {
	SetupStep
	Outcome SetupOutcome `json:"outcome"`
}

func (r *SetupResult) add(s SetupStep, o SetupOutcome, report func(string)) {
	r.Steps = append(r.Steps, SetupStepResult{SetupStep: s, Outcome: o})
	switch o {
	case OutcomeCreated:
		r.Created++
	case OutcomeKept:
		r.Kept++
	case OutcomeOverwritten:
		r.Overwritten++
	case OutcomeUnchanged:
		r.Unchanged++
	}
	report(reportLine(s, o))
}

// Summary is the result's final report line, e.g. "3 created, 2 kept, 0 overwritten, 1 unchanged".
func (r SetupResult) Summary() string {
	return fmt.Sprintf("%d created, %d kept, %d overwritten, %d unchanged", r.Created, r.Kept, r.Overwritten, r.Unchanged)
}

// ApplySetup plans in from fresh state and runs the plan. report, which may be nil, receives one
// line per step and a final summary; no line ever holds a value.
//
// A plan that is not runnable, or that has a config file awaiting a decision, is an error
// wrapping ErrValidation and nothing is written. So is a plan whose digest differs from
// in.ExpectDigest, with an error wrapping ErrConflict. The env file is written first, in one
// SecretStore.WriteEnv call and only when its bytes change, so that a tracked env file
// (ErrTracked) aborts the run before any host write. Host steps then run in plan order and
// re-check at the point of write: an existing secret file is never overwritten, even one that
// appeared since planning, and running the same input twice changes nothing the second time.
// An error after the first write names the failed step's path and leaves earlier steps done;
// the result holds the steps that completed.
func ApplySetup(ctx context.Context, in SetupInput, deps SetupDeps, report func(line string)) (SetupResult, error) {
	if report == nil {
		report = func(string) {}
	}
	st, err := planSetup(ctx, in, deps)
	if err != nil {
		return SetupResult{}, err
	}
	plan := st.plan
	if !plan.Runnable {
		return SetupResult{}, fmt.Errorf("setup %s cannot run: %s: %w", in.App, strings.Join(plan.Problems, "; "), ErrValidation)
	}
	if in.ExpectDigest != "" && in.ExpectDigest != plan.Digest {
		return SetupResult{}, fmt.Errorf("setup %s: the plan changed since it was shown; review it again: %w", in.App, ErrConflict)
	}
	if len(plan.Decisions) > 0 {
		return SetupResult{}, fmt.Errorf("setup %s needs an overwrite decision for %s: %w", in.App, strings.Join(plan.Decisions, ", "), ErrValidation)
	}

	r := &run{ctx: ctx, app: in.App, deps: deps, st: st, values: map[string]Secret{}}
	var res SetupResult
	if err := r.writeEnv(&res, report); err != nil {
		return res, err
	}
	for i, s := range plan.Steps {
		if s.Kind == StepEnvFile || s.Kind == StepEnvKey {
			continue
		}
		s, o, err := r.host(s, st.data[i])
		if err != nil {
			report(fmt.Sprintf("failed %s %s: %v", kindLabel(s.Kind), s.Path, err))
			report("stopped: " + res.Summary())
			return res, fmt.Errorf("setup %s: %s %s: %w", in.App, kindLabel(s.Kind), s.Path, err)
		}
		res.add(s, o, report)
	}
	report(res.Summary())
	return res, nil
}

// run is one execution of a plan.
type run struct {
	ctx  context.Context
	app  string
	deps SetupDeps
	st   *setupState
	// values holds the env values set by this run, by key; value_from reads them first.
	values map[string]Secret
	// finalEnv is the env file as this run leaves it.
	finalEnv DotEnv
}

// writeEnv realises the prompt values, writes the env file once if its bytes change, and records
// the env steps.
func (r *run) writeEnv(res *SetupResult, report func(string)) error {
	steps, data := r.st.plan.Steps, r.st.data
	hasEnv := false
	for i, s := range steps {
		if s.Kind != StepEnvKey && s.Kind != StepEnvFile {
			continue
		}
		hasEnv = true
		if s.Kind != StepEnvKey || s.Action != ActionSet {
			continue
		}
		v := data[i].answer
		if g := data[i].generate; g != nil {
			var err error
			if v, err = generateSecret(*g); err != nil {
				return fmt.Errorf("generate %s: %w", s.Key, err)
			}
		}
		r.values[s.Key] = v
	}
	final, err := UpdateDotEnv(r.st.envBase, r.values, nil)
	if err != nil {
		return fmt.Errorf("update %s: %w", envRel(r.app), err)
	}
	if r.finalEnv, err = ParseDotEnv(final); err != nil {
		return fmt.Errorf("parse updated %s: %w", envRel(r.app), err)
	}
	write := hasEnv && (!r.st.envExists || !bytes.Equal(final, r.st.envCurrent))
	if write {
		if err := r.deps.Secrets.WriteEnv(r.ctx, r.app, final); err != nil {
			return fmt.Errorf("write %s: %w", envRel(r.app), err)
		}
	}
	for i, s := range steps {
		var o SetupOutcome
		switch {
		case s.Kind != StepEnvKey && s.Kind != StepEnvFile:
			continue
		case s.Action == ActionKeep:
			o = OutcomeKept
		case !write:
			o = OutcomeUnchanged
		case s.Kind == StepEnvKey && data[i].hadValue:
			o = OutcomeOverwritten
		default:
			o = OutcomeCreated
		}
		res.add(s, o, report)
	}
	return nil
}

func envRel(app string) string { return "compose/" + app + "/.env" }

// host runs one directory, secret or config file step.
func (r *run) host(s SetupStep, d stepData) (SetupStep, SetupOutcome, error) {
	h := r.deps.Host
	switch s.Action {
	case ActionKeep:
		return s, OutcomeKept, nil
	case ActionUnchanged:
		return s, OutcomeUnchanged, nil
	case ActionCreate:
	case ActionOverwrite:
		return r.overwrite(s, d)
	case ActionSet, ActionAsk:
		return s, "", fmt.Errorf("unexpected action %s: %w", s.Action, ErrValidation)
	}

	var err error
	switch s.Kind {
	case StepDir:
		err = h.Mkdir(r.ctx, s.Root, s.Rel, s.Mode)
	case StepSecret:
		v, verr := r.secretValue(d)
		if verr != nil {
			return s, "", verr
		}
		err = h.CreateExclusive(r.ctx, s.Root, s.Rel, []byte(v.Reveal()), s.Mode) // the secret's point of use
	case StepConfigFile:
		err = h.CreateExclusive(r.ctx, s.Root, s.Rel, d.content, s.Mode)
	case StepEnvFile, StepEnvKey:
		return s, "", fmt.Errorf("env step outside the env phase: %w", ErrValidation)
	}
	switch {
	case errors.Is(err, ErrConflict):
		// Something appeared since planning. Only the kind of thing the step creates counts as
		// present: a symlink (perhaps dangling) or a directory where a file belongs does not.
		info, serr := h.Stat(r.ctx, s.Root, s.Rel)
		if serr != nil {
			return s, "", serr
		}
		if !info.Exists || info.IsSymlink || info.IsDir != (s.Kind == StepDir) {
			return s, "", fmt.Errorf("something other than the planned %s appeared since planning: %w", kindLabel(s.Kind), err)
		}
		s.Reason = "already present; appeared since planning"
		return s, OutcomeKept, nil
	case err != nil:
		return s, "", err
	}
	return s, OutcomeCreated, nil
}

// overwrite replaces a config file whose content differs, re-reading it first.
func (r *run) overwrite(s SetupStep, d stepData) (SetupStep, SetupOutcome, error) {
	cur, err := r.deps.Host.ReadFile(r.ctx, s.Root, s.Rel)
	o := OutcomeOverwritten
	switch {
	case errors.Is(err, ErrNotFound):
		o = OutcomeCreated
	case err != nil:
		return s, "", err
	case bytes.Equal(cur, d.content):
		return s, OutcomeUnchanged, nil
	}
	if err := r.deps.Host.WriteFile(r.ctx, s.Root, s.Rel, d.content, s.Mode); err != nil {
		return s, "", err
	}
	return s, o, nil
}

// secretValue realises a secret: generated now, or the value of its value_from key as this run
// leaves the env file — the same value a generated prompt wrote there.
func (r *run) secretValue(d stepData) (Secret, error) {
	if d.generate != nil {
		return generateSecret(*d.generate)
	}
	if v, ok := r.values[d.valueFrom]; ok {
		return v, nil
	}
	if !hasValue(r.finalEnv, d.valueFrom) {
		return Secret{}, fmt.Errorf("value_from %s has no value in %s: %w", d.valueFrom, envRel(r.app), ErrValidation)
	}
	e, _ := r.finalEnv.Lookup(d.valueFrom)
	return e.Value, nil
}

const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// generateSecret draws g.Bytes random bytes from crypto/rand and encodes them: base64 is standard
// with padding (like the base64 command, without a newline), hex is lowercase. alphanumeric is
// g.Bytes characters drawn uniformly from [A-Za-z0-9] by rejection sampling.
func generateSecret(g SetupGenerate) (Secret, error) {
	if g.Bytes < 1 || g.Bytes > maxGenerateBytes {
		return Secret{}, fmt.Errorf("generate bytes must be between 1 and %d: %w", maxGenerateBytes, ErrValidation)
	}
	switch g.Encoding {
	case "", encodingBase64, encodingHex:
		b := make([]byte, g.Bytes)
		if _, err := rand.Read(b); err != nil {
			return Secret{}, fmt.Errorf("read random bytes: %w", err)
		}
		if g.Encoding == encodingHex {
			return NewSecret(hex.EncodeToString(b)), nil
		}
		return NewSecret(base64.StdEncoding.EncodeToString(b)), nil
	case encodingAlphanumeric:
		out := make([]byte, 0, g.Bytes)
		buf := make([]byte, g.Bytes)
		for len(out) < g.Bytes {
			if _, err := rand.Read(buf); err != nil {
				return Secret{}, fmt.Errorf("read random bytes: %w", err)
			}
			for _, c := range buf {
				// 248 = 4*62: accepting only bytes below it keeps every character equally likely.
				if c < 248 && len(out) < g.Bytes {
					out = append(out, alphanumeric[c%62])
				}
			}
		}
		return NewSecret(string(out)), nil
	}
	return Secret{}, fmt.Errorf("generate encoding %q is not base64, hex or alphanumeric: %w", g.Encoding, ErrValidation)
}

func kindLabel(k SetupStepKind) string {
	switch k {
	case StepEnvFile:
		return "env file"
	case StepEnvKey:
		return "env"
	case StepDir:
		return "dir"
	case StepSecret:
		return "secret"
	case StepConfigFile:
		return "config"
	}
	return string(k)
}

// reportLine renders one step's outcome for job output, e.g.
// "create dir /mnt/appconfig/authelia/secrets mode 0700" or
// "keep secret /mnt/appconfig/authelia/secrets/JWT_SECRET (already present; never overwritten)".
// It holds paths and key names only.
func reportLine(s SetupStep, o SetupOutcome) string {
	var verb string
	switch o {
	case OutcomeCreated:
		verb = "create"
	case OutcomeKept:
		verb = "keep"
	case OutcomeOverwritten:
		verb = "overwrite"
	case OutcomeUnchanged:
		verb = "unchanged"
	}
	target := s.Path
	if s.Kind == StepEnvKey {
		target = s.Key
		if o == OutcomeCreated || o == OutcomeOverwritten {
			verb = "set"
		}
	}
	line := verb + " " + kindLabel(s.Kind) + " " + target
	if (o == OutcomeCreated || o == OutcomeOverwritten) && s.Kind != StepEnvKey {
		line += " mode " + s.Mode.String()
	}
	if s.Reason != "" && s.Reason != reasonDeclared {
		line += " (" + s.Reason + ")"
	}
	return line
}
