package homelab

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// validator holds what every stack shares.
type validator struct {
	repo   Repo
	parse  ComposeParser
	opts   Options
	global envRead
}

// stack is the working state of one app's validation.
type stack struct {
	app string
	dir string // compose/<app>

	findings []Finding
	// unresolved lists, in order, the names reported as ENV001; ENV008 reports unused keys only
	// when it is non-empty.
	unresolved []string
	// envProblems holds the env files whose unreadable or unparseable state is already reported.
	envProblems map[string]bool

	cf      ComposeFile
	appEnv  envRead
	example envRead
	deploy  EnvScope

	services map[string]*serviceScope
	// ownEnvMissing is set when compose/<app>/.env is absent although the stack expects it.
	ownEnvMissing bool
	// declaredMissing is set when a path setup.toml will create is reported missing.
	declaredMissing bool

	setup        *SetupFile
	requires     []string
	requiresLine int

	status        Status
	interpolation EnvScope
	container     EnvScope
}

// serviceScope is one service's container scope.
type serviceScope struct {
	b *scopeBuilder
	// unknown is set when an env_file: entry could not be read (outside the repo, unreadable,
	// unparseable), so the scope's key set is incomplete.
	unknown bool
	// selfRef holds environment: keys whose value interpolates the same name (KEY: ${KEY}).
	// Such a key exists in the container only if the deploy env already supplies it, so it
	// does not count as container-only availability for ENV003.
	selfRef map[string]bool
}

func (s *stack) add(code Code, file string, line int, msg string) {
	s.findings = append(s.findings, newFinding(code, file, line, msg))
}

// addOwnEnvMissing emits ENV004 for the app's own compose/<app>/.env. Setup creates that file
// from example.env, so its absence is outstanding setup: warning severity, status NeedsSetup.
func (s *stack) addOwnEnvMissing(file string, line int, msg string) {
	s.ownEnvMissing = true
	f := newFinding(ENV004, file, line, msg)
	f.Severity = SeverityWarning
	s.findings = append(s.findings, f)
}

// reportEnvProblem emits ENV004 (error) once per env file that exists but cannot be used: not a
// readable regular file, or an unterminated quoted value. name is what the message shows.
func (s *stack) reportEnvProblem(rel, name, file string, line int, r envRead) {
	if s.envProblems[rel] {
		return
	}
	switch {
	case r.unreadable:
		s.add(ENV004, file, line, fmt.Sprintf(
			"env_file %s cannot be read (symlink leaving the repo, or not a regular file)", name))
	case r.parseErr != nil:
		s.add(ENV004, file, line, fmt.Sprintf(
			"env file %s line %d has an unterminated quoted value; the keys after it are ignored",
			name, r.parseErr.Line))
	default:
		return
	}
	if s.envProblems == nil {
		s.envProblems = map[string]bool{}
	}
	s.envProblems[rel] = true
}

// reportFileProblem reports a problem with an env file the pipeline reads directly, pointing at
// the file itself.
func (s *stack) reportFileProblem(rel string, r envRead) {
	line := 0
	if r.parseErr != nil {
		line = r.parseErr.Line
	}
	s.reportEnvProblem(rel, rel, rel, line, r)
}

func (s *stack) appEnvPath() string  { return s.dir + "/.env" }
func (s *stack) examplePath() string { return s.dir + "/example.env" }

func (v *validator) validateStack(ctx context.Context, app string) (*stack, error) {
	s := &stack{app: app, dir: composeDir + "/" + app, services: map[string]*serviceScope{}}
	if !identifierRe.MatchString(app) {
		s.add(COMPOSE006, s.dir, 0, fmt.Sprintf(
			"app directory name %q does not match [a-z0-9][a-z0-9-]*; the stack is not validated", app))
		s.status = s.deriveStatus()
		return s, nil
	}

	var err error
	if s.appEnv, err = readEnv(ctx, v.repo, s.appEnvPath()); err != nil {
		return nil, err
	}
	if s.example, err = readEnv(ctx, v.repo, s.examplePath()); err != nil {
		return nil, err
	}
	s.deploy = DeployEnv(v.opts.HomelabStorage, v.global.env, s.appEnv.env, globalEnvPath, s.appEnvPath())
	s.interpolation = s.deploy

	ok, err := v.loadCompose(ctx, s)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.status = s.deriveStatus()
		return s, nil
	}
	s.reportFileProblem(globalEnvPath, v.global)
	s.reportFileProblem(s.appEnvPath(), s.appEnv)
	s.reportFileProblem(s.examplePath(), s.example)

	if err := v.loadSetup(ctx, s); err != nil {
		return nil, err
	}
	if err := v.buildContainerScopes(ctx, s); err != nil {
		return nil, err
	}
	s.checkOwnEnv()
	s.checkInterpolationRefs()
	s.checkContainerRefs()
	s.checkEnvFiles()
	s.checkValidators()
	if err := v.checkPaths(s); err != nil {
		return nil, err
	}
	s.checkNetworks()
	s.checkLimits()

	s.interpolation = s.interpolationScope()
	s.container = s.containerScope()
	s.status = s.deriveStatus()
	return s, nil
}

