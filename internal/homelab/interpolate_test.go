package homelab_test

import (
	"sort"
	"testing"

	"github.com/compose-spec/compose-go/v2/template"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// grammarCase is one input of the Compose interpolation grammar and what ScanTemplate finds.
type grammarCase struct {
	name    string
	in      string
	refs    []homelab.VarRef // Offset is checked separately
	escaped []string
	// invalid marks a template compose-go rejects: ScanTemplate reports it in Invalid and finds
	// no reference in it.
	invalid bool
}

type ref = homelab.VarRef

func fb(name string, colon bool) homelab.Fallback { return homelab.Fallback{Name: name, Colon: colon} }

// grammarCases drives TestScanTemplate and, through compose-go, TestGrammarMatchesComposeGo, so
// every case here is checked against the reference grammar (D29).
var grammarCases = []grammarCase{
	{name: "plain braced", in: "${A}", refs: []ref{{Name: "A"}}},
	{name: "unbraced", in: "$A/x", refs: []ref{{Name: "A"}}},
	{name: "soft default", in: "${A:-d}", refs: []ref{{Name: "A", Op: ":-", HasDefault: true, Default: "d"}}},
	{name: "hard default", in: "${A-d}", refs: []ref{{Name: "A", Op: "-", HasDefault: true, Default: "d"}}},
	{name: "empty default", in: "${A:-}", refs: []ref{{Name: "A", Op: ":-", HasDefault: true}}},
	{name: "required soft", in: "${A:?boom}", refs: []ref{{Name: "A", Op: ":?", Required: true}}},
	{name: "required hard", in: "${A?boom}", refs: []ref{{Name: "A", Op: "?", Required: true}}},
	{name: "alternative", in: "${A:+x}", refs: []ref{{Name: "A", Op: ":+", Optional: true}}},
	{name: "alternative hard", in: "${A+x}", refs: []ref{{Name: "A", Op: "+", Optional: true}}},
	{name: "escaped literal", in: "$$"},
	{name: "escaped name", in: "echo $$HOME", escaped: []string{"HOME"}},
	{name: "escaped braced", in: "echo $${HOME}/x", escaped: []string{"HOME"}},
	{name: "escaped then ref", in: "$$$A", refs: []ref{{Name: "A"}}},
	{name: "argon2 hash", in: "$$argon2id$$v=19$$m=65536", escaped: []string{"argon2id", "v", "m"}},
	{
		name: "nested default", in: "${A:-${B}}",
		refs: []ref{{Name: "B", Fallback: []homelab.Fallback{fb("A", true)}}, {Name: "A", Op: ":-", HasDefault: true, Default: "${B}"}},
	},
	{
		name: "nested hard default", in: "${A-${B}}",
		refs: []ref{{Name: "B", Fallback: []homelab.Fallback{fb("A", false)}}, {Name: "A", Op: "-", HasDefault: true, Default: "${B}"}},
	},
	{
		name: "doubly nested default", in: "${A:-${B:-${C}}}",
		refs: []ref{
			{Name: "C", Fallback: []homelab.Fallback{fb("A", true), fb("B", true)}},
			{Name: "B", Op: ":-", HasDefault: true, Default: "${C}", Fallback: []homelab.Fallback{fb("A", true)}},
			{Name: "A", Op: ":-", HasDefault: true, Default: "${B:-${C}}"},
		},
	},
	{
		name: "ref in alternative is optional", in: "${A:+${B}}",
		refs: []ref{{Name: "B", Optional: true}, {Name: "A", Op: ":+", Optional: true}},
	},
	{
		name: "two refs after an operator", in: "${A:-x}/${B}",
		refs: []ref{{Name: "A", Op: ":-", HasDefault: true, Default: "x"}, {Name: "B"}},
	},
	{
		name: "unbalanced braces in a default", in: "${A:-{x}",
		refs: []ref{{Name: "A", Op: ":-", HasDefault: true, Default: "{x"}},
	},
	{
		name: "label", in: "traefik.http.routers.${SERVICE_NAME}.rule=Host(`${SERVICE_NAME}.${DOMAIN_NAME}`)",
		refs: []ref{{Name: "SERVICE_NAME"}, {Name: "SERVICE_NAME"}, {Name: "DOMAIN_NAME"}},
	},
	{name: "lowercase name", in: "${lower_1}", refs: []ref{{Name: "lower_1"}}},
	{name: "digit is not a name", in: "$1 costs $"},
	{name: "unclosed brace", in: "${A", invalid: true},
	{name: "space in name", in: "${FOO BAR}", invalid: true},
	{name: "invalid operator", in: "${A!x}", invalid: true},
	{name: "empty braces", in: "${}", invalid: true},
	{name: "no refs", in: "plain"},
}

func TestScanTemplate(t *testing.T) {
	for _, tt := range grammarCases {
		t.Run(tt.name, func(t *testing.T) {
			ts := homelab.ScanTemplate(tt.in)
			if diff := cmp.Diff(tt.refs, ts.Refs, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(homelab.VarRef{}, "Offset")); diff != "" {
				t.Errorf("ScanTemplate(%q) refs mismatch (-want +got):\n%s", tt.in, diff)
			}
			var escaped []string
			for _, e := range ts.Escaped {
				escaped = append(escaped, e.Name)
			}
			if diff := cmp.Diff(tt.escaped, escaped, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ScanTemplate(%q) escaped mismatch (-want +got):\n%s", tt.in, diff)
			}
			if got := len(ts.Invalid) > 0; got != tt.invalid {
				t.Errorf("ScanTemplate(%q) invalid = %v (%v), want %v", tt.in, got, ts.Invalid, tt.invalid)
			}
		})
	}
}

func TestScanTemplateOffsets(t *testing.T) {
	ts := homelab.ScanTemplate("a ${A:-x${B}} $C $$D ${")
	var got []int
	for _, r := range ts.Refs {
		got = append(got, r.Offset)
	}
	if diff := cmp.Diff([]int{8, 2, 14}, got); diff != "" {
		t.Errorf("ref offsets mismatch (-want +got):\n%s", diff)
	}
	if len(ts.Escaped) != 1 || ts.Escaped[0].Offset != 17 {
		t.Errorf("escaped = %+v, want D at offset 17", ts.Escaped)
	}
	if diff := cmp.Diff([]int{21}, ts.Invalid); diff != "" {
		t.Errorf("invalid offsets mismatch (-want +got):\n%s", diff)
	}
}

// TestGrammarMatchesComposeGo runs every grammar case through compose-go/v2/template, the
// reference implementation, and fails on any divergence (D29):
//   - the set of names ScanTemplate finds equals template.ExtractVariables';
//   - a template compose-go rejects is one ScanTemplate reports invalid;
//   - Interpolate equals template.SubstituteWithOptions with every name set non-empty, and with
//     every name set empty. Set-but-empty keeps compose-go from logging (it logs only for unset
//     names), and exercises the :- / - and :+ / + and :? / ? distinctions.
func TestGrammarMatchesComposeGo(t *testing.T) {
	for _, tt := range grammarCases {
		t.Run(tt.name, func(t *testing.T) {
			names := map[string]bool{}
			for _, r := range homelab.ScanTemplate(tt.in).Refs {
				names[r.Name] = true
			}
			var got []string
			for n := range names {
				got = append(got, n)
			}
			sort.Strings(got)
			var want []string
			for n := range template.ExtractVariables(map[string]any{"k": tt.in}, template.DefaultPattern) {
				if n != "" && !tt.invalid { // compose-go yields junk names for an invalid ${...}
					want = append(want, n)
				}
			}
			sort.Strings(want)
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("names mismatch with compose-go (-want +got):\n%s", diff)
			}

			for _, value := range []string{"val", ""} {
				env := map[string]string{}
				for _, n := range want {
					env[n] = value
				}
				lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
				cgOut, cgErr := template.SubstituteWithOptions(tt.in, lookup, template.WithoutLogging)
				if tt.invalid {
					if cgErr == nil {
						t.Errorf("compose-go accepts %q, which the case marks invalid", tt.in)
					}
					return
				}
				out, missing := homelab.Interpolate(tt.in, lookup)
				if (cgErr != nil) != (len(missing) > 0) {
					t.Errorf("Interpolate(%q) with names=%q: missing %v, compose-go error %v", tt.in, value, missing, cgErr)
				}
				if cgErr == nil && out != cgOut {
					t.Errorf("Interpolate(%q) with names=%q = %q, compose-go gives %q", tt.in, value, out, cgOut)
				}
			}
		})
	}
}

