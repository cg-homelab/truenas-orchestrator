package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// envKeyPattern is what a key written through update-stack-env must match.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// placeholderValues are the values ENV009 reports as placeholders. The env editor refuses to
// write them and flags them in a read.
// TODO: use an exported homelab helper once one exists, so the list lives in one place.
var placeholderValues = []string{"REPLACE_THIS", "CHANGEME", "changeme", "TODO"}

// msgPlaceholder is the problem reported for a placeholder value.
const msgPlaceholder = "is a placeholder value"

// envLocks serialises the read-modify-write cycles on each app's env file: update-stack-env
// and a setup run both read the file, compute new bytes and write them back, and two that
// overlap would lose one side's changes. One lock per app, created on first use; the map grows
// only by apps that exist, since callers look the stack up first.
type envLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func newEnvLocks() *envLocks { return &envLocks{locks: map[string]chan struct{}{}} }

// lock takes app's lock, waiting until it is free or ctx is done. It returns the unlock
// function, or ctx's error.
func (l *envLocks) lock(ctx context.Context, app string) (func(), error) {
	l.mu.Lock()
	ch, ok := l.locks[app]
	if !ok {
		ch = make(chan struct{}, 1)
		l.locks[app] = ch
	}
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for the env file lock of %s: %w", app, ctx.Err())
	}
}

// EnvEntry is one key of an app's env file.
type EnvEntry struct {
	Key       string  `json:"key"`
	Value     string  `json:"value" doc:"[REDACTED] unless reveal=true"`
	Line      int     `json:"line" doc:"1-based line in the env file"`
	Validator *string `json:"validator" doc:"The setup.toml [env_validators] type for the key; null when none"`
	Problem   *string `json:"problem" doc:"Why the current value is rejected, never quoting it; null when it passes"`
}

// EnvMissingKey is a key example.env defines and the env file lacks.
type EnvMissingKey struct {
	Key  string `json:"key"`
	Line int    `json:"line" doc:"1-based line in example.env"`
}

// EnvFileView is an app's env file as the env editor shows it.
type EnvFileView struct {
	Exists  bool            `json:"exists" doc:"Whether compose/<name>/.env exists"`
	Tracked bool            `json:"tracked" doc:"Git tracks compose/<name>/.env (GIT001): writes will be refused"`
	Ignored bool            `json:"ignored" doc:"Git ignores compose/<name>/.env; writes are refused unless it does"`
	Entries []EnvEntry      `json:"entries"`
	Missing []EnvMissingKey `json:"missing" doc:"Keys in example.env absent from the env file"`
}

// envValueProblem checks value for key against the placeholder rule and the key's
// [env_validators] entry. It returns "" when the value passes, or a description that never
// contains the value.
func envValueProblem(validators map[string]homelab.ValueValidatorSpec, key, value, app string) string {
	if slices.Contains(placeholderValues, value) {
		return msgPlaceholder
	}
	if spec, ok := validators[key]; ok {
		return homelab.ValidateValue(spec, value, app) // the domain never quotes the value
	}
	return ""
}

// newEnvFileView builds the view of env, redacting values unless reveal is set.
func newEnvFileView(
	app string, env homelab.DotEnv, exists bool, git envGit, example homelab.DotEnv,
	validators map[string]homelab.ValueValidatorSpec, reveal bool,
) EnvFileView {
	v := EnvFileView{
		Exists: exists, Tracked: git.tracked, Ignored: git.ignored, Entries: []EnvEntry{}, Missing: []EnvMissingKey{},
	}
	for _, e := range env.Entries {
		raw := e.Value.Reveal() // checked here; returned only when the caller asked to reveal
		value := redactedValue
		if reveal {
			value = raw
		}
		entry := EnvEntry{Key: e.Key, Value: value, Line: e.Line, Problem: optionalString(envValueProblem(validators, e.Key, raw, app))}
		if spec, ok := validators[e.Key]; ok {
			entry.Validator = optionalString(spec.Type)
		}
		v.Entries = append(v.Entries, entry)
	}
	for _, e := range example.Entries {
		if _, ok := env.Lookup(e.Key); !ok {
			v.Missing = append(v.Missing, EnvMissingKey{Key: e.Key, Line: e.Line})
		}
	}
	return v
}

// envGit is the git state of an app's env file.
type envGit struct {
	tracked bool
	ignored bool
}

