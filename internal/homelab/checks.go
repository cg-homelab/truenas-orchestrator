package homelab

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// storageRootKeys are the deploy-env variables whose values are the permitted storage roots.
func storageRootKeys() []string {
	return []string{"HOMELAB_STORAGE", "APPCONFIG_STORAGE", "DATABASE_STORAGE", "MEDIA_STORAGE"}
}

func isContractNetwork(name string) bool {
	switch name {
	case "home", "proxy", "isolated", "databases":
		return true
	}
	return false
}

func isPlaceholder(v string) bool {
	switch v {
	case "REPLACE_THIS", "CHANGEME", "changeme", "TODO":
		return true
	}
	return false
}

// checkInterpolationRefs emits ENV001 and ENV003 for interpolation references without a default
// that the deploy env does not supply.
func (s *stack) checkInterpolationRefs() {
	type key struct {
		code Code
		name string
		line int
	}
	seen := map[key]bool{}
	for _, r := range s.cf.Refs {
		if r.HasDefault || r.Optional || s.fallbackResolved(r) {
			continue
		}
		code := ENV001
		msg := fmt.Sprintf("%s is referenced at %s but is defined in neither %s nor %s",
			r.Name, r.Path, globalEnvPath, s.appEnvPath())
		if v, ok := s.deploy.Lookup(r.Name); ok {
			if r.Op != ":?" || v.Value.Reveal() != "" {
				continue
			}
			msg = fmt.Sprintf("%s is set but empty and ${%s:?} at %s requires a value", r.Name, r.Name, r.Path)
		} else if s.coveredByMissingEnv(r.Name) {
			continue // ENV004 on the missing .env already explains it
		} else if src, ok := s.scopeHas(r.Service, r.Name); ok {
			code = ENV003
			msg = fmt.Sprintf("%s is referenced at %s, an interpolation position, but is only available in container scope (via %s); Compose never interpolates from env_file: or environment:",
				r.Name, r.Path, src)
		}
		k := key{code, r.Name, r.Line}
		if seen[k] {
			continue
		}
		seen[k] = true
		if code == ENV001 && !slices.Contains(s.unresolved, r.Name) {
			s.unresolved = append(s.unresolved, r.Name)
		}
		s.add(code, s.cf.Path, r.Line, msg)
	}
}

// coveredByMissingEnv reports whether name is a key of example.env while the app's own .env is
// missing: setup will create it, and its ENV004 explains the absence.
func (s *stack) coveredByMissingEnv(name string) bool {
	if !s.ownEnvMissing {
		return false
	}
	_, ok := s.example.env.Lookup(name)
	return ok
}

// checkContainerRefs emits ENV002 for $$VAR reads that the service's container scope does not
// supply. A scope with an unreadable env_file: is incomplete and is not checked.
func (s *stack) checkContainerRefs() {
	for _, r := range s.cf.ContainerRefs {
		sc := s.services[r.Service]
		if sc == nil || sc.unknown {
			continue
		}
		if _, ok := sc.b.vars[r.Name]; ok {
			continue
		}
		if s.coveredByMissingEnv(r.Name) {
			continue // ENV004 on the missing .env already explains it
		}
		s.add(ENV002, s.cf.Path, r.Line, fmt.Sprintf(
			"%s is read by the container at %s ($$%s) but no env_file: or environment: entry of service %s defines it",
			r.Name, r.Path, r.Name, r.Service))
	}
}

// checkEnvFiles compares example.env with .env (ENV005, ENV006), finds unreferenced keys
// (ENV008), placeholders (ENV009) and a mismatched SERVICE_NAME (ENV010).
func (s *stack) checkEnvFiles() {
	appEnv, example := s.appEnv.env, s.example.env
	if s.appEnv.found && s.example.found {
		for _, e := range example.Entries {
			if _, ok := appEnv.Lookup(e.Key); !ok {
				s.add(ENV005, s.examplePath(), e.Line,
					fmt.Sprintf("%s is defined in example.env but missing from %s", e.Key, s.appEnvPath()))
			}
		}
		for _, e := range appEnv.Entries {
			if _, ok := example.Lookup(e.Key); !ok {
				s.add(ENV006, s.appEnvPath(), e.Line,
					fmt.Sprintf("%s is defined in .env but absent from %s", e.Key, s.examplePath()))
			}
		}
	}

	// ENV008 targets the registry's case — PORT defined while ${HTTP_PORT} is unresolved — so an
	// unused key is reported only when the stack also has an unresolved reference. A key the
	// image reads straight from env_file: (POSTGRES_PASSWORD) is not a defect on its own.
	if len(s.unresolved) > 0 {
		referenced := s.referencedNames()
		reported := map[string]bool{}
		report008 := func(d DotEnv, file string) {
			for _, e := range d.Entries {
				if referenced[e.Key] || reported[e.Key] {
					continue
				}
				reported[e.Key] = true
				s.add(ENV008, file, e.Line, fmt.Sprintf(
					"%s is defined but never referenced in compose.yaml; unresolved references in this stack: %s",
					e.Key, strings.Join(s.unresolved, ", ")))
			}
		}
		report008(example, s.examplePath())
		report008(appEnv, s.appEnvPath())
	}

	for _, e := range appEnv.Entries {
		if isPlaceholder(e.Value.Reveal()) {
			s.add(ENV009, s.appEnvPath(), e.Line, fmt.Sprintf("%s still holds a placeholder value", e.Key))
		}
	}

	if sn, ok := s.deploy.Lookup("SERVICE_NAME"); ok && sn.Source != SourceInjected && sn.Value.Reveal() != s.app {
		s.add(ENV010, sn.Source, sn.Line,
			fmt.Sprintf("SERVICE_NAME does not equal the app directory name %q", s.app))
	}
}