func TestInterpolate(t *testing.T) {
	env := map[string]string{"SET": "v", "EMPTY": "", "ROOT": "/mnt/x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	tests := []struct {
		in      string
		want    string
		missing []string
	}{
		{"${ROOT}/a", "/mnt/x/a", nil},
		{"$ROOT/a", "/mnt/x/a", nil},
		{"${UNSET}/a", "/a", []string{"UNSET"}},
		{"${UNSET:-d}", "d", nil},
		{"${EMPTY:-d}", "d", nil},
		{"${EMPTY-d}", "", nil},
		{"${UNSET-d}", "d", nil},
		{"${SET:+alt}", "alt", nil},
		{"${EMPTY:+alt}", "", nil},
		{"${EMPTY+alt}", "alt", nil},
		{"${UNSET:+alt}", "", nil},
		{"${UNSET:?e}", "", []string{"UNSET"}},
		{"${EMPTY:?e}", "", []string{"EMPTY"}},
		{"${EMPTY?e}", "", nil},
		{"${UNSET:-${SET}}", "v", nil},
		{"${SET:-${UNSET}}", "v", nil},
		{"${UNSET:-${ALSO_UNSET}}", "", []string{"ALSO_UNSET"}},
		{"$$SET and $${SET}", "$SET and ${SET}", nil},
		{"${UNSET:-x}/${SET}", "x/v", nil},
	}
	for _, tt := range tests {
		got, missing := homelab.Interpolate(tt.in, lookup)
		if got != tt.want {
			t.Errorf("Interpolate(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if diff := cmp.Diff(tt.missing, missing, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("Interpolate(%q) missing mismatch (-want +got):\n%s", tt.in, diff)
		}
	}
}

// TestInterpolateMatchesComposeGo compares results with compose-go's substitution. Only inputs
// that never look up an unset variable outside a default are used: compose-go writes a warning
// to the global logger for those, which is why it is not called in production.
func TestInterpolateMatchesComposeGo(t *testing.T) {
	env := map[string]string{"SET": "v", "EMPTY": "", "ROOT": "/mnt/x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	inputs := []string{
		"${ROOT}/a", "$ROOT/a", "${UNSET:-d}", "${EMPTY:-d}", "${EMPTY-d}", "${UNSET-d}",
		"${SET:+alt}", "${EMPTY:+alt}", "${EMPTY+alt}", "${UNSET:+alt}", "${UNSET:-${SET}}",
		"${SET:-${ROOT}}", "$$SET and $${SET}", "${UNSET:-x}/${SET}", "a$", "$1", "${SET:-x} ${ROOT:-y}",
	}
	for _, in := range inputs {
		want, err := template.SubstituteWithOptions(in, lookup, template.WithoutLogging)
		if err != nil {
			t.Fatalf("template.SubstituteWithOptions(%q): %v", in, err)
		}
		got, missing := homelab.Interpolate(in, lookup)
		if got != want || len(missing) != 0 {
			t.Errorf("Interpolate(%q) = %q, %v; compose-go gives %q", in, got, missing, want)
		}
	}
}
