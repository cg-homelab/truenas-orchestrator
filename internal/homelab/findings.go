package homelab

// Code identifies a validator check. Values are stable: never renumber, never reuse.
// Each registry row in docs/validator-checks.md becomes one constant here, spelled exactly as
// in the registry, when its check is implemented (M1).
type Code string

// Severity is how serious a finding is.
type Severity string

// Severities, in increasing order of seriousness.
const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Status is the overall state of a stack, derived from its findings.
type Status string

// Stack statuses.
const (
	StatusReady      Status = "Ready"
	StatusNeedsSetup Status = "NeedsSetup"
	StatusBroken     Status = "Broken"
)

// Finding is one result of a validator check, pointing at a file and line.
type Finding struct {
	Code     Code
	Severity Severity
	// File is the path relative to the repo root.
	File string
	// Line is 1-based; 0 means the finding applies to the whole file.
	Line    int
	Message string
	// Fix is a short, actionable suggestion.
	Fix string
}

// Report is the outcome of validating a whole homelab repo.
type Report struct {
	Findings []Finding
}
