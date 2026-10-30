package homelab_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestParseSetupFieldIssues(t *testing.T) {
	const modeMsg = "must be an octal permission mode such as 0750 (3 or 4 digits, no setuid, setgid or sticky bit)"
	tests := []struct {
		name   string
		src    string
		issues []homelab.SetupIssue
	}{
		{
			name: "valid entries with defaults",
			src: `[[dirs]]
path = "/a"
[[dirs]]
path = "/b"
mode = "755"
[[secrets]]
file = "/a/s"
generate = { bytes = 1024 }
[[secrets]]
file = "/a/t"
value_from = "KEY_1"
mode = "0400"
[[config_files]]
from = "config/x.yml"
to = "/a/x.yml"
on_exists = "prompt"
[[config_files]]
to = "/a/y.yml"
create_empty = true
[[prompts]]
key = "_KEY_1"
generate = { bytes = 1, encoding = "alphanumeric" }
`,
		},
		{
			name: "dir mode not octal, on the second entry",
			src: `[[dirs]]
path = "/a"
mode = "0750"

[[dirs]]
path = "/b"
mode = "0790"
`,
			issues: []homelab.SetupIssue{{Key: "dirs.mode", Line: 7, Message: "setup.toml [[dirs]] #2 mode " + modeMsg}},
		},
		{
			name: "dir mode with setuid digit or wrong length",
			src: `[[dirs]]
path = "/a"
mode = "4755"
[[dirs]]
path = "/b"
mode = "75"
`,
			issues: []homelab.SetupIssue{
				{Key: "dirs.mode", Line: 3, Message: "setup.toml [[dirs]] #1 mode " + modeMsg},
				{Key: "dirs.mode", Line: 6, Message: "setup.toml [[dirs]] #2 mode " + modeMsg},
			},
		},
		{
			name: "empty dir path points at the header",
			src:  "[[dirs]]\nmode = \"0750\"\n",
			issues: []homelab.SetupIssue{
				{Key: "dirs.path", Line: 1, Message: "setup.toml [[dirs]] #1 path must not be empty"},
			},
		},
		{
			name: "secret mode open to group",
			src: `[[secrets]]
file = "/a/s"
generate = { bytes = 32 }
mode = "0640"
`,
			issues: []homelab.SetupIssue{{
				Key: "secrets.mode", Line: 4,
				Message: "setup.toml [[secrets]] #1 mode must not grant group or other access (e.g. 0600 or 0400)",
			}},
		},
		{
			name: "secret with both generate and value_from",
			src: `[[secrets]]
file = "/a/s"
generate = { bytes = 32 }
value_from = "X"
`,
			issues: []homelab.SetupIssue{{
				Key: "secrets", Line: 1,
				Message: "setup.toml [[secrets]] #1 needs exactly one of generate and value_from",
			}},
		},
		{
			name: "secret with neither, and an empty file",
			src:  "[[secrets]]\nmode = \"0600\"\n",
			issues: []homelab.SetupIssue{
				{Key: "secrets.file", Line: 1, Message: "setup.toml [[secrets]] #1 file must not be empty"},
				{Key: "secrets", Line: 1, Message: "setup.toml [[secrets]] #1 needs exactly one of generate and value_from"},
			},
		},
		{
			name: "generate bytes and encoding out of range",
			src: `[[secrets]]
file = "/a/s"
generate = { bytes = 0, encoding = "base32" }
[[secrets]]
file = "/a/t"
generate = { bytes = 1025 }
`,
			issues: []homelab.SetupIssue{
				{Key: "secrets.generate.bytes", Line: 3, Message: "setup.toml [[secrets]] #1 generate.bytes must be between 1 and 1024"},
				{Key: "secrets.generate.encoding", Line: 3, Message: "setup.toml [[secrets]] #1 generate.encoding must be one of base64, hex, alphanumeric"},
				{Key: "secrets.generate.bytes", Line: 6, Message: "setup.toml [[secrets]] #2 generate.bytes must be between 1 and 1024"},
			},
		},
		{
			name: "value_from not a variable name",
			src:  "[[secrets]]\nfile = \"/a/s\"\nvalue_from = \"1BAD\"\n",
			issues: []homelab.SetupIssue{{
				Key: "secrets.value_from", Line: 3,
				Message: "setup.toml [[secrets]] #1 value_from must be a variable name ([A-Za-z_][A-Za-z0-9_]*)",
			}},
		},
		{
			name: "config file with both from and create_empty",
			src: `[[config_files]]
from = "a.yml"
to = "/a/a.yml"
create_empty = true
`,
			issues: []homelab.SetupIssue{{
				Key: "config_files", Line: 1,
				Message: "setup.toml [[config_files]] #1 needs exactly one of from and create_empty = true",
			}},
		},
		{
			name: "config file with neither, and no to",
			src:  "[[config_files]]\non_exists = \"keep\"\n",
			issues: []homelab.SetupIssue{
				{Key: "config_files.to", Line: 1, Message: "setup.toml [[config_files]] #1 to must not be empty"},
				{Key: "config_files", Line: 1, Message: "setup.toml [[config_files]] #1 needs exactly one of from and create_empty = true"},
			},
		},
		{
			name: "config from escaping the app dir",
			src: `[[config_files]]
from = "/etc/passwd"
to = "/a/a"
[[config_files]]
from = "config/../../db-postgres/.env"
to = "/a/b"
`,
			issues: []homelab.SetupIssue{
				{Key: "config_files.from", Line: 2, Message: "setup.toml [[config_files]] #1 from must be a relative path inside the app directory (no leading /, no .. segment)"},
				{Key: "config_files.from", Line: 5, Message: "setup.toml [[config_files]] #2 from must be a relative path inside the app directory (no leading /, no .. segment)"},
			},
		},
		{
			name: "config from an env file",
			src:  "[[config_files]]\nfrom = \"../.global.env\"\nto = \"/a/a\"\n[[config_files]]\nfrom = \"sub/.env\"\nto = \"/a/b\"\n",
			issues: []homelab.SetupIssue{
				{Key: "config_files.from", Line: 2, Message: "setup.toml [[config_files]] #1 from must be a relative path inside the app directory (no leading /, no .. segment)"},
				{Key: "config_files.from", Line: 5, Message: "setup.toml [[config_files]] #2 from must not be an env file (.env or .global.env); secrets reach the host only through [[secrets]]"},
			},
		},
		{
			name: "unknown on_exists",
			src:  "[[config_files]]\nto = \"/a/a\"\ncreate_empty = true\non_exists = \"merge\"\n",
			issues: []homelab.SetupIssue{{
				Key: "config_files.on_exists", Line: 4,
				Message: "setup.toml [[config_files]] #1 on_exists must be one of keep, overwrite, prompt",
			}},
		},
		{
			name: "prompt key empty or invalid, and a bad generate",
			src: `[[prompts]]
label = "x"
[[prompts]]
key = "A-B"
generate = { bytes = 2048, encoding = "hex" }
`,
			issues: []homelab.SetupIssue{
				{Key: "prompts.key", Line: 1, Message: "setup.toml [[prompts]] #1 key must be a variable name ([A-Za-z_][A-Za-z0-9_]*)"},
				{Key: "prompts.key", Line: 4, Message: "setup.toml [[prompts]] #2 key must be a variable name ([A-Za-z_][A-Za-z0-9_]*)"},
				{Key: "prompts.generate.bytes", Line: 5, Message: "setup.toml [[prompts]] #2 generate.bytes must be between 1 and 1024"},
			},
		},
		{
			name: "field issues follow unknown-key issues",
			src:  "[[dirs]]\npath = \"/a\"\nmode = \"x\"\nbogus = 1\n",
			issues: []homelab.SetupIssue{
				{Key: "dirs.bogus", Line: 4, Message: "setup.toml has unknown key dirs.bogus, which is not a registered primitive"},
				{Key: "dirs.mode", Line: 3, Message: "setup.toml [[dirs]] #1 mode " + modeMsg},
			},
		},
		{
			name: "field issues survive a type error elsewhere",
			src:  "[app]\nrequires = \"x\"\n[[dirs]]\npath = \"/a\"\nmode = \"999\"\n",
			issues: []homelab.SetupIssue{
				{Key: "app.requires", Line: 2, Message: "setup.toml key app.requires has the wrong type for the primitive registry"},
				{Key: "dirs.mode", Line: 5, Message: "setup.toml [[dirs]] #1 mode " + modeMsg},
			},
		},
		{
			name: "inline array of tables falls back to the table line",
			src:  "dirs = [{ path = \"/a\", mode = \"8\" }]\n",
			issues: []homelab.SetupIssue{
				{Key: "dirs.mode", Line: 1, Message: "setup.toml [[dirs]] #1 mode " + modeMsg},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issues, err := homelab.ParseSetup([]byte(tt.src))
			if err != nil {
				t.Fatalf("ParseSetup() error = %v", err)
			}
			if diff := cmp.Diff(tt.issues, issues); diff != "" {
				t.Errorf("ParseSetup() issues mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetupDeclared(t *testing.T) {
	tests := []struct {
		name string
		f    homelab.SetupFile
		want bool
	}{
		{"empty", homelab.SetupFile{}, false},
		{"validators and requires only", homelab.SetupFile{
			App:           homelab.SetupApp{Requires: []string{"db"}},
			EnvValidators: map[string]homelab.ValueValidatorSpec{"X": {Type: "port"}},
		}, false},
		{"dirs", homelab.SetupFile{Dirs: []homelab.SetupDir{{Path: "/a"}}}, true},
		{"secrets", homelab.SetupFile{Secrets: []homelab.SetupSecret{{File: "/a"}}}, true},
		{"config files", homelab.SetupFile{ConfigFiles: []homelab.SetupConfigFile{{To: "/a"}}}, true},
		{"prompts", homelab.SetupFile{Prompts: []homelab.SetupPrompt{{Key: "A"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := homelab.SetupDeclared(tt.f); got != tt.want {
				t.Errorf("SetupDeclared() = %v, want %v", got, tt.want)
			}
		})
	}
}
