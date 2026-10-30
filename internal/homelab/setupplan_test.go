package homelab_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// autheliaSetup is the full example of docs/architecture/setup-toml.md without
// [[provision.postgres]], which M3 does not run.
const autheliaSetup = `[app]
name = "authelia"
requires = ["db-postgres"]          # stacks that must be Ready before setup can run

[[dirs]]
path = "${APPCONFIG_STORAGE}/authelia/config"
mode = "0750"

[[dirs]]
path = "${APPCONFIG_STORAGE}/authelia/secrets"
mode = "0700"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/JWT_SECRET"
generate = { bytes = 32, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/SESSION_SECRET"
generate = { bytes = 64, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/STORAGE_ENCRYPTION_KEY"
generate = { bytes = 64, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/STORAGE_PASSWORD"
value_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"   # same value the DB user is created with
mode = "0600"

[[config_files]]
from = "config/configuration.yml"                    # path within the app dir, committed
to   = "${APPCONFIG_STORAGE}/authelia/config/configuration.yml"
on_exists = "keep"                                   # keep | overwrite | prompt

[[config_files]]
to = "${APPCONFIG_STORAGE}/authelia/config/users.yml"
create_empty = true
on_exists = "keep"

[[prompts]]
key = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
label = "Authelia database password"
secret = true
generate = { bytes = 24, encoding = "base64" }       # offer a generated default
`

const (
	autheliaTemplate = "# Authelia\nSERVICE_NAME=authelia\nAUTHELIA_STORAGE_POSTGRES_PASSWORD=REPLACE_THIS\n"
	autheliaConfig   = "theme: dark\n"
	pgKey            = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
)

// setupWorld is one app's setup input with fake adapters.
type setupWorld struct {
	in    homelab.SetupInput
	repo  *fakeRepo
	host  *fakeHost
	store *fakeStore
}

func (w *setupWorld) deps() homelab.SetupDeps {
	return homelab.SetupDeps{Repo: w.repo, Host: w.host, Secrets: w.store}
}

// deployEnv builds a sorted EnvScope from key, value pairs.
func deployEnv(kv ...string) homelab.EnvScope {
	var s homelab.EnvScope
	for i := 0; i < len(kv); i += 2 {
		s.Vars = append(s.Vars, homelab.EnvVar{Key: kv[i], Value: homelab.NewSecret(kv[i+1]), Source: "compose/.global.env"})
	}
	slices.SortFunc(s.Vars, func(a, b homelab.EnvVar) int { return strings.Compare(a.Key, b.Key) })
	return s
}

func parseSetup(t *testing.T, src string) homelab.SetupFile {
	t.Helper()
	f, issues, err := homelab.ParseSetup([]byte(src))
	if err != nil || len(issues) > 0 {
		t.Fatalf("ParseSetup() = %v, %v; want no issues", issues, err)
	}
	return f
}

// newAuthelia is a fresh authelia: no env file, empty storage roots, db-postgres Ready.
func newAuthelia(t *testing.T) *setupWorld {
	t.Helper()
	return &setupWorld{
		in: homelab.SetupInput{
			App:   "authelia",
			Setup: parseSetup(t, autheliaSetup),
			Deploy: deployEnv("APPCONFIG_STORAGE", "/mnt/appconfig", "DATABASE_STORAGE", "/mnt/db",
				"HOMELAB_STORAGE", "/srv/homelab"),
			Statuses: map[string]homelab.Status{"db-postgres": homelab.StatusReady, "authelia": homelab.StatusNeedsSetup},
		},
		repo: &fakeRepo{root: "/srv/homelab", tracked: map[string]bool{}, files: map[string][]byte{
			"compose/authelia/example.env":              []byte(autheliaTemplate),
			"compose/authelia/config/configuration.yml": []byte(autheliaConfig),
		}},
		host:  newFakeHost("/mnt/appconfig", "/mnt/db"),
		store: newFakeStore(),
	}
}