// loadCompose finds, reads and parses the compose file. It returns false when no further checks
// can run (no readable compose file, or COMPOSE002).
func (v *validator) loadCompose(ctx context.Context, s *stack) (bool, error) {
	rel := s.dir + "/compose.yaml"
	src, found, err := readOptional(ctx, v.repo, rel)
	if !found && err == nil {
		legacy := s.dir + "/compose.yml"
		src, found, err = readOptional(ctx, v.repo, legacy)
		if found || errors.Is(err, ErrUnreadable) {
			rel = legacy
			s.add(COMPOSE004, rel, 0, "compose file is named compose.yml; the layout contract requires compose.yaml")
		}
	}
	if errors.Is(err, ErrUnreadable) {
		s.add(COMPOSE005, rel, 0, fmt.Sprintf(
			"%s cannot be read (symlink leaving the repo, or not a regular file)", rel))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !found {
		s.add(COMPOSE005, s.dir, 0, fmt.Sprintf("%s has no compose.yaml", s.dir))
		return false, nil
	}
	cf, err := v.parse.Parse(rel, src)
	if err != nil {
		line, msg := 0, err.Error()
		var pe *ParseError
		if errors.As(err, &pe) {
			line, msg = pe.Line, pe.Message
		}
		s.add(COMPOSE002, rel, line, "not parseable YAML: "+msg)
		return false, nil
	}
	s.cf = cf
	for _, se := range cf.SchemaErrors {
		s.add(COMPOSE001, rel, se.Line, "not valid per the Compose spec: "+se.Message)
	}
	return true, nil
}

// loadSetup reads setup.sh presence and setup.toml.
func (v *validator) loadSetup(ctx context.Context, s *stack) error {
	sh, err := v.repo.Stat(ctx, s.dir+"/setup.sh")
	switch {
	case errors.Is(err, ErrUnreadable):
		sh.Exists = true // present, whatever it points at
	case err != nil:
		return fmt.Errorf("stat setup.sh: %w", err)
	}
	if sh.Exists {
		s.add(SETUP001, s.dir+"/setup.sh", 0, "legacy setup.sh is present; it is never executed")
	}
	rel := s.dir + "/setup.toml"
	src, found, err := readOptional(ctx, v.repo, rel)
	if errors.Is(err, ErrUnreadable) {
		s.add(SETUP005, rel, 0, "setup.toml cannot be read (symlink leaving the repo, or not a regular file)")
		return nil
	}
	if err != nil || !found {
		return err
	}
	f, issues, err := ParseSetup(src)
	if err != nil {
		line, msg := 0, err.Error()
		var pe *ParseError
		if errors.As(err, &pe) {
			line, msg = pe.Line, pe.Message
		}
		s.add(SETUP005, rel, line, "setup.toml is not parseable TOML: "+msg)
		return nil
	}
	for _, is := range issues {
		s.add(SETUP002, rel, is.Line, is.Message)
	}
	if f.App.Name != "" && f.App.Name != s.app {
		s.add(SETUP006, rel, tomlKeyLine(src, "name"),
			fmt.Sprintf("[app] name does not equal the app directory name %q", s.app))
	}
	s.setup = &f
	s.requires = f.App.Requires
	s.requiresLine = tomlKeyLine(src, "requires")
	return nil
}

// envFileName is how an env_file: entry is named in messages and in EnvVar.Source: the
// repo-relative path when the raw path interpolates nothing but HOMELAB_STORAGE, otherwise the
// raw path from compose.yaml, so that no other variable's value is ever printed.
func envFileName(raw, rel string) string {
	for _, r := range ScanTemplate(raw).Refs {
		if r.Name != "HOMELAB_STORAGE" {
			return raw
		}
	}
	return rel
}

// buildContainerScopes resolves each service's env_file: chain and environment: block, and
// reports env_file: paths that do not exist or cannot be used (ENV004).
func (v *validator) buildContainerScopes(ctx context.Context, s *stack) error {
	cache := map[string]envRead{globalEnvPath: v.global, s.appEnvPath(): s.appEnv}
	lookup := s.deploy.lookupFunc()
	for _, svc := range s.cf.Services {
		sc := &serviceScope{b: newScopeBuilder(), selfRef: map[string]bool{}}
		s.services[svc.Name] = sc
		for _, ef := range svc.EnvFiles {
			raw := ef.Path.Value
			resolved, missing := Interpolate(raw, lookup)
			if len(missing) > 0 {
				sc.unknown = true // ENV001 reports the unresolved variable
				continue
			}
			rel, inRepo := v.repoRel(s, resolved)
			if !inRepo {
				sc.unknown = true
				continue
			}
			name := envFileName(raw, rel)
			r, seen := cache[rel]
			if !seen {
				var err error
				if r, err = readEnv(ctx, v.repo, rel); err != nil {
					return err
				}
				cache[rel] = r
			}
			switch {
			case r.unreadable:
				sc.unknown = true
				s.reportEnvProblem(rel, raw, s.cf.Path, ef.Path.Line, r)
				continue
			case !r.found:
				if ef.Required {
					s.reportMissingEnvFile(rel, name, ef.Path.Line)
				}
				continue
			case r.parseErr != nil:
				sc.unknown = true
				s.reportEnvProblem(rel, name, s.cf.Path, ef.Path.Line, r)
			}
			sc.b.addDotEnv(r.env, name)
		}
		for _, e := range svc.Environment {
			if !e.HasValue {
				if dv, ok := s.deploy.Lookup(e.Key); ok {
					sc.b.set(EnvVar{Key: e.Key, Value: dv.Value, Source: SourceEnvironment, Line: e.Line})
				}
				continue
			}
			for _, r := range ScanTemplate(e.Value).Refs {
				if r.Name == e.Key {
					sc.selfRef[e.Key] = true
				}
			}
			val, _ := Interpolate(e.Value, lookup)
			sc.b.set(EnvVar{Key: e.Key, Value: NewSecret(val), Source: SourceEnvironment, Line: e.Line})
		}
	}
	return nil
}

// reportMissingEnvFile emits ENV004 for an env_file: entry: a warning for the app's own .env,
// an error for any other path.
func (s *stack) reportMissingEnvFile(rel, name string, line int) {
	msg := fmt.Sprintf("env_file %s does not exist", name)
	if rel == s.appEnvPath() {
		if s.ownEnvMissing {
			return
		}
		if s.example.found {
			msg += "; create it from example.env"
		}
		s.addOwnEnvMissing(s.cf.Path, line, msg)
		return
	}
	s.add(ENV004, s.cf.Path, line, msg)
}

// checkOwnEnv reports a missing compose/<app>/.env that no env_file: entry already reported.
// The orchestrator reads it for the deploy env whether or not env_file: lists it, so it is
// expected whenever the app ships an example.env.
func (s *stack) checkOwnEnv() {
	if s.appEnv.found || s.appEnv.unreadable || s.ownEnvMissing || !s.example.found {
		return
	}
	s.addOwnEnvMissing(s.appEnvPath(), 0,
		fmt.Sprintf("%s does not exist; create it from example.env", s.appEnvPath()))
}

// repoRel maps a resolved env_file: path to a repo-relative path. A relative path is relative to
// the compose file's directory; an absolute path maps when it is under HOMELAB_STORAGE or the
// repo root. Anything else is outside the repo and cannot be read.
func (v *validator) repoRel(s *stack, p string) (string, bool) {
	if !path.IsAbs(p) {
		rel := path.Join(s.dir, p)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", false
		}
		return rel, true
	}
	p = path.Clean(p)
	var roots []string
	if hs, ok := s.deploy.Lookup("HOMELAB_STORAGE"); ok {
		roots = append(roots, hs.Value.Reveal())
	}
	roots = append(roots, filepath.ToSlash(v.repo.Root()))
	for _, root := range roots {
		if rel, ok := under(p, root); ok && rel != "." {
			return rel, true
		}
	}
	return "", false
}