// writeRefusal is the 409 detail for an env file git would expose, or "" when it may be written.
func (g envGit) writeRefusal(app string) string {
	switch {
	case g.tracked:
		return envPath(app) + " is tracked by git (GIT001); it is never written"
	case !g.ignored:
		return envPath(app) + " is not gitignored; add compose/*/.env to .gitignore first"
	}
	return ""
}

// readEnvGit reports whether git tracks and ignores compose/<app>/.env.
func readEnvGit(ctx context.Context, repo homelab.Repo, app string) (envGit, error) {
	tracked, err := repo.Tracked(ctx, envPath(app))
	if err != nil {
		return envGit{}, err
	}
	ignored, err := repo.Ignored(ctx, envPath(app))
	if err != nil {
		return envGit{}, err
	}
	return envGit{tracked: tracked, ignored: ignored}, nil
}

func envPath(app string) string     { return "compose/" + app + "/.env" }
func examplePath(app string) string { return "compose/" + app + "/example.env" }
func setupPath(app string) string   { return "compose/" + app + "/setup.toml" }

// findStack returns the stack named name from the cached report, or a 404.
func findStack(
	ctx context.Context, logger *slog.Logger, reports *reportCache, name string,
) (homelab.Report, homelab.StackReport, error) {
	rep, err := reports.get(ctx)
	if err != nil {
		return homelab.Report{}, homelab.StackReport{}, apiError(ctx, logger, err)
	}
	for _, s := range rep.Stacks {
		if s.Name == name {
			return rep, s, nil
		}
	}
	return homelab.Report{}, homelab.StackReport{}, huma.Error404NotFound("no such stack")
}

// setupToml is an app's parsed setup.toml.
type setupToml struct {
	file homelab.SetupFile
	// present is false when the app has no setup.toml.
	present bool
	// valid is false when the file is unreadable, is not TOML, or has SETUP002 issues; file then
	// holds whatever decoded, which is enough for [env_validators].
	valid bool
}

// loadSetup reads and parses compose/<app>/setup.toml. The report does not carry the parsed
// file, so it is read again here.
func loadSetup(ctx context.Context, repo homelab.Repo, app string) (setupToml, error) {
	b, err := repo.ReadFile(ctx, setupPath(app))
	switch {
	case errors.Is(err, homelab.ErrNotFound):
		return setupToml{}, nil
	case errors.Is(err, homelab.ErrUnreadable):
		return setupToml{present: true}, nil
	case err != nil:
		return setupToml{}, fmt.Errorf("read %s: %w", setupPath(app), err)
	}
	f, issues, err := homelab.ParseSetup(b)
	if err != nil { // a TOML syntax error is a finding (SETUP005), not a failure
		return setupToml{present: true}, nil
	}
	return setupToml{file: f, present: true, valid: len(issues) == 0}, nil
}

// readEnvState reads the app's env file and example.env. A missing file reads as nil content
// with exists false; a missing or unreadable example.env as empty.
func readEnvState(
	ctx context.Context, repo homelab.Repo, secrets homelab.SecretStore, app string,
) (content []byte, exists bool, example []byte, err error) {
	content, err = secrets.ReadEnv(ctx, app)
	switch {
	case err == nil:
		exists = true
	case errors.Is(err, homelab.ErrNotFound):
	case errors.Is(err, homelab.ErrUnreadable):
		return nil, false, nil, huma.Error422UnprocessableEntity(envPath(app) + " is not a readable regular file")
	default:
		return nil, false, nil, err
	}
	example, err = repo.ReadFile(ctx, examplePath(app))
	switch {
	case err == nil, errors.Is(err, homelab.ErrNotFound), errors.Is(err, homelab.ErrUnreadable):
		return content, exists, example, nil
	default:
		return nil, false, nil, err
	}
}

// unterminated is the 422 for env bytes ParseDotEnv rejects.
func unterminated(rel string, err error) error {
	line := 0
	var pe *homelab.ParseError
	if errors.As(err, &pe) {
		line = pe.Line
	}
	return huma.Error422UnprocessableEntity(rel + " has an unterminated quoted value on line " + strconv.Itoa(line))
}

// GetStackEnvInput is the get-stack-env request.
type GetStackEnvInput struct {
	Name   string `path:"name" maxLength:"255" doc:"App directory name under compose/"`
	Reveal bool   `query:"reveal" doc:"Return values instead of [REDACTED]; the reveal is logged and audited"`
}

// GetStackEnvOutput is the get-stack-env response.
type GetStackEnvOutput struct {
	Body EnvFileView
}