// referencedNames is every name compose.yaml reads: interpolation references (with or without
// a default), container reads, and bare environment: keys passed through from the deploy env.
func (s *stack) referencedNames() map[string]bool {
	out := map[string]bool{}
	for _, r := range s.cf.Refs {
		out[r.Name] = true
	}
	for _, r := range s.cf.ContainerRefs {
		out[r.Name] = true
	}
	for _, svc := range s.cf.Services {
		for _, e := range svc.Environment {
			if !e.HasValue {
				out[e.Key] = true
			}
		}
	}
	return out
}

// checkValidators applies setup.toml [env_validators] to deploy-env values (ENV007).
func (s *stack) checkValidators() {
	if s.setup == nil {
		return
	}
	for _, key := range sortedKeys(s.setup.EnvValidators) {
		spec := s.setup.EnvValidators[key]
		if !knownValidator(spec.Type) {
			continue // SETUP002 already reported it
		}
		v, ok := s.deploy.Lookup(key)
		if !ok {
			continue
		}
		if msg := ValidateValue(spec, v.Value.Reveal(), s.app); msg != "" {
			file, line := v.Source, v.Line
			if v.Source == SourceInjected {
				file, line = s.dir+"/setup.toml", 0
			}
			s.add(ENV007, file, line, fmt.Sprintf("%s %s (setup.toml type %q)", key, msg, spec.Type))
		}
	}
}

// hostPath is one host path from volumes: or secrets: file:.
type hostPath struct {
	raw  string
	line int
}

