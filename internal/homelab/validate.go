package homelab

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Options configures ValidateRepo.
type Options struct {
	// HomelabStorage is the value injected for HOMELAB_STORAGE. It always wins over .global.env
	// for that key: the orchestrator's value is the source of truth (env-scopes.md). For the CLI
	// it is the repo root. When empty, nothing is injected and the files' value, if any, is used.
	HomelabStorage string
	// HostPathExists reports whether a host path (from volumes: or secrets: file:) exists. nil
	// disables PATH001. An error aborts validation.
	HostPathExists func(path string) (bool, error)
}

const (
	composeDir    = "compose"
	globalEnvPath = "compose/.global.env"
)

// ValidateRepo runs every validator check over repo, parsing compose files with parse. Both
// `orchestrator validate` and internal/server call it, so the CLI and the UI cannot disagree.
//
// The error is non-nil only when the repo cannot be read or ctx is done; every defect in the
// repo's content is a Finding.
func ValidateRepo(ctx context.Context, repo Repo, parse ComposeParser, opts Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("validate repo: %w", err)
	}
	status, err := repo.Status(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("validate repo: status: %w", err)
	}
	apps, err := repo.Apps(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("validate repo: list apps: %w", err)
	}
	global, err := readEnv(ctx, repo, globalEnvPath)
	if err != nil {
		return Report{}, fmt.Errorf("validate repo: %w", err)
	}

	rep := Report{Repo: status, Stacks: make([]StackReport, 0, len(apps))}
	rep.Findings, err = repoFindings(ctx, repo, status, apps)
	if err != nil {
		return Report{}, fmt.Errorf("validate repo: %w", err)
	}

	v := &validator{repo: repo, parse: parse, opts: opts, global: global}
	stacks := make([]*stack, 0, len(apps))
	for _, app := range apps {
		if err := ctx.Err(); err != nil {
			return Report{}, fmt.Errorf("validate repo: %w", err)
		}
		st, err := v.validateStack(ctx, app)
		if err != nil {
			return Report{}, fmt.Errorf("validate repo: stack %s: %w", app, err)
		}
		stacks = append(stacks, st)
	}

	// Second pass: SETUP003 needs every stack's status from the first pass.
	statusOf := make(map[string]Status, len(stacks))
	for _, st := range stacks {
		statusOf[st.app] = st.status
	}
	for _, st := range stacks {
		st.checkRequires(statusOf)
	}

	for _, st := range stacks {
		rep.Stacks = append(rep.Stacks, st.report())
	}
	slices.SortFunc(rep.Stacks, func(a, b StackReport) int { return strings.Compare(a.Name, b.Name) })
	if rep.Findings == nil {
		rep.Findings = []Finding{}
	}
	sortFindings(rep.Findings)
	return rep, nil
}

// repoFindings returns the repo-level GIT00x findings.
func repoFindings(ctx context.Context, repo Repo, status RepoStatus, apps []string) ([]Finding, error) {
	var out []Finding
	candidates := []string{globalEnvPath}
	for _, app := range apps {
		candidates = append(candidates, composeDir+"/"+app+"/.env")
	}
	for _, rel := range candidates {
		tracked, err := repo.Tracked(ctx, rel)
		if err != nil {
			return nil, fmt.Errorf("tracked %s: %w", rel, err)
		}
		if tracked {
			out = append(out, newFinding(GIT001, rel, 0,
				fmt.Sprintf("%s is tracked by git; real env files must be gitignored", rel)))
		}
	}
	if status.Dirty {
		out = append(out, newFinding(GIT002, "", 0, "the repo has uncommitted changes to tracked files"))
	}
	if status.Behind > 0 {
		out = append(out, newFinding(GIT003, "", 0,
			fmt.Sprintf("branch %s is %d commit(s) behind %s", status.Branch, status.Behind, status.Upstream)))
	}
	return out, nil
}

// readOptional reads rel, reporting false for an absent file. ErrUnreadable is returned
// wrapped, for the caller to report.
func readOptional(ctx context.Context, repo Repo, rel string) (content []byte, found bool, err error) {
	b, err := repo.ReadFile(ctx, rel)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", rel, err)
	}
	return b, true, nil
}

// envRead is the outcome of reading one env file.
type envRead struct {
	env   DotEnv
	found bool
	// unreadable is set when the repo reports ErrUnreadable (a symlink leaving the repo, or not
	// a regular file).
	unreadable bool
	// parseErr is set for an unterminated quoted value; env holds the entries before it.
	parseErr *ParseError
}

// readEnv reads and parses an env file. Only an unexpected repo error is returned.
func readEnv(ctx context.Context, repo Repo, rel string) (envRead, error) {
	b, found, err := readOptional(ctx, repo, rel)
	if errors.Is(err, ErrUnreadable) {
		return envRead{unreadable: true}, nil
	}
	if err != nil || !found {
		return envRead{}, err
	}
	d, err := ParseDotEnv(b)
	r := envRead{env: d, found: true}
	if err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) {
			pe = &ParseError{Message: "cannot be parsed"}
		}
		r.parseErr = pe
	}
	return r, nil
}

func sortFindings(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		return cmp.Or(
			strings.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			strings.Compare(string(a.Code), string(b.Code)),
			strings.Compare(a.Message, b.Message),
		)
	})
}