// under reports whether p is root or inside it, and p relative to root.
func under(p, root string) (string, bool) {
	if root == "" || !path.IsAbs(root) {
		return "", false
	}
	root = path.Clean(root)
	if p == root {
		return ".", true
	}
	if root == "/" {
		return p[1:], true
	}
	if strings.HasPrefix(p, root+"/") {
		return p[len(root)+1:], true
	}
	return "", false
}

// scopeHas reports whether name is available in the container scope of service (all services
// when service is empty), ignoring self-referencing environment: entries, and the source.
func (s *stack) scopeHas(service, name string) (string, bool) {
	check := func(sc *serviceScope) (string, bool) {
		if sc == nil || sc.selfRef[name] {
			return "", false
		}
		if v, ok := sc.b.vars[name]; ok {
			return v.Source, true
		}
		return "", false
	}
	if service != "" {
		return check(s.services[service])
	}
	for _, svc := range s.cf.Services {
		if src, ok := check(s.services[svc.Name]); ok {
			return src, true
		}
	}
	return "", false
}

// interpolationScope is the deploy env plus the defaults that fill unresolved ${VAR:-d}.
func (s *stack) interpolationScope() EnvScope {
	b := newScopeBuilder()
	for _, v := range s.deploy.Vars {
		b.set(v)
	}
	lookup := s.deploy.lookupFunc()
	for _, r := range s.cf.Refs {
		if !r.HasDefault || s.fallbackResolved(r) {
			continue
		}
		if _, ok := s.deploy.Lookup(r.Name); ok {
			continue
		}
		val, _ := Interpolate(r.Default, lookup)
		b.setIfAbsent(EnvVar{Key: r.Name, Value: NewSecret(val), Source: SourceDefault, Line: r.Line})
	}
	return b.scope()
}