func envStep(kind homelab.SetupStepKind, key string, mode homelab.FileMode, action homelab.SetupAction, reason string) homelab.SetupStep {
	return homelab.SetupStep{
		Kind: kind, RootKind: homelab.RootApp, Root: "/srv/homelab", Rel: "compose/authelia/.env",
		Path: "/srv/homelab/compose/authelia/.env", Key: key, Mode: mode, Action: action, Reason: reason,
	}
}

func acStep(kind homelab.SetupStepKind, rel string, mode homelab.FileMode, action homelab.SetupAction, reason string) homelab.SetupStep {
	return homelab.SetupStep{
		Kind: kind, RootKind: homelab.RootAppConfig, Root: "/mnt/appconfig", Rel: rel,
		Path: "/mnt/appconfig/" + rel, Mode: mode, Action: action, Reason: reason,
	}
}

func TestPlanSetupAuthelia(t *testing.T) {
	w := newAuthelia(t)
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil {
		t.Fatalf("PlanSetup() error = %v", err)
	}
	envFile := envStep(homelab.StepEnvFile, "", 0o600, homelab.ActionCreate, "copy of compose/authelia/example.env")
	envFile.Source = "compose/authelia/example.env"
	config := acStep(homelab.StepConfigFile, "authelia/config/configuration.yml", 0o640, homelab.ActionCreate,
		"copy of compose/authelia/config/configuration.yml")
	config.Source = "compose/authelia/config/configuration.yml"
	want := homelab.SetupPlan{
		App:      "authelia",
		Runnable: true,
		Requires: []homelab.RequireState{{Stack: "db-postgres", Status: homelab.StatusReady, OK: true}},
		Prompts: []homelab.PromptState{{
			Key: pgKey, Label: "Authelia database password", Secret: true, CanGenerate: true, State: homelab.PromptMissing,
		}},
		Steps: []homelab.SetupStep{
			envFile,
			envStep(homelab.StepEnvKey, pgKey, 0, homelab.ActionSet, "generated 24 bytes base64"),
			acStep(homelab.StepDir, "authelia", 0o750, homelab.ActionCreate, "parent of /mnt/appconfig/authelia/config"),
			acStep(homelab.StepDir, "authelia/config", 0o750, homelab.ActionCreate, "declared"),
			acStep(homelab.StepDir, "authelia/secrets", 0o700, homelab.ActionCreate, "declared"),
			acStep(homelab.StepSecret, "authelia/secrets/JWT_SECRET", 0o600, homelab.ActionCreate, "generated 32 bytes base64"),
			acStep(homelab.StepSecret, "authelia/secrets/SESSION_SECRET", 0o600, homelab.ActionCreate, "generated 64 bytes base64"),
			acStep(homelab.StepSecret, "authelia/secrets/STORAGE_ENCRYPTION_KEY", 0o600, homelab.ActionCreate, "generated 64 bytes base64"),
			acStep(homelab.StepSecret, "authelia/secrets/STORAGE_PASSWORD", 0o600, homelab.ActionCreate, "from prompt "+pgKey),
			config,
			acStep(homelab.StepConfigFile, "authelia/config/users.yml", 0o640, homelab.ActionCreate, "an empty file"),
		},
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(plan.Digest) {
		t.Errorf("PlanSetup().Digest = %q, want 64 hex digits", plan.Digest)
	}
	if diff := cmp.Diff(want, plan, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(homelab.SetupPlan{}, "Digest")); diff != "" {
		t.Errorf("PlanSetup() mismatch (-want +got):\n%s", diff)
	}
	if len(w.host.writes) != 0 || w.store.writes != 0 {
		t.Errorf("PlanSetup() wrote: host %v, store %d", w.host.writes, w.store.writes)
	}
}

func TestPlanSetupProblems(t *testing.T) {
	tests := []struct {
		name   string
		modify func(w *setupWorld)
		want   string
		// zeroSteps means the plan must hold no steps at all.
		zeroSteps bool
	}{
		{
			name:   "requires not Ready",
			modify: func(w *setupWorld) { w.in.Statuses["db-postgres"] = homelab.StatusBroken },
			want:   "required stack db-postgres is Broken, not Ready",
		},
		{
			name:   "requires missing",
			modify: func(w *setupWorld) { delete(w.in.Statuses, "db-postgres") },
			want:   "required stack db-postgres does not exist",
		},
		{
			name:      "unresolved variable",
			modify:    func(w *setupWorld) { w.in.Setup.Dirs[1].Path = "${APPDATA_STORAGE}/authelia/secrets" },
			want:      "setup.toml uses ${APPDATA_STORAGE}, which the deploy env does not define",
			zeroSteps: true,
		},
		{
			name:      "invalid template",
			modify:    func(w *setupWorld) { w.in.Setup.Dirs[0].Path = "${APPCONFIG STORAGE}/x" },
			want:      "setup.toml [[dirs]] #1 path has an invalid ${...} template",
			zeroSteps: true,
		},
		{
			name:   "dot-dot escape",
			modify: func(w *setupWorld) { w.in.Setup.Secrets[0].File = "${APPCONFIG_STORAGE}/../../etc/passwd" },
			want:   "setup.toml [[secrets]] #1 file /mnt/appconfig/../../etc/passwd contains a .. segment",
		},
		{
			name:   "dot-dot that stays inside the root is still refused",
			modify: func(w *setupWorld) { w.in.Setup.Dirs[0].Path = "${APPCONFIG_STORAGE}/x/../authelia/config" },
			want:   "setup.toml [[dirs]] #1 path /mnt/appconfig/x/../authelia/config contains a .. segment",
		},
		{
			name: "root undefined",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("DATABASE_STORAGE", "/mnt/db", "HOMELAB_STORAGE", "/srv/homelab")
			},
			want:      "setup.toml uses ${APPCONFIG_STORAGE}, which the deploy env does not define",
			zeroSteps: true,
		},
		{
			name: "root relative",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("APPCONFIG_STORAGE", "mnt/appconfig", "HOMELAB_STORAGE", "/srv/homelab")
			},
			want: "APPCONFIG_STORAGE must be an absolute directory other than /",
		},
		{
			name: "root is /",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("APPCONFIG_STORAGE", "/", "HOMELAB_STORAGE", "/srv/homelab")
			},
			want: "APPCONFIG_STORAGE must be an absolute directory other than /",
		},
		{
			name:   "root missing on the host",
			modify: func(w *setupWorld) { delete(w.host.roots, "/mnt/appconfig") },
			want:   "APPCONFIG_STORAGE /mnt/appconfig is not an existing directory",
		},
		{
			name:   "path inside the homelab repo",
			modify: func(w *setupWorld) { w.in.Setup.Dirs[0].Path = "${HOMELAB_STORAGE}/compose/authelia/config" },
			want:   "setup.toml [[dirs]] #1 path /srv/homelab/compose/authelia/config is inside the homelab repo, which setup writes only through the env file",
		},
		{
			name:   "path under no root",
			modify: func(w *setupWorld) { w.in.Setup.ConfigFiles[1].To = "/etc/authelia/users.yml" },
			want:   "setup.toml [[config_files]] #2 to /etc/authelia/users.yml is not under APPCONFIG_STORAGE or DATABASE_STORAGE",
		},
		{
			name:   "path is the root itself",
			modify: func(w *setupWorld) { w.in.Setup.Dirs[0].Path = "${APPCONFIG_STORAGE}/" },
			want:   "setup.toml [[dirs]] #1 path /mnt/appconfig is a storage root itself; setup never creates or changes a root",
		},
		{
			name:   "symlink escaping the root",
			modify: func(w *setupWorld) { w.host.refuse["/mnt/appconfig/authelia"] = true },
			want:   "/mnt/appconfig/authelia cannot be used: authelia escapes the storage root: validation failed",
		},
		{
			name:   "parent exists as a file",
			modify: func(w *setupWorld) { w.host.file("/mnt/appconfig/authelia", "x", 0o644) },
			want:   "/mnt/appconfig/authelia exists and is not a directory, but /mnt/appconfig/authelia/config needs it as one",
		},
		{
			name:   "secret path is a directory",
			modify: func(w *setupWorld) { w.host.file("/mnt/appconfig/authelia/secrets/JWT_SECRET/x", "", 0o600) },
			want:   "/mnt/appconfig/authelia/secrets/JWT_SECRET is a directory, but [[secrets]] #1 declares it as a file",
		},
		{
			name:   "same path declared twice",
			modify: func(w *setupWorld) { w.in.Setup.Secrets[1].File = w.in.Setup.Secrets[0].File },
			want:   "/mnt/appconfig/authelia/secrets/JWT_SECRET is declared more than once in setup.toml",
		},
		{
			name:   "prompt with no answer, no value and no generate",
			modify: func(w *setupWorld) { w.in.Setup.Prompts[0].Generate = nil },
			want:   "prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD (Authelia database password) needs an answer: compose/authelia/.env has no value for it and it offers no generated value",
		},
		{
			name: "value_from names nothing",
			modify: func(w *setupWorld) {
				w.store.files["authelia"] = []byte(autheliaTemplate)
				w.in.Setup.Secrets[3].ValueFrom = "NOWHERE"
			},
			want: "[[secrets]] #4 value_from NOWHERE names neither a prompt nor a key with a value in compose/authelia/.env",
		},
		{
			name: "value_from names a placeholder in the env file",
			modify: func(w *setupWorld) {
				w.store.files["authelia"] = []byte(autheliaTemplate)
				w.in.Setup.Prompts = nil
			},
			want: "[[secrets]] #4 value_from AUTHELIA_STORAGE_POSTGRES_PASSWORD names neither a prompt nor a key with a value in compose/authelia/.env",
		},
		{
			name:   "config template missing from the repo",
			modify: func(w *setupWorld) { delete(w.repo.files, "compose/authelia/config/configuration.yml") },
			want:   "[[config_files]] #1 from compose/authelia/config/configuration.yml is not a readable file in the repo",
		},
		{
			name:   "config from interpolates outside the app dir",
			modify: func(w *setupWorld) { w.in.Setup.ConfigFiles[0].From = "${APPCONFIG_STORAGE}/x" },
			want:   "setup.toml [[config_files]] #1 from ${APPCONFIG_STORAGE}/x is not a relative path inside the app directory",
		},
		{
			name: "config from interpolates to an env file",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("APPCONFIG_STORAGE", "/mnt/appconfig", "HOMELAB_STORAGE", "/srv/homelab", "ENVNAME", ".env")
				w.in.Setup.ConfigFiles[0].From = "${ENVNAME}"
			},
			want: "setup.toml [[config_files]] #1 from ${ENVNAME} is an env file; secrets reach the host only through [[secrets]]",
		},
		{
			name: "value_from from a committed template value",
			modify: func(w *setupWorld) {
				w.repo.files["compose/authelia/example.env"] = []byte("DB_PW=committed-default\n")
				w.in.Setup.Prompts = nil
				w.in.Setup.Secrets[3].ValueFrom = "DB_PW"
			},
			want: "[[secrets]] #4 value_from DB_PW cannot be satisfied: compose/authelia/.env does not exist, " +
				"and a value in example.env is committed and never used as a secret",
		},
		{
			name: "path variable from the app env file",
			modify: func(w *setupWorld) {
				env := deployEnv("APPCONFIG_STORAGE", "/mnt/appconfig", "HOMELAB_STORAGE", "/srv/homelab")
				// Appended last, which keeps Vars sorted (S after H).
				env.Vars = append(env.Vars, homelab.EnvVar{Key: "SECRET_DIR", Value: homelab.NewSecret("hunter2"), Source: "compose/authelia/.env"})
				w.in.Deploy = env
				w.in.Setup.Secrets[0].File = "${SECRET_DIR}"
			},
			want: "setup.toml [[secrets]] #1 file uses ${SECRET_DIR}, which comes from compose/authelia/.env; " +
				"setup.toml may use only variables from compose/.global.env or injected by the orchestrator",
			zeroSteps: true,
		},
		{
			name: "path variable from a compose.yaml default",
			modify: func(w *setupWorld) {
				w.in.Deploy.Vars[0].Source = homelab.SourceDefault // APPCONFIG_STORAGE
			},
			want: "setup.toml [[dirs]] #1 path uses ${APPCONFIG_STORAGE}, which comes from default; " +
				"setup.toml may use only variables from compose/.global.env or injected by the orchestrator",
			zeroSteps: true,
		},
		{
			name: "resolved value that is not a path is quoted as its template",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("APPCONFIG_STORAGE", "/mnt/appconfig", "HOMELAB_STORAGE", "/srv/homelab", "WORD", "hunter2")
				w.in.Setup.Secrets[0].File = "${WORD}"
			},
			want: "setup.toml [[secrets]] #1 file ${WORD} is not an absolute path",
		},
		{
			name:   "symlink at a secret path",
			modify: func(w *setupWorld) { w.host.symlink("/mnt/appconfig/authelia/secrets/JWT_SECRET") },
			want:   "/mnt/appconfig/authelia/secrets/JWT_SECRET is a symbolic link; setup does not write through links",
		},
		{
			name:   "symlink at a parent directory",
			modify: func(w *setupWorld) { w.host.symlink("/mnt/appconfig/authelia") },
			want:   "/mnt/appconfig/authelia is a symbolic link; setup does not write through links",
		},
		{
			name:   "env file not gitignored",
			modify: func(w *setupWorld) { w.repo.notIgnored = map[string]bool{"compose/authelia/.env": true} },
			want:   "compose/authelia/.env is not gitignored and is never written (GIT001): add compose/*/.env to .gitignore",
		},
		{
			name: "whitespace-only answer",
			modify: func(w *setupWorld) {
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Value: homelab.NewSecret("   "), HasValue: true}}
			},
			want: "the answer for prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD is empty",
		},
		{
			name: "generate over an existing value",
			modify: func(w *setupWorld) {
				w.store.files["authelia"] = []byte(pgKey + "=old\n")
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Generate: true}}
			},
			want: "prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD already has a value; unset it in the env editor to generate a new one",
		},
		{
			name:   "relative repo root",
			modify: func(w *setupWorld) { w.repo.root = "srv/homelab" },
			want:   "the homelab repo path must be absolute",
		},
		{
			name: "relative HOMELAB_STORAGE",
			modify: func(w *setupWorld) {
				w.in.Deploy = deployEnv("APPCONFIG_STORAGE", "/mnt/appconfig", "HOMELAB_STORAGE", "homelab")
			},
			want: "HOMELAB_STORAGE must be an absolute path",
		},
		{
			name:   "env file tracked by git",
			modify: func(w *setupWorld) { w.repo.tracked["compose/authelia/.env"] = true },
			want:   "compose/authelia/.env is tracked by git and is never written (GIT001): gitignore it and git rm --cached it",
		},
		{
			name:   "env file unparseable",
			modify: func(w *setupWorld) { w.store.files["authelia"] = []byte("A=1\nB=\"open\n") },
			want:   "compose/authelia/.env is not parseable: unterminated quoted value starting on line 2",
		},
		{
			name:      "schema issue",
			modify:    func(w *setupWorld) { w.in.Setup.Dirs[0].Mode = "999" },
			want:      "setup.toml [[dirs]] #1 mode must be an octal permission mode such as 0750 (3 or 4 digits, no setuid, setgid or sticky bit)",
			zeroSteps: true,
		},
		{
			name:   "app name mismatch",
			modify: func(w *setupWorld) { w.in.Setup.App.Name = "other" },
			want:   `setup.toml [app] name does not equal the app directory name "authelia"`,
		},
		{
			name: "answer for an unknown key",
			modify: func(w *setupWorld) {
				w.in.Answers = map[string]homelab.PromptAnswer{"OTHER": {Value: homelab.NewSecret("x"), HasValue: true}}
			},
			want: "an answer was given for OTHER, which is not a [[prompts]] key",
		},
		{
			name: "answer failing its validator",
			modify: func(w *setupWorld) {
				w.in.Setup.EnvValidators = map[string]homelab.ValueValidatorSpec{pgKey: {Type: "port"}}
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Value: homelab.NewSecret("hunter2"), HasValue: true}}
			},
			want: `the answer for prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD is not a port number (1-65535) (setup.toml type "port")`,
		},
		{
			name: "placeholder answer",
			modify: func(w *setupWorld) {
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Value: homelab.NewSecret("CHANGEME"), HasValue: true}}
			},
			want: "the answer for prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD is a placeholder",
		},
		{
			name: "env value changes under an existing value_from secret",
			modify: func(w *setupWorld) {
				w.store.files["authelia"] = []byte(pgKey + "=old\n")
				w.host.file("/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD", "old", 0o600)
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Value: homelab.NewSecret("new"), HasValue: true}}
			},
			want: "[[secrets]] #4 /mnt/appconfig/authelia/secrets/STORAGE_PASSWORD already exists and is never overwritten, " +
				"so it would no longer match the new value of AUTHELIA_STORAGE_POSTGRES_PASSWORD; " +
				"answer AUTHELIA_STORAGE_POSTGRES_PASSWORD with the value the file holds, or remove the file to regenerate it",
		},
		{
			name: "env value generated while an existing value_from secret is kept",
			modify: func(w *setupWorld) {
				w.host.file("/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD", "old", 0o600)
			},
			want: "[[secrets]] #4 /mnt/appconfig/authelia/secrets/STORAGE_PASSWORD already exists and is never overwritten, " +
				"so it would no longer match the new value of AUTHELIA_STORAGE_POSTGRES_PASSWORD; " +
				"answer AUTHELIA_STORAGE_POSTGRES_PASSWORD with the value the file holds, or remove the file to regenerate it",
		},
		{
			name: "generate requested where none is offered",
			modify: func(w *setupWorld) {
				w.in.Setup.Prompts[0].Generate = nil
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Generate: true}}
			},
			want: "prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD offers no generated value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAuthelia(t)
			tt.modify(w)
			plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
			if err != nil {
				t.Fatalf("PlanSetup() error = %v", err)
			}
			if plan.Runnable {
				t.Errorf("PlanSetup().Runnable = true, want false")
			}
			if !slices.Contains(plan.Problems, tt.want) {
				t.Errorf("PlanSetup().Problems = %q, want it to contain %q", plan.Problems, tt.want)
			}
			if tt.zeroSteps && len(plan.Steps) != 0 {
				t.Errorf("PlanSetup().Steps = %+v, want none", plan.Steps)
			}
			for _, p := range plan.Problems {
				if strings.Contains(p, "hunter2") || strings.Contains(p, "CHANGEME") {
					t.Errorf("problem %q contains a value", p)
				}
			}
		})
	}
}