var opGetStackEnv = huma.Operation{
	OperationID: "get-stack-env",
	Method:      http.MethodGet,
	Path:        "/api/stacks/{name}/env",
	Summary:     "Get a stack's env file with per-key validation",
	Tags:        []string{"stacks"},
	Security:    authenticated(),
	Errors:      []int{http.StatusNotFound, http.StatusUnprocessableEntity},
}

func handleGetStackEnv(
	logger *slog.Logger, reports *reportCache, repo homelab.Repo, secrets homelab.SecretStore, audit *auditor,
) func(context.Context, *GetStackEnvInput) (*GetStackEnvOutput, error) {
	return func(ctx context.Context, in *GetStackEnvInput) (*GetStackEnvOutput, error) {
		if repo == nil || secrets == nil {
			return nil, apiError(ctx, logger, errNoRepo)
		}
		if _, _, err := findStack(ctx, logger, reports, in.Name); err != nil {
			return nil, err
		}
		setup, err := loadSetup(ctx, repo, in.Name)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		content, exists, example, err := readEnvState(ctx, repo, secrets, in.Name)
		if err != nil {
			return nil, envError(ctx, logger, err)
		}
		env, err := homelab.ParseDotEnv(content)
		if err != nil {
			return nil, unterminated(envPath(in.Name), err)
		}
		exampleEnv, _ := homelab.ParseDotEnv(example) // a broken example.env still lists the keys before the break
		git, err := readEnvGit(ctx, repo, in.Name)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		if in.Reveal {
			u, _ := userFrom(ctx)
			logger.InfoContext(ctx, "env values revealed", "stack", in.Name, "user", u.Username)
			audit.record(ctx, auditEnvReveal, envPath(in.Name), nil)
		}
		view := newEnvFileView(in.Name, env, exists, git, exampleEnv, setup.file.EnvValidators, in.Reveal)
		return &GetStackEnvOutput{Body: view}, nil
	}
}

// envError passes through the huma errors the env helpers build and maps the rest.
func envError(ctx context.Context, logger *slog.Logger, err error) error {
	var se huma.StatusError
	if errors.As(err, &se) {
		return err
	}
	return apiError(ctx, logger, err)
}

// EnvUpdate sets and removes keys of an env file. Both may be empty: against an absent env file
// that creates it from example.env.
type EnvUpdate struct {
	Set   map[string]secretField `json:"set,omitempty" doc:"Keys to assign, with their values"`
	Unset []string               `json:"unset,omitempty" doc:"Keys to remove"`
}

// UpdateStackEnvInput is the update-stack-env request.
type UpdateStackEnvInput struct {
	Name string `path:"name" maxLength:"255" doc:"App directory name under compose/"`
	Body EnvUpdate
}

// UpdateStackEnvOutput is the update-stack-env response.
type UpdateStackEnvOutput struct {
	Body EnvFileView
}

var opUpdateStackEnv = huma.Operation{
	OperationID: "update-stack-env",
	Method:      http.MethodPut,
	Path:        "/api/stacks/{name}/env",
	Summary:     "Set and remove keys of a stack's env file, creating it from example.env when absent",
	Description: "Every value is checked against the stack's setup.toml [env_validators] and the placeholder " +
		"rule first; a rejected value is a 422 whose errors name body.set.KEY and never echo the value. " +
		"An env file git tracks (GIT001) or does not ignore is never written (409). The response is the masked env file.",
	Tags:     []string{"stacks"},
	Security: authenticated(),
	Errors:   []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
}

