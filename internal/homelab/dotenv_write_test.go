package homelab_test

import (
	"errors"
	"maps"
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// envValues parses src and reveals every value, for comparison in tests.
func envValues(t *testing.T, src []byte) map[string]string {
	t.Helper()
	d, err := homelab.ParseDotEnv(src)
	if err != nil {
		t.Fatalf("ParseDotEnv() error = %v", err)
	}
	out := map[string]string{}
	for _, e := range d.Entries {
		out[e.Key] = e.Value.Reveal()
	}
	return out
}

func secrets(kv ...string) map[string]homelab.Secret {
	m := map[string]homelab.Secret{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = homelab.NewSecret(kv[i+1])
	}
	return m
}

func TestUpdateDotEnvRoundTrip(t *testing.T) {
	values := []string{
		"", "plain", "with space", " leading", "trailing ", "a#b", "a #b", "#start", "$HOME", "${X}", "$$",
		`dq"`, `sq'`, `'quoted'`, `"quoted"`, `back\slash`, `trail\`, `\n literal`, "new\nline", "cr\r\nlf",
		"tab\there", "unicodé ✓ 日本", "=equals=", "\ufeffbom", "x\\\"y", "\\$", "multi\n\nblank",
	}
	bases := []string{
		"",
		"K=old\n",
		"# comment\nK='old' # x\nOTHER=1\n",
		"export K = \"old\" # trailing\nOTHER=two words\n",
		"K=\"multi\nline\"\nOTHER=1",
		"K=first\nOTHER=1\nK=second\n",
		"OTHER=1\r\nK=old\r\n",
		"K=\"x\"junk after quote\n",
	}
	for _, base := range bases {
		before := envValues(t, []byte(base))
		for _, v := range values {
			got, err := homelab.UpdateDotEnv([]byte(base), secrets("K", v), nil)
			if err != nil {
				t.Fatalf("UpdateDotEnv(%q) error = %v", base, err)
			}
			want := maps.Clone(before)
			want["K"] = v
			if diff := cmp.Diff(want, envValues(t, got)); diff != "" {
				t.Errorf("UpdateDotEnv(%q, K=%q) = %q; parsed mismatch (-want +got):\n%s", base, v, got, diff)
			}
		}
	}
}

func TestUpdateDotEnvBytes(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		set   map[string]homelab.Secret
		unset []string
		want  string
	}{
		{
			name: "no change is byte-identical",
			src:  "# header\n\nA=1 # one\nB='two'\nexport C=\"three\"\n",
			want: "# header\n\nA=1 # one\nB='two'\nexport C=\"three\"\n",
		},
		{
			name: "rewrite in place keeps comments, order and other quoting",
			src:  "# header\n\nA=1\nB='two words'\n# trailing comment\nC=3\n",
			set:  secrets("A", "new"),
			want: "# header\n\nA=new\nB='two words'\n# trailing comment\nC=3\n",
		},
		{
			name: "only the last of duplicate keys is rewritten",
			src:  "A=first\nB=1\nA=second\n",
			set:  secrets("A", "third"),
			want: "A=first\nB=1\nA=third\n",
		},
		{
			name: "inline comment and export prefix kept, value quoted to stay apart",
			src:  "export  A = old # keep me\n",
			set:  secrets("A", "new"),
			want: "export  A = \"new\" # keep me\n",
		},
		{
			name: "quoted value with junk after the closing quote",
			src:  "A=\"old\"junk\n",
			set:  secrets("A", "new"),
			want: "A=\"new\"junk\n",
		},
		{
			name: "multi-line quoted value collapses to one escaped line",
			src:  "A=\"line1\nline2\"\nB=2\n",
			set:  secrets("A", "x\ny"),
			want: "A=\"x\\ny\"\nB=2\n",
		},
		{
			name: "values needing quotes",
			src:  "",
			set:  secrets("A", "has space", "B", "a#b", "C", "$X", "D", `q"`, "E", ""),
			want: "A=\"has space\"\nB=\"a#b\"\nC=\"\\$X\"\nD=\"q\\\"\"\nE=\n",
		},
		{
			name: "absent keys appended sorted",
			src:  "A=1\n",
			set:  secrets("Z", "26", "M", "13"),
			want: "A=1\nM=13\nZ=26\n",
		},
		{
			name: "append to a file without trailing newline keeps it without",
			src:  "A=1",
			set:  secrets("B", "2"),
			want: "A=1\nB=2",
		},
		{
			name: "CRLF file: rewritten line keeps CRLF, appended line uses it",
			src:  "A=1\r\nB=2\r\n",
			set:  secrets("A", "x", "C", "3"),
			want: "A=x\r\nB=2\r\nC=3\r\n",
		},
		{
			name: "unset removes every statement of the key",
			src:  "A=1\nB=2\n# c\nA=3\n",
			unset: []string{
				"A",
			},
			want: "B=2\n# c\n",
		},
		{
			name:  "set wins over unset",
			src:   "A=1\n",
			set:   secrets("A", "2"),
			unset: []string{"A"},
			want:  "A=2\n",
		},
		{
			name: "byte order mark kept",
			src:  "\xef\xbb\xbfA=1\n",
			set:  secrets("A", "2"),
			want: "\xef\xbb\xbfA=2\n",
		},
		{
			name: "commented-out key is not a statement",
			src:  "# A=old\n",
			set:  secrets("A", "new"),
			want: "# A=old\nA=new\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := homelab.UpdateDotEnv([]byte(tt.src), tt.set, tt.unset)
			if err != nil {
				t.Fatalf("UpdateDotEnv() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, string(got)); diff != "" {
				t.Errorf("UpdateDotEnv() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateDotEnvErrors(t *testing.T) {
	_, err := homelab.UpdateDotEnv([]byte("A=\"unterminated\nB=2\n"), secrets("B", "x"), nil)
	var pe *homelab.ParseError
	if !errors.As(err, &pe) || pe.Line != 1 {
		t.Errorf("UpdateDotEnv(unterminated) error = %v, want *ParseError at line 1", err)
	}
	_, err = homelab.UpdateDotEnv(nil, secrets("BAD-KEY", "hunter2"), nil)
	if !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("UpdateDotEnv(bad key) error = %v, want ErrValidation", err)
	}
	if err != nil && regexp.MustCompile(`hunter2`).MatchString(err.Error()) {
		t.Errorf("error %q contains the value", err)
	}
}

// FuzzUpdateDotEnv checks UpdateDotEnv against ParseDotEnv: setting any key to any value is
// read back exactly, and every other key keeps its value.
func FuzzUpdateDotEnv(f *testing.F) {
	for _, s := range []string{
		"", "A=1\n", "export A = 'x' # c\nB=\"y\nz\"\n", "A=1\r\nB=2", " \t A=1 \t\n#x\n=\nnoeq\n",
		"A='it\\'s'\n", "A=\"a\\\"b\"\n", "\xef\xbb\xbfA=1", "A=\"x\"tail\nA=2\n",
	} {
		f.Add(s, "A", "value with \"quotes\" and $ and #")
	}
	keyRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	f.Fuzz(func(t *testing.T, src, key, value string) {
		before, err := homelab.ParseDotEnv([]byte(src))
		if err != nil {
			if _, uerr := homelab.UpdateDotEnv([]byte(src), nil, nil); uerr == nil {
				t.Fatalf("UpdateDotEnv accepted %q, which ParseDotEnv rejects", src)
			}
			return
		}
		if !keyRe.MatchString(key) {
			key = "FUZZ_KEY"
		}
		same, err := homelab.UpdateDotEnv([]byte(src), nil, nil)
		if err != nil || string(same) != src {
			t.Fatalf("UpdateDotEnv(%q, nil) = %q, %v; want the input unchanged", src, same, err)
		}
		got, err := homelab.UpdateDotEnv([]byte(src), secrets(key, value), nil)
		if err != nil {
			t.Fatalf("UpdateDotEnv(%q) error = %v", src, err)
		}
		after, err := homelab.ParseDotEnv(got)
		if err != nil {
			t.Fatalf("ParseDotEnv(UpdateDotEnv(%q)) = %q: error %v", src, got, err)
		}
		want := map[string]string{}
		for _, e := range before.Entries {
			want[e.Key] = e.Value.Reveal()
		}
		want[key] = value
		gotVals := map[string]string{}
		for _, e := range after.Entries {
			gotVals[e.Key] = e.Value.Reveal()
		}
		if diff := cmp.Diff(want, gotVals); diff != "" {
			t.Errorf("UpdateDotEnv(%q, %s=%q) = %q; mismatch (-want +got):\n%s", src, key, value, got, diff)
		}
	})
}
