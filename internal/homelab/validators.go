package homelab

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ValueValidatorSpec is one [env_validators] entry in setup.toml.
type ValueValidatorSpec struct {
	// Type is one of duration, port, identifier, absolute_path, non_empty, integer, bool, one_of.
	Type string `toml:"type"`
	// EqualsAppName additionally requires the value to equal the app directory name.
	EqualsAppName bool `toml:"equals_app_name"`
	// NotOneOf rejects these exact values, whatever the type.
	NotOneOf []string `toml:"not_one_of"`
	// Values is the allowed set for type one_of.
	Values []string `toml:"values"`
}

var (
	// durationRe accepts Go and Prometheus durations: one or more <number><unit> groups.
	durationRe   = regexp.MustCompile(`^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h|d|w|y))+$`)
	identifierRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func knownValidator(t string) bool {
	switch t {
	case "duration", "port", "identifier", "absolute_path", "non_empty", "integer", "bool", "one_of":
		return true
	}
	return false
}

// ValidateValue checks value against spec for the app named app. It returns "" when the value
// passes, or a description of the expected shape. The description never contains the value.
func ValidateValue(spec ValueValidatorSpec, value, app string) string {
	if slices.Contains(spec.NotOneOf, value) {
		return "is a disallowed placeholder value"
	}
	if msg := validateType(spec, value); msg != "" {
		return msg
	}
	if spec.EqualsAppName && value != app {
		return fmt.Sprintf("does not equal the app directory name %q", app)
	}
	return ""
}

func validateType(spec ValueValidatorSpec, value string) string {
	switch spec.Type {
	case "duration":
		if !durationRe.MatchString(value) {
			return "is not a duration with a unit (e.g. 60d, 12h, 30s)"
		}
	case "port":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return "is not a port number (1-65535)"
		}
	case "identifier":
		if !identifierRe.MatchString(value) {
			return "is not an identifier (lowercase letters, digits and -, starting with a letter or digit)"
		}
	case "absolute_path":
		if !path.IsAbs(value) {
			return "is not an absolute path"
		}
	case "non_empty":
		if strings.TrimSpace(value) == "" {
			return "is empty"
		}
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return "is not an integer"
		}
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return "is not a boolean (true or false)"
		}
	case "one_of":
		if !slices.Contains(spec.Values, value) {
			return fmt.Sprintf("is not one of %s", strings.Join(spec.Values, ", "))
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