func handleUpdateStackEnv(
	logger *slog.Logger, reports *reportCache, repo homelab.Repo, secrets homelab.SecretStore, audit *auditor,
	locks *envLocks,
) func(context.Context, *UpdateStackEnvInput) (*UpdateStackEnvOutput, error) {
	return func(ctx context.Context, in *UpdateStackEnvInput) (*UpdateStackEnvOutput, error) {
		if repo == nil || secrets == nil {
			return nil, apiError(ctx, logger, errNoRepo)
		}
		app := in.Name
		if _, _, err := findStack(ctx, logger, reports, app); err != nil {
			return nil, err
		}
		setup, err := loadSetup(ctx, repo, app)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		set := make(map[string]homelab.Secret, len(in.Body.Set))
		for k, v := range in.Body.Set {
			set[k] = v.Secret
		}
		if err := checkEnvUpdate(app, set, in.Body.Unset, setup.file.EnvValidators); err != nil {
			return nil, err
		}

		// Held from the read to the write, so no other update or setup run interleaves.
		unlock, err := locks.lock(ctx, app)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		defer unlock()

		current, exists, example, err := readEnvState(ctx, repo, secrets, app)
		if err != nil {
			return nil, envError(ctx, logger, err)
		}
		base, baseRel := current, envPath(app)
		if !exists {
			base, baseRel = example, examplePath(app)
		}
		updated, err := homelab.UpdateDotEnv(base, set, in.Body.Unset)
		var pe *homelab.ParseError
		switch {
		case errors.As(err, &pe):
			return nil, unterminated(baseRel, err)
		case err != nil:
			return nil, apiError(ctx, logger, err)
		}

		// Refused whether or not this update changes the bytes: the caller asked to write a file
		// that git would expose.
		git, err := readEnvGit(ctx, repo, app)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		if msg := git.writeRefusal(app); msg != "" {
			return nil, huma.Error409Conflict(msg)
		}
		if !exists || !bytes.Equal(updated, current) {
			if err := secrets.WriteEnv(ctx, app, updated); err != nil {
				// Git state can change between the check above and the write; the store re-checks.
				switch {
				case errors.Is(err, homelab.ErrTracked):
					return nil, huma.Error409Conflict(envGit{tracked: true}.writeRefusal(app))
				case errors.Is(err, homelab.ErrNotIgnored):
					return nil, huma.Error409Conflict(envGit{}.writeRefusal(app))
				}
				return nil, apiError(ctx, logger, err)
			}
			reports.invalidate()
			keysSet := slices.Sorted(maps.Keys(set))
			keysUnset := slices.Sorted(slices.Values(in.Body.Unset))
			audit.record(ctx, auditEnvWrite, envPath(app), map[string]string{
				"keys_set":   strings.Join(keysSet, ","),
				"keys_unset": strings.Join(keysUnset, ","),
				"created":    strconv.FormatBool(!exists),
			})
			logger.InfoContext(ctx, "env file written",
				"stack", app, "keys", slices.Concat(keysSet, keysUnset), "created", !exists)
		}

		env, err := homelab.ParseDotEnv(updated)
		if err != nil { // UpdateDotEnv only writes values that round-trip
			return nil, apiError(ctx, logger, fmt.Errorf("parse updated %s: %w", envPath(app), err))
		}
		exampleEnv, _ := homelab.ParseDotEnv(example)
		view := newEnvFileView(app, env, true, git, exampleEnv, setup.file.EnvValidators, false)
		return &UpdateStackEnvOutput{Body: view}, nil
	}
}

// msgControlChar is the problem for a value holding a control character.
const msgControlChar = "contains a control character (only tab and newline are allowed)"

// isForbiddenControl reports a C0 control character other than tab and newline.
func isForbiddenControl(r rune) bool {
	return r < 0x20 && r != '\t' && r != '\n'
}

// checkEnvUpdate validates the key names and the values of an update. The 422 lists one error
// per rejected key at body.set.KEY or body.unset[i]; messages never contain a value.
func checkEnvUpdate(app string, set map[string]homelab.Secret, unset []string, validators map[string]homelab.ValueValidatorSpec) error {
	var errs []error
	keys := slices.Sorted(maps.Keys(set))
	for _, k := range keys {
		loc := "body.set." + k
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, &huma.ErrorDetail{Location: loc, Message: "is not a variable name ([A-Za-z_][A-Za-z0-9_]*)"})
			continue
		}
		value := set[k].Reveal() // checked, never quoted in a message
		if strings.ContainsFunc(value, isForbiddenControl) {
			errs = append(errs, &huma.ErrorDetail{Location: loc, Message: msgControlChar})
			continue
		}
		if msg := envValueProblem(validators, k, value, app); msg != "" {
			errs = append(errs, &huma.ErrorDetail{Location: loc, Message: msg})
		}
	}
	for i, k := range unset {
		loc := "body.unset[" + strconv.Itoa(i) + "]"
		switch _, inSet := set[k]; {
		case !envKeyPattern.MatchString(k):
			errs = append(errs, &huma.ErrorDetail{Location: loc, Message: "is not a variable name ([A-Za-z_][A-Za-z0-9_]*)"})
		case inSet:
			errs = append(errs, &huma.ErrorDetail{Location: loc, Message: "is also in set; a key is either set or unset"})
		}
	}
	if len(errs) > 0 {
		return huma.Error422UnprocessableEntity("env update rejected", errs...)
	}
	return nil
}
