package homelab

import "fmt"

// ComposeFile is the parsed model of one compose file: what the checks need, with the 1-based
// source line of everything a finding may point at. compose.Parser produces it.
type ComposeFile struct {
	// Path is the file's path relative to the repo root.
	Path string
	// SchemaErrors are Compose-spec violations and invalid interpolation templates (COMPOSE001);
	// empty when the file is valid.
	SchemaErrors []SchemaError
	// Services is in file order.
	Services []Service
	// Networks is the top-level networks: block, in file order.
	Networks []NetworkDecl
	// SecretFiles are the host paths of top-level secrets: <name>: file:, in file order.
	SecretFiles []Scalar
	// Refs is every interpolation-position reference (${VAR}, $VAR) in the file, wherever it
	// sits, including environment: values. Docker Compose interpolates all of them from the
	// deploy env.
	Refs []Ref
	// ContainerRefs are the escaped $$VAR / $${VAR} reads that a shell in the container
	// evaluates against container scope: command:, entrypoint: and healthcheck.test in string
	// form, in list form after sh/bash -c, or after CMD-SHELL. Names the same string assigns
	// (X=...) or loops over (for X in) are excluded. environment: values are never evaluated.
	ContainerRefs []Ref
}

// SchemaError is one Compose-spec violation.
type SchemaError struct {
	Message string
	Line    int
}

// Scalar is a raw string value from the compose file and its line.
type Scalar struct {
	Value string
	Line  int
}

// Service is one entry under services:.
type Service struct {
	Name          string
	Line          int
	ContainerName Scalar
	Image         Scalar
	EnvFiles      []EnvFileRef
	Environment   []EnvEntry
	Networks      []Scalar
	// Volumes are the raw host sides of bind mounts (short syntax with a host part, or long
	// syntax with type: bind). Named volumes are excluded only when obviously named; a host
	// side that is a bare ${VAR} is kept and classified after interpolation.
	Volumes     []Scalar
	Command     []Scalar
	Entrypoint  []Scalar
	Healthcheck []Scalar
	// HasLimits reports a non-empty deploy.resources.limits mapping.
	HasLimits bool
}

// EnvFileRef is one env_file: entry.
type EnvFileRef struct {
	Path Scalar
	// Required is false only for the long form with required: false.
	Required bool
}

// EnvEntry is one environment: entry, from the map form (KEY: value) or the list form
// (- KEY=value). HasValue is false for a bare KEY, which Compose fills from the deploy env.
type EnvEntry struct {
	Key      string
	Value    string
	HasValue bool
	Line     int
}

// NetworkDecl is one entry of the top-level networks: block.
type NetworkDecl struct {
	// Key is the declaration key services refer to.
	Key string
	// Name is the network's name: its name: value when set, else Key.
	Name     string
	External bool
	Line     int
}

// Ref is one variable reference in a compose file.
type Ref struct {
	Name string
	// Line is the 1-based line of the reference itself, also inside a multi-line scalar.
	Line int
	// Path is the YAML path of the scalar holding the reference, e.g. services.db.volumes[0].
	Path string
	// Service is the service the scalar belongs to; empty for top-level keys.
	Service string
	// Op is the operator: "", ":-", "-", ":+", "+", ":?" or "?".
	Op string
	// HasDefault is set for ${VAR:-d} and ${VAR-d}; Default holds d, unresolved.
	HasDefault bool
	Default    string
	// Required is set for ${VAR:?e} and ${VAR?e}.
	Required bool
	// Optional is set when an unset VAR is legitimate: ${VAR:+a}, ${VAR+a}, or a reference
	// nested in another reference's alternative or error text.
	Optional bool
	// Fallback lists the enclosing references when this one sits in their default text:
	// ${A:-${B}} gives B the fallback A (Colon set). It is evaluated only when every one of
	// them is unset (or, with Colon, unset or empty).
	Fallback []Fallback
}

// ParseError is a syntax error in a repo file.
type ParseError struct {
	// Line is 1-based; 0 when unknown.
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	}
	return e.Message
}
