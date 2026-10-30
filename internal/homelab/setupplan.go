package homelab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// SetupInput is everything a setup plan or run needs besides its adapters. A server handler
// fills it from the stack's StackReport, the parsed SetupFile and the user's input.
type SetupInput struct {
	// App is the app directory name under compose/.
	App   string
	Setup SetupFile
	// Deploy is the deploy env that fills ${VAR} in setup.toml: StackReport.Interpolation.
	Deploy EnvScope
	// Answers holds the user's input for [[prompts]], by key.
	Answers map[string]PromptAnswer
	// Overwrite holds the decisions for config files with on_exists = "prompt", by resolved
	// path (SetupStep.Path): true overwrites, false keeps.
	Overwrite map[string]bool
	// Statuses holds every stack's status by name, for [app] requires.
	Statuses map[string]Status
	// ExpectDigest, when non-empty, is the SetupPlan.Digest the user reviewed. ApplySetup refuses
	// with an error wrapping ErrConflict when the fresh plan's digest differs. Empty skips the
	// check.
	ExpectDigest string
}

// PromptAnswer is the user's input for one prompt: a value, or a request to generate one.
type PromptAnswer struct {
	Value    Secret
	HasValue bool
	Generate bool
}

// SetupDeps are the adapters a setup plan reads through and a setup run writes through.
type SetupDeps struct {
	// Repo reads compose/<app>/example.env and config file templates.
	Repo Repo
	// Host stats, reads and writes under the storage roots.
	Host SetupHost
	// Secrets reads and writes compose/<app>/.env.
	Secrets SecretStore
}

// SetupPlan is the exact set of writes a setup run would make. It never holds a value.
type SetupPlan struct {
	App string `json:"app"`
	// Runnable reports that Problems is empty. A run also needs every Decisions entry made.
	Runnable bool `json:"runnable"`
	// Problems lists everything that forbids running, by key name or path, never by value.
	Problems []string `json:"problems"`
	// Decisions lists the paths of config files whose step is ActionAsk: they exist, differ
	// from their source and declare on_exists = "prompt". Answer them in SetupInput.Overwrite.
	Decisions []string       `json:"decisions"`
	Requires  []RequireState `json:"requires"`
	Prompts   []PromptState  `json:"prompts"`
	// Steps is in execution order: the env file, env keys, directories, secrets, config files.
	// It is empty when setup.toml breaks its schema or uses a variable the deploy env lacks.
	Steps []SetupStep `json:"steps"`
	// Digest is the hex SHA-256 of the steps (kind, path, key, action, mode, source) and the
	// paths that need or took an overwrite decision. A decision does not change it, so the plan a
	// user reviewed and the same plan with its decisions made share a digest. Pass it back as
	// SetupInput.ExpectDigest to bind a run to what was shown.
	Digest string `json:"digest"`
	// Deferred lists declared work a later milestone runs, such as [[provision.postgres]]. It
	// never blocks a run and is never run by setup.
	Deferred []string `json:"deferred"`
}

// RequireState is one [app] requires entry. Status is empty for a stack that does not exist.
type RequireState struct {
	Stack  string `json:"stack"`
	Status Status `json:"status"`
	OK     bool   `json:"ok"`
}

// PromptStatus says where a prompt's value comes from.
type PromptStatus string

// Prompt statuses.
const (
	// PromptAnswered means the run was given a value or asked to generate one.
	PromptAnswered PromptStatus = "answered"
	// PromptSet means the key already has a value in the env file, which is kept.
	PromptSet PromptStatus = "set"
	// PromptMissing means neither; the run generates a value when the prompt offers one.
	PromptMissing PromptStatus = "missing"
)

// PromptState is one [[prompts]] entry as the setup wizard shows it.
type PromptState struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Secret      bool         `json:"secret"`
	CanGenerate bool         `json:"can_generate"`
	State       PromptStatus `json:"state"`
}

// SetupStepKind is what a setup step writes.
type SetupStepKind string

// Setup step kinds.
const (
	StepEnvFile    SetupStepKind = "env_file"
	StepEnvKey     SetupStepKind = "env_key"
	StepDir        SetupStepKind = "dir"
	StepSecret     SetupStepKind = "secret"
	StepConfigFile SetupStepKind = "config_file"
)

// SetupAction is what a step will do.
type SetupAction string

// Setup actions.
const (
	ActionCreate    SetupAction = "create"
	ActionKeep      SetupAction = "keep"
	ActionSet       SetupAction = "set"
	ActionOverwrite SetupAction = "overwrite"
	ActionUnchanged SetupAction = "unchanged"
	// ActionAsk is a config file that needs a decision (SetupInput.Overwrite) before a run.
	ActionAsk SetupAction = "ask"
)

// RootKind names the root a step writes under.
type RootKind string