func TestPlanSetupInvalidApp(t *testing.T) {
	w := newAuthelia(t)
	w.in.App = "../etc"
	if _, err := homelab.PlanSetup(t.Context(), w.in, w.deps()); !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("PlanSetup(bad app) error = %v, want ErrValidation", err)
	}
}

func TestPlanSetupPrompts(t *testing.T) {
	tests := []struct {
		name    string
		env     string // existing env file; "" for none
		answer  *homelab.PromptAnswer
		state   homelab.PromptStatus
		action  homelab.SetupAction
		reason  string
		secretR string // reason of the STORAGE_PASSWORD step
	}{
		{
			name: "no answer, placeholder in template: generated", state: homelab.PromptMissing,
			action: homelab.ActionSet, reason: "generated 24 bytes base64", secretR: "from prompt " + pgKey,
		},
		{
			name: "value answered", answer: &homelab.PromptAnswer{Value: homelab.NewSecret("s3cret"), HasValue: true},
			state: homelab.PromptAnswered, action: homelab.ActionSet, reason: "from answer", secretR: "from prompt " + pgKey,
		},
		{
			name: "generate answered", answer: &homelab.PromptAnswer{Generate: true},
			state: homelab.PromptAnswered, action: homelab.ActionSet, reason: "generated 24 bytes base64", secretR: "from prompt " + pgKey,
		},
		{
			name: "already set in env file", env: pgKey + "=existing\n",
			state: homelab.PromptSet, action: homelab.ActionKeep, reason: "already set", secretR: "from prompt " + pgKey,
		},
		{
			name: "answer equal to the current value", env: pgKey + "=existing\n",
			answer: &homelab.PromptAnswer{Value: homelab.NewSecret("existing"), HasValue: true},
			state:  homelab.PromptAnswered, action: homelab.ActionKeep, reason: "answer equals the current value",
			secretR: "from prompt " + pgKey,
		},
		{
			name: "empty answer is no answer", env: pgKey + "=existing\n", answer: &homelab.PromptAnswer{},
			state: homelab.PromptSet, action: homelab.ActionKeep, reason: "already set", secretR: "from prompt " + pgKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAuthelia(t)
			if tt.env != "" {
				w.store.files["authelia"] = []byte(tt.env)
			}
			if tt.answer != nil {
				w.in.Answers = map[string]homelab.PromptAnswer{pgKey: *tt.answer}
			}
			plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
			if err != nil {
				t.Fatalf("PlanSetup() error = %v", err)
			}
			if !plan.Runnable {
				t.Fatalf("PlanSetup() problems = %q", plan.Problems)
			}
			if got := plan.Prompts[0].State; got != tt.state {
				t.Errorf("prompt state = %s, want %s", got, tt.state)
			}
			got := map[homelab.SetupStepKind]homelab.SetupStep{}
			for _, s := range plan.Steps {
				if s.Kind == homelab.StepEnvKey || (s.Kind == homelab.StepSecret && strings.HasSuffix(s.Rel, "STORAGE_PASSWORD")) {
					got[s.Kind] = s
				}
			}
			if k := got[homelab.StepEnvKey]; k.Action != tt.action || k.Reason != tt.reason {
				t.Errorf("env key step = %s %q, want %s %q", k.Action, k.Reason, tt.action, tt.reason)
			}
			if s := got[homelab.StepSecret]; s.Reason != tt.secretR {
				t.Errorf("STORAGE_PASSWORD reason = %q, want %q", s.Reason, tt.secretR)
			}
		})
	}
}

