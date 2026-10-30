package homelab

import (
	"slices"
	"strings"
)

// Provenance values for EnvVar.Source that are not file paths.
const (
	SourceInjected    = "injected"
	SourceEnvironment = "compose.yaml:environment"
	SourceDefault     = "default"
)

// EnvScope is a resolved set of variables with provenance. See docs/architecture/env-scopes.md.
type EnvScope struct {
	// Vars is sorted by key.
	Vars []EnvVar `json:"vars"`
}

// EnvVar is one resolved variable.
type EnvVar struct {
	Key   string `json:"key"`
	Value Secret `json:"value"`
	// Source is SourceInjected, SourceEnvironment, SourceDefault, or the repo-relative path of
	// the env file that supplied the value (compose/.global.env, compose/<app>/.env, ...).
	Source string `json:"source"`
	// Line is the 1-based line in Source (in compose.yaml for environment: and defaults); 0 for
	// injected values.
	Line int `json:"line"`
}

// Lookup returns the variable named key.
func (s EnvScope) Lookup(key string) (EnvVar, bool) {
	i, ok := slices.BinarySearchFunc(s.Vars, key, func(v EnvVar, k string) int { return strings.Compare(v.Key, k) })
	if !ok {
		return EnvVar{}, false
	}
	return s.Vars[i], true
}

// lookupFunc adapts s to Interpolate.
func (s EnvScope) lookupFunc() func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := s.Lookup(k)
		return v.Value.Reveal(), ok
	}
}

// scopeBuilder accumulates variables where a later set wins.
type scopeBuilder struct {
	vars map[string]EnvVar
}

func newScopeBuilder() *scopeBuilder { return &scopeBuilder{vars: map[string]EnvVar{}} }

func (b *scopeBuilder) set(v EnvVar) { b.vars[v.Key] = v }

func (b *scopeBuilder) setIfAbsent(v EnvVar) {
	if _, ok := b.vars[v.Key]; !ok {
		b.vars[v.Key] = v
	}
}

func (b *scopeBuilder) addDotEnv(d DotEnv, source string) {
	for _, e := range d.Entries {
		b.set(EnvVar{Key: e.Key, Value: e.Value, Source: source, Line: e.Line})
	}
}

func (b *scopeBuilder) scope() EnvScope {
	vars := make([]EnvVar, 0, len(b.vars))
	for _, v := range b.vars {
		vars = append(vars, v)
	}
	slices.SortFunc(vars, func(a, b EnvVar) int { return strings.Compare(a.Key, b.Key) })
	return EnvScope{Vars: vars}
}

// DeployEnv resolves the interpolation scope: injected HOMELAB_STORAGE (when non-empty), then
// compose/.global.env, then compose/<app>/.env, later wins — except that an injected
// HOMELAB_STORAGE always wins over the files, because the orchestrator's value is the source of
// truth (env-scopes.md, the bootstrap rule).
func DeployEnv(homelabStorage string, global, app DotEnv, globalPath, appPath string) EnvScope {
	b := newScopeBuilder()
	b.addDotEnv(global, globalPath)
	b.addDotEnv(app, appPath)
	if homelabStorage != "" {
		b.set(EnvVar{Key: "HOMELAB_STORAGE", Value: NewSecret(homelabStorage), Source: SourceInjected})
	}
	return b.scope()
}
