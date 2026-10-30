package homelab_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestParseSetup(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		requires []string
		issues   []homelab.SetupIssue
	}{
		{
			name: "full example decodes",
			src: `[app]
name = "authelia"
requires = ["db-postgres"]

[[dirs]]
path = "${APPCONFIG_STORAGE}/authelia/config"
mode = "0750"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/JWT_SECRET"
generate = { bytes = 32, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/STORAGE_PASSWORD"
value_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
mode = "0600"

[[config_files]]
from = "config/configuration.yml"
to   = "${APPCONFIG_STORAGE}/authelia/config/configuration.yml"
on_exists = "keep"

[[config_files]]
to = "${APPCONFIG_STORAGE}/authelia/config/users.yml"
create_empty = true

[[prompts]]
key = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
label = "Authelia database password"
secret = true
generate = { bytes = 24, encoding = "base64" }

[[provision.postgres]]
stack = "db-postgres"
database = "authelia"
user = "authelia"
password_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"

[env_validators]
HTTP_PORT = { type = "port" }
POSTGRES_PASSWORD = { type = "non_empty", not_one_of = ["REPLACE_THIS"] }
`,
			requires: []string{"db-postgres"},
		},
		{
			name: "unknown key names the key and line, rest still decodes",
			src: `[app]
requires = ["db-postgres"]

[[dirs]]
path = "/x"
recursive_chown = true
`,
			requires: []string{"db-postgres"},
			issues: []homelab.SetupIssue{{
				Key: "dirs.recursive_chown", Line: 6,
				Message: "setup.toml has unknown key dirs.recursive_chown, which is not a registered primitive",
			}},
		},
		{
			name: "unknown top-level table",
			src:  "[hooks]\npost = \"rm -rf /\"\n",
			issues: []homelab.SetupIssue{{
				Key: "hooks", Line: 1,
				Message: "setup.toml has unknown key hooks, which is not a registered primitive",
			}},
		},
		{
			name: "unknown validator type",
			src:  "[env_validators]\nX = { type = \"email\" }\n",
			issues: []homelab.SetupIssue{{
				Key: "env_validators.X.type", Line: 2,
				Message: `setup.toml [env_validators] X has unknown type "email"`,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, issues, err := homelab.ParseSetup([]byte(tt.src))
			if err != nil {
				t.Fatalf("ParseSetup() error = %v", err)
			}
			if diff := cmp.Diff(tt.issues, issues); diff != "" {
				t.Errorf("ParseSetup() issues mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.requires, f.App.Requires); diff != "" {
				t.Errorf("ParseSetup() requires mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseSetupSyntaxError(t *testing.T) {
	_, issues, err := homelab.ParseSetup([]byte("[app]\nname = \"x\"\n[[dirs]\npath = \"/a\"\n"))
	var pe *homelab.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("ParseSetup(bad TOML) error = %v, want *homelab.ParseError", err)
	}
	if pe.Line != 3 || len(issues) != 0 {
		t.Errorf("ParseSetup(bad TOML) = line %d, issues %+v; want line 3 and no issues", pe.Line, issues)
	}
}

func TestSetupDeclaredPaths(t *testing.T) {
	f, _, _ := homelab.ParseSetup([]byte(`[[dirs]]
path = "/a"
[[secrets]]
file = "/b"
[[config_files]]
to = "/c"
`))
	if diff := cmp.Diff([]string{"/a", "/b", "/c"}, f.DeclaredPaths()); diff != "" {
		t.Errorf("DeclaredPaths() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseSetupTypeErrorIsSchema(t *testing.T) {
	src := `[app]
name = "x"
requires = "db-postgres"

[[dirs]]
path = "/a"
bogus = 1

[env_validators]
PORT = { type = "port" }
`
	f, issues, err := homelab.ParseSetup([]byte(src))
	if err != nil {
		t.Fatalf("ParseSetup() error = %v, want issues only (valid TOML)", err)
	}
	want := []homelab.SetupIssue{
		{Key: "app.requires", Line: 3, Message: "setup.toml key app.requires has the wrong type for the primitive registry"},
		{Key: "dirs.bogus", Line: 7, Message: "setup.toml has unknown key dirs.bogus, which is not a registered primitive"},
	}
	if diff := cmp.Diff(want, issues); diff != "" {
		t.Errorf("issues mismatch (-want +got):\n%s", diff)
	}
	if len(f.Dirs) != 1 || f.Dirs[0].Path != "/a" || f.EnvValidators["PORT"].Type != "port" || f.App.Name != "x" {
		t.Errorf("the other tables were not decoded: %+v", f)
	}
}