// Root kinds.
const (
	RootAppConfig RootKind = "appconfig" // $APPCONFIG_STORAGE
	RootDatabase  RootKind = "database"  // $DATABASE_STORAGE
	RootApp       RootKind = "app"       // the app directory in the repo, through SecretStore only
)

// SetupStep is one planned write, or a write that is not needed. Paths are not secrets; the
// values written never appear here.
type SetupStep struct {
	Kind     SetupStepKind `json:"kind"`
	RootKind RootKind      `json:"root_kind"`
	// Root is the absolute root directory and Rel the slash path under it; Path joins them.
	Root string `json:"root"`
	Rel  string `json:"rel"`
	Path string `json:"path"`
	// Key is the env key of a StepEnvKey step.
	Key string `json:"key,omitempty"`
	// Mode is the mode a created or replaced file or directory gets; 0 for env keys.
	Mode   FileMode    `json:"mode"`
	Action SetupAction `json:"action"`
	Reason string      `json:"reason"`
	// Source is the repo-relative template of a config file or of a created env file.
	Source string `json:"source,omitempty"`
}

// ValidAppName reports whether name is a valid app directory name: [a-z0-9][a-z0-9-]*.
func ValidAppName(name string) bool { return identifierRe.MatchString(name) }

// SetupDeclared reports whether f declares anything a setup run does: dirs, secrets,
// config files or prompts. A setup.toml with only [env_validators] or requires declares none.
func SetupDeclared(f SetupFile) bool {
	return len(f.Dirs) > 0 || len(f.Secrets) > 0 || len(f.ConfigFiles) > 0 || len(f.Prompts) > 0
}

// PlanSetup computes the setup plan for in without writing anything. It reads the env file
// through deps.Secrets, example.env and config templates through deps.Repo, and stats and reads
// target paths through deps.Host. Conditions that forbid running are Problems in the plan; the
// error is for invalid input (an invalid app name, wrapping ErrValidation) and adapter failures.
func PlanSetup(ctx context.Context, in SetupInput, deps SetupDeps) (SetupPlan, error) {
	st, err := planSetup(ctx, in, deps)
	if err != nil {
		return SetupPlan{}, err
	}
	return st.plan, nil
}

// setupState is a plan plus what a run needs to realise it, which the plan must not expose.
type setupState struct {
	plan SetupPlan
	// data is parallel to plan.Steps.
	data []stepData
	// envBase is what the env steps start from: the env file, else example.env, else empty.
	envBase    []byte
	envCurrent []byte
	envExists  bool
}

// stepData is the private half of a step.
type stepData struct {
	answer    Secret         // env key set from an answer
	generate  *SetupGenerate // env key or secret to generate
	hadValue  bool           // env key had a real value in the env file before this run
	decided   bool           // config file whose action came from an overwrite decision
	valueFrom string         // secret taken from an env key
	content   []byte         // config file bytes to write
}

// storageRoot is a permitted root for host writes.
type storageRoot struct {
	key  string // APPCONFIG_STORAGE or DATABASE_STORAGE
	kind RootKind
	path string // absolute and clean, never "/"
}

// target is one resolved host path of setup.toml.
type target struct {
	root storageRoot
	rel  string
	path string
	ok   bool
}

func (t target) key() string { return t.root.path + "\x00" + t.rel }

type planner struct {
	ctx  context.Context
	in   SetupInput
	deps SetupDeps
	st   *setupState

	problemSeen map[string]bool
	roots       []storageRoot
	badRoot     map[string]bool
	protected   []string // HOMELAB_STORAGE and the repo root: never written by host steps
	envRel      string
	base        DotEnv
	envOK       bool
	fromTmpl    bool // the env file is absent and example.env is the base

	stats    map[string]HostInfo
	dirs     []SetupStep
	dirData  []stepData
	planned  map[string]bool // dirs planned (create or keep) or existing, by target key
	declared map[string]int  // declared dirs by target key
	files    map[string]bool // secret and config targets, by target key
	envSet   map[string]bool // env keys this run sets

	dirT, secretT, configT []target
}