func TestPlanSetupValueFromEnv(t *testing.T) {
	w := newAuthelia(t)
	w.in.Setup.Prompts = nil
	w.store.files["authelia"] = []byte(pgKey + "=from-env\n")
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil || !plan.Runnable {
		t.Fatalf("PlanSetup() = %q, %v", plan.Problems, err)
	}
	for _, s := range plan.Steps {
		if s.Kind == homelab.StepEnvFile || s.Kind == homelab.StepEnvKey {
			t.Errorf("unexpected env step %+v: no prompts and an existing env file", s)
		}
		if s.Kind == homelab.StepSecret && strings.HasSuffix(s.Rel, "STORAGE_PASSWORD") && s.Reason != "from env "+pgKey {
			t.Errorf("STORAGE_PASSWORD reason = %q, want from env", s.Reason)
		}
	}
}

func TestPlanSetupOnExists(t *testing.T) {
	const target = "/mnt/appconfig/authelia/config/configuration.yml"
	yes, no := true, false
	tests := []struct {
		name     string
		onExists string
		existing string // "" for no file
		decision *bool
		action   homelab.SetupAction
	}{
		{name: "absent", onExists: "keep", action: homelab.ActionCreate},
		{name: "keep, differs", onExists: "keep", existing: "changed\n", action: homelab.ActionKeep},
		{name: "default is keep", onExists: "", existing: "changed\n", action: homelab.ActionKeep},
		{name: "overwrite, differs", onExists: "overwrite", existing: "changed\n", action: homelab.ActionOverwrite},
		{name: "overwrite, identical", onExists: "overwrite", existing: autheliaConfig, action: homelab.ActionUnchanged},
		{name: "prompt, identical", onExists: "prompt", existing: autheliaConfig, action: homelab.ActionUnchanged},
		{name: "prompt, differs, no decision", onExists: "prompt", existing: "changed\n", action: homelab.ActionAsk},
		{name: "prompt, differs, overwrite", onExists: "prompt", existing: "changed\n", decision: &yes, action: homelab.ActionOverwrite},
		{name: "prompt, differs, keep", onExists: "prompt", existing: "changed\n", decision: &no, action: homelab.ActionKeep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAuthelia(t)
			w.in.Setup.ConfigFiles[0].OnExists = tt.onExists
			if tt.existing != "" {
				w.host.file(target, tt.existing, 0o640)
			}
			if tt.decision != nil {
				w.in.Overwrite = map[string]bool{target: *tt.decision}
			}
			plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
			if err != nil {
				t.Fatalf("PlanSetup() error = %v", err)
			}
			i := slices.IndexFunc(plan.Steps, func(s homelab.SetupStep) bool { return s.Path == target })
			if i < 0 {
				t.Fatalf("no step for %s in %+v (problems %q)", target, plan.Steps, plan.Problems)
			}
			if got := plan.Steps[i].Action; got != tt.action {
				t.Errorf("action = %s, want %s (reason %q)", got, tt.action, plan.Steps[i].Reason)
			}
			wantDecisions := []string(nil)
			if tt.action == homelab.ActionAsk {
				wantDecisions = []string{target}
			}
			if diff := cmp.Diff(wantDecisions, plan.Decisions); diff != "" {
				t.Errorf("Decisions mismatch (-want +got):\n%s", diff)
			}
			if !plan.Runnable {
				t.Errorf("Runnable = false, problems %q; a pending decision is not a problem", plan.Problems)
			}
		})
	}
}

