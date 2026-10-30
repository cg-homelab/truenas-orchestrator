package homelab

// Code identifies a validator check. Values are stable: never renumber, never reuse.
// Each registry row in docs/validator-checks.md is one constant here, spelled exactly as in the
// registry so that grep finds the doc, the check and the tests together.
type Code string

// Finding codes, one per row of docs/validator-checks.md.
const (
	COMPOSE001 Code = "COMPOSE001"
	COMPOSE002 Code = "COMPOSE002"
	COMPOSE003 Code = "COMPOSE003"
	COMPOSE004 Code = "COMPOSE004"
	COMPOSE005 Code = "COMPOSE005"
	COMPOSE006 Code = "COMPOSE006"

	ENV001 Code = "ENV001"
	ENV002 Code = "ENV002"
	ENV003 Code = "ENV003"
	ENV004 Code = "ENV004"
	ENV005 Code = "ENV005"
	ENV006 Code = "ENV006"
	ENV007 Code = "ENV007"
	ENV008 Code = "ENV008"
	ENV009 Code = "ENV009"
	ENV010 Code = "ENV010"

	PATH001 Code = "PATH001"
	PATH002 Code = "PATH002"
	PATH003 Code = "PATH003"

	NET001 Code = "NET001"
	NET002 Code = "NET002"
	NET003 Code = "NET003"

	SETUP001 Code = "SETUP001"
	SETUP002 Code = "SETUP002"
	SETUP003 Code = "SETUP003"
	// SETUP004 needs the record of setup runs, which arrives with M3. It is never emitted in M1.
	SETUP004 Code = "SETUP004"
	SETUP005 Code = "SETUP005"
	SETUP006 Code = "SETUP006"

	GIT001 Code = "GIT001"
	GIT002 Code = "GIT002"
	GIT003 Code = "GIT003"
)

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
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	// File is the path relative to the repo root, slash-separated.
	File string `json:"file"`
	// Line is 1-based; 0 means the finding applies to the whole file.
	Line int `json:"line"`
	// Message names keys, never values: a .env value must not reach a finding.
	Message string `json:"message"`
	// Fix is a short, actionable suggestion.
	Fix string `json:"fix"`
}

// codeInfo returns the registry severity and suggested fix for c.
func codeInfo(c Code) (sev Severity, fix string) {
	switch c {
	case COMPOSE001:
		return SeverityError, "Make the file match the Compose spec, e.g. change `networks: databases` to a list"
	case COMPOSE002:
		return SeverityError, "Fix the YAML syntax"
	case COMPOSE003:
		return SeverityWarning, "Add cpu and memory limits under deploy.resources.limits"
	case COMPOSE004:
		return SeverityWarning, "Rename to compose.yaml"
	case COMPOSE005:
		return SeverityError, "Add one, or remove the directory"
	case COMPOSE006:
		return SeverityError, "Rename the directory"
	case ENV001:
		return SeverityError, "Define the variable, or correct the reference to the defined name"
	case ENV002:
		return SeverityError, "Add the key to .global.env or <app>/.env"
	case ENV003:
		return SeverityError, "Ensure the orchestrator supplies it in the deploy env; do not rely on env_file: or environment: for interpolation"
	case ENV004:
		return SeverityError, "Run app setup, or create the file"
	case ENV005:
		return SeverityWarning, "Add the key; the template defines the expected contract"
	case ENV006:
		return SeverityInfo, "Add it to the template so other deployments know it exists"
	case ENV007:
		return SeverityError, "Set a value that satisfies the validator declared in setup.toml"
	case ENV008:
		return SeverityWarning, "Remove it, or correct the compose reference"
	case ENV009:
		return SeverityError, "Set a real value"
	case ENV010:
		return SeverityWarning, "Make them match; Traefik labels and container_name depend on it"
	case PATH001:
		return SeverityWarning, "Run app setup, or declare it in setup.toml"
	case PATH002:
		return SeverityWarning, "Replace the literal prefix with the storage variable, e.g. ${APPCONFIG_STORAGE}/..."
	case PATH003:
		return SeverityError, "Correct the path"
	case NET001:
		return SeverityError, "Use one of home, proxy, isolated, databases, or add it to the contract deliberately"
	case NET002:
		return SeverityError, "Declare it in the top-level networks: block as external: true"
	case NET003:
		return SeverityWarning, "Mark it external: true; these networks are created during bootstrap"
	case SETUP001:
		return SeverityWarning, "Port it to setup.toml (docs/architecture/setup-toml.md)"
	case SETUP002:
		return SeverityError, "Remove it, or add the primitive to the registry deliberately"
	case SETUP003:
		return SeverityWarning, "Set up the required stack first"
	case SETUP004:
		return SeverityWarning, "Run setup"
	case SETUP005:
		return SeverityError, "Fix the TOML syntax"
	case SETUP006:
		return SeverityError, "Make them match"
	case GIT001:
		return SeverityError, "Add the gitignore rule and `git rm --cached` the file. Treat any committed secrets as compromised and rotate them"
	case GIT002:
		return SeverityWarning, "Commit or revert"
	case GIT003:
		return SeverityInfo, "Pull"
	}
	return SeverityError, ""
}

// newFinding builds a finding with the registry severity and fix for code.
func newFinding(code Code, file string, line int, message string) Finding {
	sev, fix := codeInfo(code)
	return Finding{Code: code, Severity: sev, File: file, Line: line, Message: message, Fix: fix}
}

// Report is the outcome of validating a whole homelab repo.
type Report struct {
	Repo RepoStatus `json:"repo"`
	// Stacks is sorted by name.
	Stacks []StackReport `json:"stacks"`
	// Findings holds repo-level findings only (GIT00x); stack findings live in StackReport.
	Findings []Finding `json:"findings"`
}

// StackReport is the outcome of validating one app directory under compose/.
type StackReport struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	// Findings is sorted by file, line and code.
	Findings []Finding `json:"findings"`
	// Interpolation is the resolved deploy env that fills ${VAR} in compose.yaml, with provenance.
	Interpolation EnvScope `json:"interpolation"`
	// Container is what env_file: and environment: inject into the containers, with provenance.
	Container EnvScope `json:"container"`
}