func planSetup(ctx context.Context, in SetupInput, deps SetupDeps) (*setupState, error) {
	if !ValidAppName(in.App) {
		return nil, fmt.Errorf("app name %q is not a valid app directory name: %w", in.App, ErrValidation)
	}
	p := &planner{
		ctx: ctx, in: in, deps: deps, st: &setupState{plan: SetupPlan{App: in.App}},
		problemSeen: map[string]bool{}, badRoot: map[string]bool{}, stats: map[string]HostInfo{},
		planned: map[string]bool{}, declared: map[string]int{}, files: map[string]bool{}, envSet: map[string]bool{},
		envRel: "compose/" + in.App + "/.env",
	}
	p.checkRequires()
	fatal := false
	for _, fi := range setupFieldIssues(in.Setup) {
		p.problem("%s", fi.text())
		fatal = true
	}
	if in.Setup.App.Name != "" && in.Setup.App.Name != in.App {
		p.problem("setup.toml [app] name does not equal the app directory name %q", in.App)
	}
	if err := p.loadEnv(); err != nil {
		return nil, err
	}
	envSteps, envData, err := p.planEnv(fatal)
	if err != nil {
		return nil, err
	}
	if !p.resolveTargets() {
		fatal = true
	}
	if !fatal {
		if err := p.planHost(envSteps, envData); err != nil {
			return nil, err
		}
	}
	plan := &p.st.plan
	plan.Runnable = len(plan.Problems) == 0
	for _, s := range plan.Steps {
		if s.Action == ActionAsk {
			plan.Decisions = append(plan.Decisions, s.Path)
		}
	}
	plan.Digest = planDigest(plan.Steps, p.st.data)
	for _, pg := range in.Setup.Provision.Postgres {
		plan.Deferred = append(plan.Deferred,
			fmt.Sprintf("provision.postgres %s database %s: not run until M5", pg.Stack, pg.Database))
	}
	return p.st, nil
}