func TestPlanSetupDeclaredParentAfterChild(t *testing.T) {
	w := newAuthelia(t)
	w.in.Setup = parseSetup(t, `[[dirs]]
path = "${DATABASE_STORAGE}/authelia/data/pg"
[[dirs]]
path = "${DATABASE_STORAGE}/authelia"
mode = "0700"
`)
	w.host.file("/mnt/db/other/x", "", 0o600)
	delete(w.repo.files, "compose/authelia/example.env") // no env file to create from it
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil || !plan.Runnable {
		t.Fatalf("PlanSetup() = %q, %v", plan.Problems, err)
	}
	db := func(rel string, mode homelab.FileMode, reason string) homelab.SetupStep {
		return homelab.SetupStep{
			Kind: homelab.StepDir, RootKind: homelab.RootDatabase, Root: "/mnt/db", Rel: rel, Path: "/mnt/db/" + rel,
			Mode: mode, Action: homelab.ActionCreate, Reason: reason,
		}
	}
	want := []homelab.SetupStep{
		db("authelia", 0o700, "declared"),
		db("authelia/data", 0o750, "parent of /mnt/db/authelia/data/pg"),
		db("authelia/data/pg", 0o750, "declared"),
	}
	if diff := cmp.Diff(want, plan.Steps); diff != "" {
		t.Errorf("Steps mismatch (-want +got):\n%s", diff)
	}
}