// containerScope merges the services' scopes. With several services, the alphabetically first
// service that defines a key provides its value and source.
func (s *stack) containerScope() EnvScope {
	names := make([]string, 0, len(s.services))
	for n := range s.services {
		names = append(names, n)
	}
	slices.Sort(names)
	b := newScopeBuilder()
	for _, n := range names {
		for _, v := range s.services[n].b.vars {
			b.setIfAbsent(v)
		}
	}
	return b.scope()
}

// fallbackResolved reports whether r sits in the default of a reference that supplies a value,
// so it is never evaluated: the enclosing name is set, and for :- also non-empty.
func (s *stack) fallbackResolved(r Ref) bool {
	for _, f := range r.Fallback {
		if v, ok := s.deploy.Lookup(f.Name); ok && (!f.Colon || v.Value.Reveal() != "") {
			return true
		}
	}
	return false
}

// deriveStatus applies docs/validator-checks.md: any error is Broken; outstanding setup — the
// app's own .env, or a path setup.toml will create — is NeedsSetup; otherwise Ready.
func (s *stack) deriveStatus() Status {
	for _, f := range s.findings {
		if f.Severity == SeverityError {
			return StatusBroken
		}
	}
	if s.ownEnvMissing || s.declaredMissing {
		return StatusNeedsSetup
	}
	return StatusReady
}

// checkRequires emits SETUP003 for each required stack that is not Ready.
func (s *stack) checkRequires(statusOf map[string]Status) {
	for _, req := range s.requires {
		st, ok := statusOf[req]
		switch {
		case !ok:
			s.add(SETUP003, s.dir+"/setup.toml", s.requiresLine,
				fmt.Sprintf("setup.toml requires %s, which is not a stack in compose/", req))
		case st != StatusReady:
			s.add(SETUP003, s.dir+"/setup.toml", s.requiresLine,
				fmt.Sprintf("setup.toml requires %s, which is %s", req, st))
		}
	}
}

// report returns the stack's result. Slices are non-nil so JSON shows [] rather than null.
func (s *stack) report() StackReport {
	sortFindings(s.findings)
	r := StackReport{
		Name:          s.app,
		Status:        s.status,
		Findings:      s.findings,
		Interpolation: s.interpolation,
		Container:     s.container,
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Interpolation.Vars == nil {
		r.Interpolation.Vars = []EnvVar{}
	}
	if r.Container.Vars == nil {
		r.Container.Vars = []EnvVar{}
	}
	return r
}