// planDigest hashes what a run would do. A step whose action came from an overwrite decision
// counts as ActionAsk, so making a decision does not change the digest.
func planDigest(steps []SetupStep, data []stepData) string {
	h := sha256.New()
	var decisions []string
	for i, s := range steps {
		action := s.Action
		if i < len(data) && data[i].decided {
			action = ActionAsk
		}
		if action == ActionAsk {
			decisions = append(decisions, s.Path)
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\n", s.Kind, s.Path, s.Key, action, s.Mode, s.Source)
	}
	slices.Sort(decisions)
	for _, d := range decisions {
		fmt.Fprintf(h, "decision\x00%s\n", d)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// problem records a reason the plan cannot run, once. Callers pass names and paths, never values.
func (p *planner) problem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.problemSeen[msg] {
		return
	}
	p.problemSeen[msg] = true
	p.st.plan.Problems = append(p.st.plan.Problems, msg)
}

func (p *planner) checkRequires() {
	for _, name := range p.in.Setup.App.Requires {
		status, ok := p.in.Statuses[name]
		rs := RequireState{Stack: name, Status: status, OK: ok && status == StatusReady}
		p.st.plan.Requires = append(p.st.plan.Requires, rs)
		switch {
		case !ok:
			p.problem("required stack %s does not exist", name)
		case !rs.OK:
			p.problem("required stack %s is %s, not Ready", name, status)
		}
	}
}

// loadEnv reads the env file and example.env and parses the one the env steps start from.
func (p *planner) loadEnv() error {
	st := p.st
	b, err := p.deps.Secrets.ReadEnv(p.ctx, p.in.App)
	switch {
	case err == nil:
		st.envCurrent, st.envExists = b, true
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("read %s: %w", p.envRel, err)
	}
	tmplRel := "compose/" + p.in.App + "/example.env"
	tmpl, err := p.deps.Repo.ReadFile(p.ctx, tmplRel)
	tmplOK := err == nil
	switch {
	case err == nil, errors.Is(err, ErrNotFound):
	case errors.Is(err, ErrUnreadable):
		p.problem("%s cannot be read (symlink leaving the repo, or not a regular file)", tmplRel)
	default:
		return fmt.Errorf("read %s: %w", tmplRel, err)
	}
	src, srcRel := st.envCurrent, p.envRel
	if !st.envExists && tmplOK {
		src, srcRel = tmpl, tmplRel
		st.envBase, p.fromTmpl = tmpl, true
	} else {
		st.envBase = st.envCurrent
	}
	d, err := ParseDotEnv(src)
	if err != nil {
		line := 0
		var pe *ParseError
		if errors.As(err, &pe) {
			line = pe.Line
		}
		p.problem("%s is not parseable: unterminated quoted value starting on line %d", srcRel, line)
		return nil
	}
	p.base, p.envOK = d, true
	return nil
}

// hasValue reports whether d gives key a value that is neither blank nor a placeholder.
func hasValue(d DotEnv, key string) bool {
	e, ok := d.Lookup(key)
	if !ok {
		return false
	}
	v := e.Value.Reveal() // compared, never stored or printed
	return strings.TrimSpace(v) != "" && !isPlaceholder(v)
}

// planEnv computes prompt states and the env file and env key steps.
func (p *planner) planEnv(fatal bool) ([]SetupStep, []stepData, error) {
	in := p.in
	prompts := map[string]bool{}
	for _, pr := range in.Setup.Prompts {
		prompts[pr.Key] = true
	}
	for _, k := range sortedKeys(in.Answers) {
		if !prompts[k] {
			p.problem("an answer was given for %s, which is not a [[prompts]] key", k)
		}
	}
	envStep := SetupStep{
		RootKind: RootApp, Root: p.deps.Repo.Root(), Rel: p.envRel,
		Path: path.Join(p.deps.Repo.Root(), p.envRel),
	}
	var steps []SetupStep
	var data []stepData
	for _, pr := range in.Setup.Prompts {
		ans, given := in.Answers[pr.Key]
		given = given && (ans.HasValue || ans.Generate)
		ps := PromptState{Key: pr.Key, Label: pr.Label, Secret: pr.Secret, CanGenerate: pr.Generate != nil, State: PromptMissing}
		step := envStep
		step.Kind, step.Key = StepEnvKey, pr.Key
		// A value in example.env is committed and public: it never stands in for a secret.
		d := stepData{hadValue: p.envOK && !p.fromTmpl && hasValue(p.base, pr.Key)}
		switch {
		case given && ans.HasValue && ans.Generate:
			ps.State = PromptAnswered
			p.problem("prompt %s was given both a value and a request to generate one", pr.Key)
		case given && ans.Generate:
			ps.State = PromptAnswered
			if pr.Generate == nil {
				p.problem("prompt %s offers no generated value", pr.Key)
				break
			}
			if d.hadValue {
				p.problem("prompt %s already has a value; unset it in the env editor to generate a new one", pr.Key)
				break
			}
			step.Action, step.Reason, d.generate = ActionSet, generatedReason(*pr.Generate), pr.Generate
		case given:
			ps.State = PromptAnswered
			switch {
			case !p.answerOK(pr.Key, ans.Value):
			case d.hadValue && sameValue(p.base, pr.Key, ans.Value):
				step.Action, step.Reason = ActionKeep, "answer equals the current value"
			default:
				step.Action, step.Reason, d.answer = ActionSet, "from answer", ans.Value
			}
		case d.hadValue:
			ps.State = PromptSet
			step.Action, step.Reason = ActionKeep, "already set"
		case pr.Generate != nil:
			step.Action, step.Reason, d.generate = ActionSet, generatedReason(*pr.Generate), pr.Generate
		default:
			label := ""
			if pr.Label != "" {
				label = fmt.Sprintf(" (%s)", pr.Label)
			}
			p.problem("prompt %s%s needs an answer: %s has no value for it and it offers no generated value", pr.Key, label, p.envRel)
		}
		p.st.plan.Prompts = append(p.st.plan.Prompts, ps)
		if step.Action == ActionSet {
			p.envSet[pr.Key] = true
		}
		if step.Action != "" {
			steps = append(steps, step)
			data = append(data, d)
		}
	}
	if fatal || !p.envOK {
		return nil, nil, nil
	}

	writes := false
	for _, s := range steps {
		writes = writes || s.Action == ActionSet
	}
	file := envStep
	file.Kind, file.Mode = StepEnvFile, defaultSecretMode
	switch {
	case p.st.envExists:
		if len(in.Setup.Prompts) == 0 {
			return nil, nil, nil
		}
		file.Action, file.Reason = ActionKeep, "already present"
	case p.fromTmpl:
		file.Action, file.Source = ActionCreate, "compose/"+in.App+"/example.env"
		file.Reason = "copy of " + file.Source
		writes = true
	case writes:
		file.Action, file.Reason = ActionCreate, "new file"
	default:
		return nil, nil, nil
	}
	if writes {
		tracked, err := p.deps.Repo.Tracked(p.ctx, p.envRel)
		if err != nil {
			return nil, nil, fmt.Errorf("check whether %s is tracked: %w", p.envRel, err)
		}
		if tracked {
			p.problem("%s is tracked by git and is never written (GIT001): gitignore it and git rm --cached it", p.envRel)
		}
		ignored, err := p.deps.Repo.Ignored(p.ctx, p.envRel)
		if err != nil {
			return nil, nil, fmt.Errorf("check whether %s is gitignored: %w", p.envRel, err)
		}
		if !ignored {
			p.problem("%s is not gitignored and is never written (GIT001): add compose/*/.env to .gitignore", p.envRel)
		}
	}
	return append([]SetupStep{file}, steps...), append([]stepData{{}}, data...), nil
}

// sameValue reports whether d gives key exactly the value v.
func sameValue(d DotEnv, key string, v Secret) bool {
	e, ok := d.Lookup(key)
	return ok && e.Value.Reveal() == v.Reveal() // compared, never stored or printed
}

// answerOK checks a prompt answer against [env_validators]; messages name the key, not the value.
func (p *planner) answerOK(key string, v Secret) bool {
	value := v.Reveal() // validated, never stored or printed
	switch {
	case strings.TrimSpace(value) == "":
		p.problem("the answer for prompt %s is empty", key)
		return false
	case isPlaceholder(value):
		p.problem("the answer for prompt %s is a placeholder", key)
		return false
	}
	if spec, ok := p.in.Setup.EnvValidators[key]; ok && knownValidator(spec.Type) {
		if msg := ValidateValue(spec, value, p.in.App); msg != "" {
			p.problem("the answer for prompt %s %s (setup.toml type %q)", key, msg, spec.Type)
			return false
		}
	}
	return true
}

// linkProblem is the problem for a symbolic link where setup would write: a link can make a
// missing file look present, or point a write somewhere the plan does not show.
const linkProblem = "%s is a symbolic link; setup does not write through links"

// reasonDeclared is the reason of a directory created because setup.toml declares it.
const reasonDeclared = "declared"

func generatedReason(g SetupGenerate) string {
	switch g.Encoding {
	case encodingAlphanumeric:
		return fmt.Sprintf("generated %d alphanumeric characters", g.Bytes)
	case encodingHex:
		return fmt.Sprintf("generated %d bytes hex", g.Bytes)
	default:
		return fmt.Sprintf("generated %d bytes base64", g.Bytes)
	}
}

// resolveTargets interpolates every host path and config source and places each under a
// permitted root. It returns false when any ${...} is unresolved or invalid: nothing may then be
// planned, because the paths that did resolve may not be the paths the author meant.
func (p *planner) resolveTargets() bool {
	f := p.in.Setup
	p.setRoots()
	ok := true
	resolve := func(desc, raw string) (string, bool) {
		ts := ScanTemplate(raw)
		if len(ts.Invalid) > 0 {
			p.problem("setup.toml %s has an invalid ${...} template", desc)
			ok = false
			return "", false
		}
		// Only public, orchestrator-level variables may shape a path: a value from the app's
		// env file may be a secret, and a resolved path is shown in plans and job output.
		sourced := true
		for _, ref := range ts.Refs {
			if ev, found := p.in.Deploy.Lookup(ref.Name); found && ev.Source != SourceInjected && ev.Source != globalEnvPath {
				p.problem("setup.toml %s uses ${%s}, which comes from %s; setup.toml may use only variables from %s "+
					"or injected by the orchestrator", desc, ref.Name, ev.Source, globalEnvPath)
				sourced = false
			}
		}
		if !sourced {
			ok = false
			return "", false
		}
		out, missing := Interpolate(raw, p.in.Deploy.lookupFunc())
		for _, name := range missing {
			p.problem("setup.toml uses ${%s}, which the deploy env does not define", name)
		}
		if len(missing) > 0 {
			ok = false
			return "", false
		}
		return out, true
	}
	place := func(desc, raw string) target {
		r, resolved := resolve(desc, raw)
		if !resolved {
			return target{}
		}
		return p.place(desc, raw, r)
	}
	for i, d := range f.Dirs {
		p.dirT = append(p.dirT, place(fmt.Sprintf("[[dirs]] #%d path", i+1), d.Path))
	}
	for i, s := range f.Secrets {
		p.secretT = append(p.secretT, place(fmt.Sprintf("[[secrets]] #%d file", i+1), s.File))
	}
	for i, c := range f.ConfigFiles {
		p.configT = append(p.configT, place(fmt.Sprintf("[[config_files]] #%d to", i+1), c.To))
		if c.From == "" {
			continue
		}
		desc := fmt.Sprintf("[[config_files]] #%d from", i+1)
		from, resolved := resolve(desc, c.From)
		switch {
		case !resolved:
		case !relInside(from):
			p.problem("setup.toml %s %s is not a relative path inside the app directory", desc, c.From)
		case isEnvFileName(from):
			p.problem("setup.toml %s %s is an env file; secrets reach the host only through [[secrets]]", desc, c.From)
		}
	}
	return ok
}

// setRoots records the permitted roots and the protected repo directories.
func (p *planner) setRoots() {
	referenced := map[string]bool{}
	for _, raw := range p.in.Setup.DeclaredPaths() {
		for _, r := range ScanTemplate(raw).Refs {
			referenced[r.Name] = true
		}
	}
	for _, r := range []storageRoot{{key: "APPCONFIG_STORAGE", kind: RootAppConfig}, {key: "DATABASE_STORAGE", kind: RootDatabase}} {
		ev, ok := p.in.Deploy.Lookup(r.key)
		if !ok {
			continue
		}
		v := ev.Value.Reveal() // a storage root path, which plans and findings show
		if !path.IsAbs(v) || path.Clean(v) == "/" {
			if referenced[r.key] {
				p.problem("%s must be an absolute directory other than /", r.key)
			}
			continue
		}
		r.path = path.Clean(v)
		p.roots = append(p.roots, r)
	}
	// The repo directories must be absolute, or the check that keeps host steps out of them
	// could not hold.
	if ev, ok := p.in.Deploy.Lookup("HOMELAB_STORAGE"); ok {
		if v := ev.Value.Reveal(); path.IsAbs(v) { // a repo path, which plans show
			p.protected = append(p.protected, path.Clean(v))
		} else {
			p.problem("HOMELAB_STORAGE must be an absolute path")
		}
	}
	if root := p.deps.Repo.Root(); path.IsAbs(root) {
		p.protected = append(p.protected, path.Clean(root))
	} else {
		p.problem("the homelab repo path must be absolute")
	}
}

// place confines a resolved path to the deepest permitted root that contains it. raw is the
// template, quoted where the resolved value may not be a path at all.
func (p *planner) place(desc, raw, resolved string) target {
	if !path.IsAbs(resolved) {
		p.problem("setup.toml %s %s is not an absolute path", desc, raw)
		return target{}
	}
	if slices.Contains(strings.Split(resolved, "/"), "..") {
		p.problem("setup.toml %s %s contains a .. segment", desc, resolved)
		return target{}
	}
	c := path.Clean(resolved)
	for _, prot := range p.protected {
		if _, in := under(c, prot); in {
			p.problem("setup.toml %s %s is inside the homelab repo, which setup writes only through the env file", desc, c)
			return target{}
		}
	}
	var best target
	for _, r := range p.roots {
		if rel, in := under(c, r.path); in && len(r.path) >= len(best.root.path) {
			best = target{root: r, rel: rel, path: c, ok: true}
		}
	}
	switch {
	case !best.ok:
		p.problem("setup.toml %s %s is not under APPCONFIG_STORAGE or DATABASE_STORAGE", desc, c)
		return target{}
	case best.rel == ".":
		p.problem("setup.toml %s %s is a storage root itself; setup never creates or changes a root", desc, c)
		return target{}
	}
	return best
}

// planHost plans directories, secrets and config files and assembles the steps in order.
func (p *planner) planHost(envSteps []SetupStep, envData []stepData) error {
	f := p.in.Setup
	seen := map[string]bool{}
	for _, ts := range [][]target{p.dirT, p.secretT, p.configT} {
		for i := range ts {
			t := &ts[i]
			if !t.ok {
				continue
			}
			if seen[t.key()] {
				p.problem("%s is declared more than once in setup.toml", t.path)
				t.ok = false
				continue
			}
			seen[t.key()] = true
		}
	}
	for _, t := range slices.Concat(p.secretT, p.configT) {
		if t.ok {
			p.files[t.key()] = true
		}
	}
	all := slices.Concat(p.dirT, p.secretT, p.configT)
	// Each root must exist: setup never creates one.
	for _, t := range all {
		if !t.ok || p.badRoot[t.root.path] {
			continue
		}
		if _, ok := p.stats[t.root.path+"\x00."]; ok {
			continue
		}
		info, err := p.deps.Host.Stat(p.ctx, t.root.path, ".")
		switch {
		case errors.Is(err, ErrValidation), err == nil && !info.IsDir:
			p.problem("%s %s is not an existing directory", t.root.key, t.root.path)
			p.badRoot[t.root.path] = true
		case err != nil:
			return fmt.Errorf("stat %s: %w", t.root.path, err)
		default:
			p.stats[t.root.path+"\x00."] = info
		}
	}
	usable := func(t target) bool { return t.ok && !p.badRoot[t.root.path] }

	for i, t := range p.dirT {
		if usable(t) {
			if _, dup := p.declared[t.key()]; !dup {
				p.declared[t.key()] = i
			}
		}
	}
	for i, t := range p.dirT {
		if !usable(t) {
			continue
		}
		if _, err := p.planDeclaredDir(i); err != nil {
			return err
		}
	}

	var secrets, configs []SetupStep
	var secretData, configData []stepData
	for i, t := range p.secretT {
		if !usable(t) {
			continue
		}
		step, d, ok, err := p.planSecret(f.Secrets[i], i, t)
		if err != nil {
			return err
		}
		if ok {
			secrets, secretData = append(secrets, step), append(secretData, d)
		}
	}
	for i, t := range p.configT {
		if !usable(t) {
			continue
		}
		step, d, ok, err := p.planConfig(f.ConfigFiles[i], i, t)
		if err != nil {
			return err
		}
		if ok {
			configs, configData = append(configs, step), append(configData, d)
		}
	}
	p.st.plan.Steps = slices.Concat(envSteps, p.dirs, secrets, configs)
	p.st.data = slices.Concat(envData, p.dirData, secretData, configData)
	return nil
}

// stat describes rel under r through the host, caching the answer. A path the host refuses (an
// escape, or a parent that is not a directory) is a problem and reports ok false.
func (p *planner) stat(r storageRoot, rel string) (HostInfo, bool, error) {
	k := r.path + "\x00" + rel
	if info, ok := p.stats[k]; ok {
		return info, true, nil
	}
	info, err := p.deps.Host.Stat(p.ctx, r.path, rel)
	switch {
	case errors.Is(err, ErrValidation), errors.Is(err, ErrConflict):
		p.problem("%s cannot be used: %v", path.Join(r.path, rel), err)
		return HostInfo{}, false, nil
	case err != nil:
		return HostInfo{}, false, fmt.Errorf("stat %s: %w", path.Join(r.path, rel), err)
	}
	p.stats[k] = info
	return info, true, nil
}

func (p *planner) step(kind SetupStepKind, t target, mode FileMode, action SetupAction, reason string) SetupStep {
	return SetupStep{
		Kind: kind, RootKind: t.root.kind, Root: t.root.path, Rel: t.rel, Path: t.path,
		Mode: mode, Action: action, Reason: reason,
	}
}

// ensureParents plans every missing ancestor directory of t, parents first.
func (p *planner) ensureParents(t target) (bool, error) {
	parts := strings.Split(t.rel, "/")
	for i := 1; i < len(parts); i++ {
		anc := target{root: t.root, rel: strings.Join(parts[:i], "/"), ok: true}
		anc.path = path.Join(t.root.path, anc.rel)
		if ok, err := p.ensureDir(anc, t.path); !ok || err != nil {
			return ok, err
		}
	}
	return true, nil
}

// ensureDir makes sure the directory t exists or is planned: a declared directory with its
// declared mode, an existing one as is, anything else as an implied directory with mode 0750.
func (p *planner) ensureDir(t target, child string) (bool, error) {
	k := t.key()
	switch {
	case p.planned[k]:
		return true, nil
	case p.files[k]:
		p.problem("%s is declared as a file but is a parent directory of %s", t.path, child)
		return false, nil
	}
	if i, ok := p.declared[k]; ok {
		return p.planDeclaredDir(i)
	}
	info, ok, err := p.stat(t.root, t.rel)
	if !ok || err != nil {
		return false, err
	}
	switch {
	case info.IsSymlink:
		p.problem(linkProblem, t.path)
		return false, nil
	case info.Exists && info.IsDir:
	case info.Exists:
		p.problem("%s exists and is not a directory, but %s needs it as one", t.path, child)
		return false, nil
	default:
		p.dirs = append(p.dirs, p.step(StepDir, t, defaultDirMode, ActionCreate, "parent of "+child))
		p.dirData = append(p.dirData, stepData{})
	}
	p.planned[k] = true
	return true, nil
}

// planDeclaredDir plans [[dirs]] entry i after its parents.
func (p *planner) planDeclaredDir(i int) (bool, error) {
	t := p.dirT[i]
	if p.planned[t.key()] {
		return true, nil
	}
	p.planned[t.key()] = true // a cycle is impossible: ancestors are strictly shorter
	if ok, err := p.ensureParents(t); !ok || err != nil {
		return ok, err
	}
	info, ok, err := p.stat(t.root, t.rel)
	if !ok || err != nil {
		return false, err
	}
	mode := modeOr(p.in.Setup.Dirs[i].Mode, defaultDirMode)
	switch {
	case info.IsSymlink:
		p.problem(linkProblem, t.path)
		return false, nil
	case info.Exists && info.IsDir:
		reason := "already present"
		if info.Mode != mode {
			reason = fmt.Sprintf("already present with mode %s; setup does not change it", info.Mode)
		}
		p.dirs = append(p.dirs, p.step(StepDir, t, mode, ActionKeep, reason))
	case info.Exists:
		p.problem("%s exists and is not a directory", t.path)
		return false, nil
	default:
		p.dirs = append(p.dirs, p.step(StepDir, t, mode, ActionCreate, reasonDeclared))
	}
	p.dirData = append(p.dirData, stepData{})
	return true, nil
}

func (p *planner) planSecret(s SetupSecret, i int, t target) (SetupStep, stepData, bool, error) {
	if ok, err := p.ensureParents(t); !ok || err != nil {
		return SetupStep{}, stepData{}, false, err
	}
	info, ok, err := p.stat(t.root, t.rel)
	if !ok || err != nil {
		return SetupStep{}, stepData{}, false, err
	}
	mode := modeOr(s.Mode, defaultSecretMode)
	switch {
	case info.IsSymlink:
		p.problem(linkProblem, t.path)
		return SetupStep{}, stepData{}, false, nil
	case info.Exists && info.IsDir:
		p.problem("%s is a directory, but [[secrets]] #%d declares it as a file", t.path, i+1)
		return SetupStep{}, stepData{}, false, nil
	case info.Exists && p.envSet[s.ValueFrom]:
		// The env key changes but this file, fed by it, is never overwritten: the two would
		// disagree, as a database password and the secret file holding it.
		p.problem("[[secrets]] #%d %s already exists and is never overwritten, so it would no longer match the new value of %s; "+
			"answer %s with the value the file holds, or remove the file to regenerate it", i+1, t.path, s.ValueFrom, s.ValueFrom)
		return SetupStep{}, stepData{}, false, nil
	case info.Exists:
		return p.step(StepSecret, t, mode, ActionKeep, "already present; never overwritten"), stepData{}, true, nil
	case s.Generate != nil:
		return p.step(StepSecret, t, mode, ActionCreate, generatedReason(*s.Generate)), stepData{generate: s.Generate}, true, nil
	}
	isPrompt := slices.ContainsFunc(p.in.Setup.Prompts, func(pr SetupPrompt) bool { return pr.Key == s.ValueFrom })
	reason := "from prompt " + s.ValueFrom
	if !isPrompt {
		reason = "from env " + s.ValueFrom
		if p.fromTmpl {
			p.problem("[[secrets]] #%d value_from %s cannot be satisfied: %s does not exist, and a value in example.env "+
				"is committed and never used as a secret", i+1, s.ValueFrom, p.envRel)
			return SetupStep{}, stepData{}, false, nil
		}
		if !p.envOK || !hasValue(p.base, s.ValueFrom) {
			p.problem("[[secrets]] #%d value_from %s names neither a prompt nor a key with a value in %s", i+1, s.ValueFrom, p.envRel)
			return SetupStep{}, stepData{}, false, nil
		}
	}
	return p.step(StepSecret, t, mode, ActionCreate, reason), stepData{valueFrom: s.ValueFrom}, true, nil
}

func (p *planner) planConfig(c SetupConfigFile, i int, t target) (SetupStep, stepData, bool, error) {
	content := []byte{}
	source := ""
	if c.From != "" {
		from, _ := Interpolate(c.From, p.in.Deploy.lookupFunc()) // resolved and checked earlier
		if !relInside(from) {
			return SetupStep{}, stepData{}, false, nil
		}
		source = "compose/" + p.in.App + "/" + path.Clean(from)
		b, err := p.deps.Repo.ReadFile(p.ctx, source)
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrUnreadable):
			p.problem("[[config_files]] #%d from %s is not a readable file in the repo", i+1, source)
			return SetupStep{}, stepData{}, false, nil
		case err != nil:
			return SetupStep{}, stepData{}, false, fmt.Errorf("read %s: %w", source, err)
		}
		content = b
	}
	if ok, err := p.ensureParents(t); !ok || err != nil {
		return SetupStep{}, stepData{}, false, err
	}
	info, ok, err := p.stat(t.root, t.rel)
	if !ok || err != nil {
		return SetupStep{}, stepData{}, false, err
	}
	d := stepData{content: content}
	mk := func(action SetupAction, reason string) (SetupStep, stepData, bool, error) {
		s := p.step(StepConfigFile, t, defaultConfigMode, action, reason)
		s.Source = source
		return s, d, true, nil
	}
	what := "an empty file"
	if source != "" {
		what = "copy of " + source
	}
	switch {
	case info.IsSymlink:
		p.problem(linkProblem, t.path)
		return SetupStep{}, stepData{}, false, nil
	case info.Exists && info.IsDir:
		p.problem("%s is a directory, but [[config_files]] #%d declares it as a file", t.path, i+1)
		return SetupStep{}, stepData{}, false, nil
	case !info.Exists:
		return mk(ActionCreate, what)
	}
	onExists := c.OnExists
	if onExists == "" || onExists == onExistsKeep {
		return mk(ActionKeep, "already present; on_exists = keep")
	}
	cur, err := p.deps.Host.ReadFile(p.ctx, t.root.path, t.rel)
	switch {
	case errors.Is(err, ErrNotFound):
		return mk(ActionCreate, what)
	case errors.Is(err, ErrValidation), errors.Is(err, ErrConflict):
		p.problem("%s cannot be read: %v", t.path, err)
		return SetupStep{}, stepData{}, false, nil
	case err != nil:
		return SetupStep{}, stepData{}, false, fmt.Errorf("read %s: %w", t.path, err)
	case bytes.Equal(cur, content):
		return mk(ActionUnchanged, "identical to "+what)
	case onExists == onExistsOverwrite:
		return mk(ActionOverwrite, "differs from "+what+"; on_exists = overwrite")
	}
	switch overwrite, decided := p.in.Overwrite[t.path]; {
	case !decided:
		return mk(ActionAsk, "differs from "+what+"; overwrite or keep?")
	case overwrite:
		d.decided = true
		return mk(ActionOverwrite, "differs from "+what+"; overwrite chosen")
	default:
		d.decided = true
		return mk(ActionKeep, "differs from "+what+"; keep chosen")
	}
}

// isEnvFileName reports whether p names an env file, which only SecretStore may read.
func isEnvFileName(p string) bool {
	switch path.Base(p) {
	case ".env", ".global.env":
		return true
	}
	return false
}