func TestPlanSetupExistingState(t *testing.T) {
	w := newAuthelia(t)
	w.store.files["authelia"] = []byte(pgKey + "=set\n")
	w.host.file("/mnt/appconfig/authelia/secrets/JWT_SECRET", "old", 0o600)
	w.host.nodes["/mnt/appconfig/authelia/secrets"].mode = 0o755
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil || !plan.Runnable {
		t.Fatalf("PlanSetup() = %q, %v", plan.Problems, err)
	}
	got := map[string]string{}
	for _, s := range plan.Steps {
		got[s.Path+" "+s.Key] = string(s.Action) + ": " + s.Reason
	}
	want := map[string]string{
		"/srv/homelab/compose/authelia/.env ":                     "keep: already present",
		"/srv/homelab/compose/authelia/.env " + pgKey:             "keep: already set",
		"/mnt/appconfig/authelia/config ":                         "create: declared",
		"/mnt/appconfig/authelia/secrets ":                        "keep: already present with mode 0755; setup does not change it",
		"/mnt/appconfig/authelia/secrets/JWT_SECRET ":             "keep: already present; never overwritten",
		"/mnt/appconfig/authelia/secrets/SESSION_SECRET ":         "create: generated 64 bytes base64",
		"/mnt/appconfig/authelia/secrets/STORAGE_ENCRYPTION_KEY ": "create: generated 64 bytes base64",
		"/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD ":       "create: from prompt " + pgKey,
		"/mnt/appconfig/authelia/config/configuration.yml ":       "create: copy of compose/authelia/config/configuration.yml",
		"/mnt/appconfig/authelia/config/users.yml ":               "create: an empty file",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("steps mismatch (-want +got):\n%s", diff)
	}
}
