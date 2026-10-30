package server

import (
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// redactedValue replaces every env value in a response unless the caller asks to reveal it.
const redactedValue = "[REDACTED]"

// Finding is one validator finding in a response.
type Finding struct {
	Code     string `json:"code" doc:"Stable finding code from docs/validator-checks.md, e.g. ENV003"`
	Severity string `json:"severity" enum:"error,warning,info"`
	File     string `json:"file" doc:"Repo-relative, slash-separated path; empty for repo-wide findings"`
	Line     int    `json:"line" doc:"1-based line; 0 means no line"`
	Message  string `json:"message"`
	Fix      string `json:"fix" doc:"Suggested fix"`
}

// EnvVar is one resolved variable with its provenance.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value" doc:"[REDACTED] unless reveal=true"`
	// Source is where the value came from: injected, compose.yaml:environment, default, or the
	// repo-relative path of an env file.
	Source string `json:"source" doc:"injected, compose.yaml:environment, default, or the env file path"`
	Line   int    `json:"line" doc:"1-based line in source; 0 means no line"`
}

// FindingCounts counts a stack's findings by severity.
type FindingCounts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Info    int `json:"info"`
}

// StackSummary is one row of the stack list.
type StackSummary struct {
	Name   string        `json:"name"`
	Status string        `json:"status" enum:"Ready,NeedsSetup,Broken"`
	Counts FindingCounts `json:"counts"`
}

// StackList is the list-stacks response body.
type StackList struct {
	Stacks []StackSummary `json:"stacks"`
}

// StackDetail is one stack with its findings and both resolved env scopes.
type StackDetail struct {
	Name     string    `json:"name"`
	Status   string    `json:"status" enum:"Ready,NeedsSetup,Broken"`
	Findings []Finding `json:"findings"`
	// Interpolation is the deploy env that fills ${VAR} in compose.yaml.
	Interpolation []EnvVar `json:"interpolation" doc:"The deploy env that fills ${VAR} in compose.yaml"`
	// Container is what env_file: and environment: inject into the containers.
	Container []EnvVar `json:"container" doc:"What env_file: and environment: inject into the containers"`
}

// RepoView is the homelab repo's git state and its repo-level findings.
type RepoView struct {
	Branch    string     `json:"branch"`
	Upstream  string     `json:"upstream" doc:"Tracking branch; empty when none is configured"`
	Ahead     int        `json:"ahead"`
	Behind    int        `json:"behind"`
	Dirty     bool       `json:"dirty"`
	LastFetch *time.Time `json:"last_fetch" doc:"When the repo last fetched; null when unknown"`
	Findings  []Finding  `json:"findings"`
}

func newFindings(fs []homelab.Finding) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, Finding{
			Code: string(f.Code), Severity: string(f.Severity), File: f.File, Line: f.Line,
			Message: f.Message, Fix: f.Fix,
		})
	}
	return out
}

func countFindings(fs []homelab.Finding) FindingCounts {
	var c FindingCounts
	for _, f := range fs {
		switch f.Severity {
		case homelab.SeverityError:
			c.Error++
		case homelab.SeverityWarning:
			c.Warning++
		case homelab.SeverityInfo:
			c.Info++
		}
	}
	return c
}

// newEnvVars converts a scope, redacting every value unless reveal is set.
func newEnvVars(s homelab.EnvScope, reveal bool) []EnvVar {
	out := make([]EnvVar, 0, len(s.Vars))
	for _, v := range s.Vars {
		value := redactedValue
		if reveal {
			value = v.Value.Reveal() // the caller asked for values explicitly; the handler logs that
		}
		out = append(out, EnvVar{Key: v.Key, Value: value, Source: v.Source, Line: v.Line})
	}
	return out
}

func newStackSummary(s homelab.StackReport) StackSummary {
	return StackSummary{Name: s.Name, Status: string(s.Status), Counts: countFindings(s.Findings)}
}

func newStackDetail(s homelab.StackReport, reveal bool) StackDetail {
	return StackDetail{
		Name:          s.Name,
		Status:        string(s.Status),
		Findings:      newFindings(s.Findings),
		Interpolation: newEnvVars(s.Interpolation, reveal),
		Container:     newEnvVars(s.Container, reveal),
	}
}

func newRepoView(r homelab.Report) RepoView {
	v := RepoView{
		Branch:   r.Repo.Branch,
		Upstream: r.Repo.Upstream,
		Ahead:    r.Repo.Ahead,
		Behind:   r.Repo.Behind,
		Dirty:    r.Repo.Dirty,
		Findings: newFindings(r.Findings),
	}
	if !r.Repo.LastFetch.IsZero() {
		t := r.Repo.LastFetch.UTC()
		v.LastFetch = &t
	}
	return v
}

// optionalTime returns nil for the zero time, so it encodes as null.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// optionalString returns nil for the empty string, so it encodes as null.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