// checkPaths emits PATH002 for literal paths under a storage root, PATH003 for interpolated or
// relative paths that resolve outside every root, and PATH001 for paths that do not exist when
// host checks are enabled.
func (v *validator) checkPaths(s *stack) error {
	var roots []string
	for _, k := range storageRootKeys() {
		if ev, ok := s.deploy.Lookup(k); ok && path.IsAbs(ev.Value.Reveal()) {
			roots = append(roots, path.Clean(ev.Value.Reveal()))
		}
	}
	lookup := s.deploy.lookupFunc()
	declared := map[string]bool{}
	if s.setup != nil {
		for _, p := range s.setup.DeclaredPaths() {
			if r, missing := Interpolate(p, lookup); len(missing) == 0 && path.IsAbs(r) {
				declared[path.Clean(r)] = true
			}
		}
	}
	appDir := ""
	if hs, ok := s.deploy.Lookup("HOMELAB_STORAGE"); ok && path.IsAbs(hs.Value.Reveal()) {
		appDir = path.Join(hs.Value.Reveal(), s.dir)
	}

	var paths []hostPath
	for _, svc := range s.cf.Services {
		for _, vol := range svc.Volumes {
			paths = append(paths, hostPath{vol.Value, vol.Line})
		}
	}
	for _, sf := range s.cf.SecretFiles {
		paths = append(paths, hostPath{sf.Value, sf.Line})
	}

	for _, hp := range paths {
		resolved := ""
		switch {
		case strings.HasPrefix(hp.raw, "/"):
			// PATH002 tests the raw text: /mnt/fast/../../etc is still a hardcoded /mnt path.
			if strings.HasPrefix(hp.raw, "/mnt/") || rawUnderAny(hp.raw, roots) {
				s.add(PATH002, s.cf.Path, hp.line, fmt.Sprintf(
					"host path %q is a literal path under a storage root; use the storage variable", hp.raw))
			}
			if refs := ScanTemplate(hp.raw).Refs; len(refs) > 0 {
				r, missing := Interpolate(hp.raw, lookup)
				if len(missing) > 0 {
					continue
				}
				resolved = path.Clean(r)
			} else {
				resolved = path.Clean(hp.raw)
			}
			// A literal path outside the roots is allowed (/var/run/docker.sock); one that only
			// gets there through ".." is an escape.
			if hasDotDot(hp.raw) && !underAny(resolved, roots) {
				s.add(PATH003, s.cf.Path, hp.line, fmt.Sprintf(
					"host path %q escapes every storage root through ..", hp.raw))
				continue
			}
		case strings.HasPrefix(hp.raw, "$"), strings.HasPrefix(hp.raw, "."):
			r, missing := Interpolate(hp.raw, lookup)
			if len(missing) > 0 || r == "" {
				continue // ENV001/ENV003 reports the unresolved variable
			}
			switch {
			case path.IsAbs(r):
				resolved = path.Clean(r)
			case strings.HasPrefix(r, "."):
				if appDir == "" {
					continue
				}
				resolved = path.Join(appDir, r)
			default:
				continue // a named volume after interpolation
			}
			if !underAny(resolved, roots) {
				s.add(PATH003, s.cf.Path, hp.line, fmt.Sprintf(
					"host path %q resolves outside every storage root (%s)", hp.raw, strings.Join(storageRootKeys(), ", ")))
				continue
			}
		default:
			continue // a named volume, or ~
		}

		if v.opts.HostPathExists == nil {
			continue
		}
		exists, err := v.opts.HostPathExists(resolved)
		if err != nil {
			return &hostPathError{raw: hp.raw, file: s.cf.Path, line: hp.line, err: err}
		}
		if exists {
			continue
		}
		if declared[resolved] {
			s.declaredMissing = true
			f := newFinding(PATH001, s.cf.Path, hp.line, fmt.Sprintf(
				"host path %q does not exist yet; setup.toml declares it, so app setup will create it", hp.raw))
			f.Severity = SeverityInfo
			s.findings = append(s.findings, f)
			continue
		}
		s.add(PATH001, s.cf.Path, hp.line, fmt.Sprintf("host path %q does not exist and nothing declares it", hp.raw))
	}
	return nil
}

// hostPathError reports a failed host-path check. Its message names the raw compose path and
// line, never the resolved path (which embeds env values); the cause stays reachable through
// errors.Is and errors.As.
type hostPathError struct {
	raw, file string
	line      int
	err       error
}

func (e *hostPathError) Error() string {
	return fmt.Sprintf("check host path %q at %s line %d failed", e.raw, e.file, e.line)
}

func (e *hostPathError) Unwrap() error { return e.err }

// rawUnderAny reports whether the uncleaned path p starts with one of the roots.
func rawUnderAny(p string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool { return p == r || strings.HasPrefix(p, r+"/") })
}

func hasDotDot(p string) bool {
	return slices.Contains(strings.Split(p, "/"), "..")
}

func underAny(p string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool { _, ok := under(p, r); return ok })
}

// checkNetworks emits NET001, NET002 and NET003. Services refer to a declaration key; the
// contract applies to the network's name, which a declaration's name: may set.
func (s *stack) checkNetworks() {
	declared := map[string]NetworkDecl{}
	for _, d := range s.cf.Networks {
		declared[d.Key] = d
	}
	used := map[string]bool{}
	for _, svc := range s.cf.Services {
		for _, n := range svc.Networks {
			used[n.Value] = true
			d, ok := declared[n.Value]
			name := n.Value
			if ok {
				name = d.Name
			}
			if !isContractNetwork(name) {
				s.add(NET001, s.cf.Path, n.Line, fmt.Sprintf(
					"service %s uses network %s, which is not one of home, proxy, isolated, databases", svc.Name, name))
			}
			if !ok {
				s.add(NET002, s.cf.Path, n.Line, fmt.Sprintf(
					"service %s uses network %s, which the top-level networks: block does not declare", svc.Name, n.Value))
			}
		}
	}
	for _, d := range s.cf.Networks {
		if !isContractNetwork(d.Name) && !used[d.Key] {
			s.add(NET001, s.cf.Path, d.Line, fmt.Sprintf(
				"network %s is not one of home, proxy, isolated, databases", d.Name))
		}
		if !d.External {
			s.add(NET003, s.cf.Path, d.Line, fmt.Sprintf("network %s is declared without external: true", d.Key))
		}
	}
}

// checkLimits emits COMPOSE003 for each service without deploy.resources.limits.
func (s *stack) checkLimits() {
	for _, svc := range s.cf.Services {
		if !svc.HasLimits {
			s.add(COMPOSE003, s.cf.Path, svc.Line, fmt.Sprintf("service %s has no deploy.resources.limits", svc.Name))
		}
	}
}
