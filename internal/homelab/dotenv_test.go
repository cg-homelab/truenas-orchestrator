package homelab_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

type kv struct {
	Key, Value string
	Line       int
}

func entries(d homelab.DotEnv) []kv {
	var out []kv
	for _, e := range d.Entries {
		out = append(out, kv{e.Key, e.Value.Reveal(), e.Line})
	}
	return out
}

func TestParseDotEnv(t *testing.T) {
	src := "# comment\n" +
		"\n" +
		"PLAIN=value\n" +
		"export EXPORTED=yes\n" +
		"DOUBLE=\"quoted # not a comment\"\n" +
		"SINGLE='it\\'s'\n" +
		"ESCAPED=\"a\\\"b\\\\c\\n\"\n" +
		"TRAILING=value # comment\n" +
		"  SPACED  =  padded  \n" +
		"EMPTY=\n" +
		"no equals sign\n" +
		"=novalue\n" +
		"URL=http://x/?a=b\n" +
		"SERVICE_NAME='b' # don't change\n" +
		"FIRST=\"a\" \"b\"\n" +
		"MULTI=\"line one\n" +
		"line two\"\n" +
		"AFTER=1\n" +
		"exported_name=keeps export prefix only with a space\n" +
		"PLAIN=again\n"
	got, err := homelab.ParseDotEnv([]byte(src))
	if err != nil {
		t.Fatalf("ParseDotEnv() error = %v", err)
	}
	want := []kv{
		{"PLAIN", "again", 20}, // a repeated key keeps its first position in order, last value and line
		{"EXPORTED", "yes", 4},
		{"DOUBLE", "quoted # not a comment", 5},
		{"SINGLE", "it's", 6},
		{"ESCAPED", "a\"b\\c\n", 7},
		{"TRAILING", "value", 8},
		{"SPACED", "padded", 9},
		{"EMPTY", "", 10},
		{"URL", "http://x/?a=b", 13},
		{"SERVICE_NAME", "b", 14}, // ends at the first unescaped quote, not the last on the line
		{"FIRST", "a", 15},
		{"MULTI", "line one\nline two", 16},
		{"AFTER", "1", 18},
		{"exported_name", "keeps export prefix only with a space", 19},
	}
	if diff := cmp.Diff(want, entries(got)); diff != "" {
		t.Errorf("ParseDotEnv() mismatch (-want +got):\n%s", diff)
	}
	if e, ok := got.Lookup("URL"); !ok || e.Line != 13 {
		t.Errorf("Lookup(URL) = %+v, %v", e, ok)
	}
	if _, ok := got.Lookup("MISSING"); ok {
		t.Errorf("Lookup(MISSING) found")
	}
}

func TestParseDotEnvUnterminatedQuote(t *testing.T) {
	src := "A=1\nSECRET=\"hunter2-never-printed\nB=2\n"
	got, err := homelab.ParseDotEnv([]byte(src))
	var pe *homelab.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("ParseDotEnv() error = %v, want *homelab.ParseError", err)
	}
	if pe.Line != 2 {
		t.Errorf("ParseError.Line = %d, want 2", pe.Line)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q contains the value", err)
	}
	if diff := cmp.Diff([]kv{{"A", "1", 1}}, entries(got)); diff != "" {
		t.Errorf("entries before the error mismatch (-want +got):\n%s", diff)
	}
}
