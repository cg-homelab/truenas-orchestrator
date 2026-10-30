package homelab_test

import (
	"testing"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestValidateValue(t *testing.T) {
	type spec = homelab.ValueValidatorSpec
	tests := []struct {
		name  string
		spec  spec
		value string
		ok    bool
	}{
		{"duration days", spec{Type: "duration"}, "60d", true},
		{"duration compound", spec{Type: "duration"}, "1h30m", true},
		{"duration ms", spec{Type: "duration"}, "500ms", true},
		{"duration bare number", spec{Type: "duration"}, "60", false},
		{"duration junk", spec{Type: "duration"}, "soon", false},
		{"port ok", spec{Type: "port"}, "8080", true},
		{"port zero", spec{Type: "port"}, "0", false},
		{"port too big", spec{Type: "port"}, "65536", false},
		{"port text", spec{Type: "port"}, "http", false},
		{"identifier ok", spec{Type: "identifier"}, "db-postgres", true},
		{"identifier upper", spec{Type: "identifier"}, "DbPostgres", false},
		{"identifier leading dash", spec{Type: "identifier"}, "-x", false},
		{"identifier equals app", spec{Type: "identifier", EqualsAppName: true}, "app", true},
		{"identifier not app", spec{Type: "identifier", EqualsAppName: true}, "other", false},
		{"absolute ok", spec{Type: "absolute_path"}, "/mnt/x", true},
		{"absolute relative", spec{Type: "absolute_path"}, "mnt/x", false},
		{"non_empty ok", spec{Type: "non_empty"}, "x", true},
		{"non_empty blank", spec{Type: "non_empty"}, "  ", false},
		{"non_empty placeholder", spec{Type: "non_empty", NotOneOf: []string{"REPLACE_THIS"}}, "REPLACE_THIS", false},
		{"integer ok", spec{Type: "integer"}, "-42", true},
		{"integer float", spec{Type: "integer"}, "4.2", false},
		{"bool ok", spec{Type: "bool"}, "true", true},
		{"bool numeric", spec{Type: "bool"}, "0", true},
		{"bool junk", spec{Type: "bool"}, "yes please", false},
		{"one_of ok", spec{Type: "one_of", Values: []string{"a", "b"}}, "b", true},
		{"one_of miss", spec{Type: "one_of", Values: []string{"a", "b"}}, "c", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := homelab.ValidateValue(tt.spec, tt.value, "app")
			if (msg == "") != tt.ok {
				t.Errorf("ValidateValue(%+v, %q) = %q, want ok=%v", tt.spec, tt.value, msg, tt.ok)
			}
		})
	}
}
