package homelab_test

import (
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// apply runs ApplySetup and collects its report lines.
func apply(t *testing.T, w *setupWorld) (homelab.SetupResult, []string, error) {
	t.Helper()
	var lines []string
	res, err := homelab.ApplySetup(t.Context(), w.in, w.deps(), func(l string) { lines = append(lines, l) })
	return res, lines, err
}

// assertNoValue fails when any of texts contains any of values.
func assertNoValue(t *testing.T, values, texts []string) {
	t.Helper()
	for _, v := range values {
		for _, s := range texts {
			if v != "" && strings.Contains(s, v) {
				t.Errorf("%q contains a secret value", s)
			}
		}
	}
}

func TestApplySetupTwice(t *testing.T) {
	w := newAuthelia(t)
	res, lines, err := apply(t, w)
	if err != nil {
		t.Fatalf("first ApplySetup() error = %v", err)
	}
	if res.Created != 11 || res.Kept != 0 || res.Overwritten != 0 || res.Unchanged != 0 {
		t.Errorf("first run = %s, want 11 created", res.Summary())
	}

	// Host end state: modes as declared, lengths as declared, contents as declared.
	wantModes := map[string]homelab.FileMode{
		"/mnt/appconfig/authelia":                                0o750,
		"/mnt/appconfig/authelia/config":                         0o750,
		"/mnt/appconfig/authelia/secrets":                        0o700,
		"/mnt/appconfig/authelia/secrets/JWT_SECRET":             0o600,
		"/mnt/appconfig/authelia/secrets/SESSION_SECRET":         0o600,
		"/mnt/appconfig/authelia/secrets/STORAGE_ENCRYPTION_KEY": 0o600,
		"/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD":       0o600,
		"/mnt/appconfig/authelia/config/configuration.yml":       0o640,
		"/mnt/appconfig/authelia/config/users.yml":               0o640,
	}
	gotModes := map[string]homelab.FileMode{}
	for p, n := range w.host.nodes {
		gotModes[p] = n.mode
	}
	if diff := cmp.Diff(wantModes, gotModes); diff != "" {
		t.Errorf("host modes mismatch (-want +got):\n%s", diff)
	}
	data := func(rel string) string { return string(w.host.nodes["/mnt/appconfig/authelia/"+rel].data) }
	for rel, n := range map[string]int{"secrets/JWT_SECRET": 32, "secrets/SESSION_SECRET": 64, "secrets/STORAGE_ENCRYPTION_KEY": 64} {
		v := data(rel)
		if len(v) != base64.StdEncoding.EncodedLen(n) {
			t.Errorf("%s has %d characters, want %d", rel, len(v), base64.StdEncoding.EncodedLen(n))
		}
		if b, err := base64.StdEncoding.DecodeString(v); err != nil || len(b) != n {
			t.Errorf("%s does not decode to %d bytes: %v", rel, n, err)
		}
	}
	if data("secrets/JWT_SECRET") == data("secrets/SESSION_SECRET")[:44] {
		t.Error("two generated secrets share a prefix: not random")
	}
	if data("config/configuration.yml") != autheliaConfig || data("config/users.yml") != "" {
		t.Errorf("config files = %q, %q", data("config/configuration.yml"), data("config/users.yml"))
	}

	// The generated prompt value is the same in .env and in the value_from secret.
	env := envValues(t, w.store.files["authelia"])
	pw := env[pgKey]
	if len(pw) != base64.StdEncoding.EncodedLen(24) {
		t.Errorf(".env %s has %d characters, want 32", pgKey, len(pw))
	}
	if got := data("secrets/STORAGE_PASSWORD"); got != pw {
		t.Error("STORAGE_PASSWORD differs from the generated .env value")
	}
	wantEnv := strings.Replace(autheliaTemplate, "REPLACE_THIS", pw, 1)
	if got := string(w.store.files["authelia"]); got != wantEnv {
		t.Errorf(".env = %q, want the template with only the prompt key replaced", got)
	}

	for _, want := range []string{
		"create env file /srv/homelab/compose/authelia/.env mode 0600 (copy of compose/authelia/example.env)",
		"set env AUTHELIA_STORAGE_POSTGRES_PASSWORD (generated 24 bytes base64)",
		"create dir /mnt/appconfig/authelia mode 0750 (parent of /mnt/appconfig/authelia/config)",
		"create dir /mnt/appconfig/authelia/secrets mode 0700",
		"create secret /mnt/appconfig/authelia/secrets/JWT_SECRET mode 0600 (generated 32 bytes base64)",
		"create secret /mnt/appconfig/authelia/secrets/STORAGE_PASSWORD mode 0600 (from prompt AUTHELIA_STORAGE_POSTGRES_PASSWORD)",
		"11 created, 0 kept, 0 overwritten, 0 unchanged",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("report lacks %q; got:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	secretValues := []string{pw, data("secrets/JWT_SECRET"), data("secrets/SESSION_SECRET"), data("secrets/STORAGE_ENCRYPTION_KEY")}
	assertNoValue(t, secretValues, lines)

	// Second run: nothing changes.
	hostWrites, storeWrites := len(w.host.writes), w.store.writes
	res2, lines2, err := apply(t, w)
	if err != nil {
		t.Fatalf("second ApplySetup() error = %v", err)
	}
	if res2.Created != 0 || res2.Overwritten != 0 {
		t.Errorf("second run = %s, want 0 created and 0 overwritten", res2.Summary())
	}
	for _, s := range res2.Steps {
		if s.Outcome != homelab.OutcomeKept && s.Outcome != homelab.OutcomeUnchanged {
			t.Errorf("second run step %s %s outcome = %s, want kept or unchanged", s.Kind, s.Path, s.Outcome)
		}
	}
	if len(w.host.writes) != hostWrites || w.store.writes != storeWrites {
		t.Errorf("second run wrote: host %v, store %d more", w.host.writes[hostWrites:], w.store.writes-storeWrites)
	}
	for _, want := range []string{
		"keep secret /mnt/appconfig/authelia/secrets/JWT_SECRET (already present; never overwritten)",
		"keep env AUTHELIA_STORAGE_POSTGRES_PASSWORD (already set)",
		"0 created, 10 kept, 0 overwritten, 0 unchanged",
	} {
		if !slices.Contains(lines2, want) {
			t.Errorf("second report lacks %q; got:\n%s", want, strings.Join(lines2, "\n"))
		}
	}
	assertNoValue(t, secretValues, lines2)
}

func TestApplySetupNeverOverwritesSecret(t *testing.T) {
	const jwt = "/mnt/appconfig/authelia/secrets/JWT_SECRET"
	t.Run("present before planning", func(t *testing.T) {
		w := newAuthelia(t)
		w.host.file(jwt, "hand-edited", 0o400)
		res, _, err := apply(t, w)
		if err != nil {
			t.Fatalf("ApplySetup() error = %v", err)
		}
		if n := w.host.nodes[jwt]; string(n.data) != "hand-edited" || n.mode != 0o400 {
			t.Errorf("JWT_SECRET = %q mode %s, want untouched", n.data, n.mode)
		}
		if slices.ContainsFunc(w.host.writes, func(s string) bool { return strings.HasSuffix(s, jwt) }) {
			t.Errorf("JWT_SECRET was written: %v", w.host.writes)
		}
		i := slices.IndexFunc(res.Steps, func(s homelab.SetupStepResult) bool { return s.Path == jwt })
		if res.Steps[i].Outcome != homelab.OutcomeKept {
			t.Errorf("JWT_SECRET outcome = %s, want kept", res.Steps[i].Outcome)
		}
	})
	t.Run("appears between planning and writing", func(t *testing.T) {
		w := newAuthelia(t)
		w.host.beforeWrite = func(_, p string) {
			if p == jwt {
				w.host.file(jwt, "raced", 0o600)
			}
		}
		res, lines, err := apply(t, w)
		if err != nil {
			t.Fatalf("ApplySetup() error = %v", err)
		}
		if got := string(w.host.nodes[jwt].data); got != "raced" {
			t.Errorf("JWT_SECRET = %q, want the raced content untouched", got)
		}
		i := slices.IndexFunc(res.Steps, func(s homelab.SetupStepResult) bool { return s.Path == jwt })
		if res.Steps[i].Outcome != homelab.OutcomeKept {
			t.Errorf("JWT_SECRET outcome = %s, want kept", res.Steps[i].Outcome)
		}
		want := "keep secret " + jwt + " (already present; appeared since planning)"
		if !slices.Contains(lines, want) {
			t.Errorf("report lacks %q", want)
		}
	})
}

func TestApplySetupAborts(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(w *setupWorld)
		wantErr error
		wantMsg string
	}{
		{
			name:    "requires not Ready",
			modify:  func(w *setupWorld) { w.in.Statuses["db-postgres"] = homelab.StatusNeedsSetup },
			wantErr: homelab.ErrValidation,
			wantMsg: "required stack db-postgres is NeedsSetup, not Ready",
		},
		{
			name:    "escaping path",
			modify:  func(w *setupWorld) { w.in.Setup.Dirs[0].Path = "${APPCONFIG_STORAGE}/../../etc" },
			wantErr: homelab.ErrValidation,
			wantMsg: "contains a .. segment",
		},
		{
			name:    "unresolved variable",
			modify:  func(w *setupWorld) { w.in.Setup.Secrets[0].File = "${APPDATA_STORAGE}/x" },
			wantErr: homelab.ErrValidation,
			wantMsg: "${APPDATA_STORAGE}",
		},
		{
			name:    "env file tracked, seen by the planner",
			modify:  func(w *setupWorld) { w.repo.tracked["compose/authelia/.env"] = true },
			wantErr: homelab.ErrValidation,
			wantMsg: "GIT001",
		},
		{
			name:    "env file tracked, refused by the store",
			modify:  func(w *setupWorld) { w.store.tracked["authelia"] = true },
			wantErr: homelab.ErrTracked,
		},
		{
			name: "config file awaiting a decision",
			modify: func(w *setupWorld) {
				w.in.Setup.ConfigFiles[0].OnExists = "prompt"
				w.host.file("/mnt/appconfig/authelia/config/configuration.yml", "local edit", 0o640)
			},
			wantErr: homelab.ErrValidation,
			wantMsg: "needs an overwrite decision for /mnt/appconfig/authelia/config/configuration.yml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAuthelia(t)
			tt.modify(w)
			before := len(w.host.writes)
			var lines []string
			_, err := homelab.ApplySetup(t.Context(), w.in, w.deps(), func(l string) { lines = append(lines, l) })
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ApplySetup() error = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("ApplySetup() error = %q, want it to mention %q", err, tt.wantMsg)
			}
			if len(w.host.writes) != before || w.store.writes != 0 {
				t.Errorf("ApplySetup() wrote: host %v, store %d", w.host.writes[before:], w.store.writes)
			}
			if len(lines) != 0 {
				t.Errorf("report = %q, want nothing", lines)
			}
		})
	}
}

func TestApplySetupAnsweredValue(t *testing.T) {
	const pw = `p@ss word #1 "$x"`
	w := newAuthelia(t)
	w.in.Answers = map[string]homelab.PromptAnswer{pgKey: {Value: homelab.NewSecret(pw), HasValue: true}}
	res, lines, err := apply(t, w)
	if err != nil {
		t.Fatalf("ApplySetup() error = %v", err)
	}
	if got := envValues(t, w.store.files["authelia"])[pgKey]; got != pw {
		t.Error(".env does not hold the answered value")
	}
	if got := string(w.host.nodes["/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD"].data); got != pw {
		t.Error("STORAGE_PASSWORD does not hold the answered value")
	}
	if !slices.Contains(lines, "set env AUTHELIA_STORAGE_POSTGRES_PASSWORD (from answer)") {
		t.Errorf("report = %q", lines)
	}
	assertNoValue(t, []string{pw, "p@ss"}, lines)
	for _, s := range res.Steps {
		assertNoValue(t, []string{pw, "p@ss"}, []string{s.Reason, s.Path})
	}

	// Answering the same value again changes nothing.
	w.store.files["authelia"] = append(w.store.files["authelia"], []byte("# local note\n")...)
	res, _, err = apply(t, w)
	if err != nil || res.Created != 0 || res.Overwritten != 0 {
		t.Fatalf("same answer again = %s, %v; want no changes", res.Summary(), err)
	}

	// A new value would leave STORAGE_PASSWORD, which is never overwritten, stale: refused.
	const storagePW = "/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD"
	w.in.Answers[pgKey] = homelab.PromptAnswer{Value: homelab.NewSecret("rotated"), HasValue: true}
	writes, envBefore := len(w.host.writes), string(w.store.files["authelia"])
	if _, _, err = apply(t, w); !errors.Is(err, homelab.ErrValidation) || !strings.Contains(err.Error(), storagePW+" already exists") {
		t.Fatalf("new answer with the secret file present: error = %v, want a refusal naming %s", err, storagePW)
	}
	if len(w.host.writes) != writes || string(w.store.files["authelia"]) != envBefore {
		t.Fatal("a refused run wrote something")
	}

	// With the file removed (regeneration is a deliberate act), the new value reaches both.
	delete(w.host.nodes, storagePW)
	res, _, err = apply(t, w)
	if err != nil || res.Overwritten != 1 || res.Created != 1 {
		t.Fatalf("new answer = %s, %v; want the env key overwritten and the secret created", res.Summary(), err)
	}
	got := string(w.store.files["authelia"])
	if want := "# Authelia\nSERVICE_NAME=authelia\n" + pgKey + "=rotated\n# local note\n"; got != want {
		t.Errorf(".env = %q, want %q", got, want)
	}
	if got := string(w.host.nodes[storagePW].data); got != "rotated" {
		t.Error("STORAGE_PASSWORD does not hold the new value")
	}
}

func TestApplySetupOverwriteDecision(t *testing.T) {
	const target = "/mnt/appconfig/authelia/config/configuration.yml"
	w := newAuthelia(t)
	w.in.Setup.ConfigFiles[0].OnExists = "prompt"
	w.host.file(target, "local edit", 0o644)
	w.in.Overwrite = map[string]bool{target: true}
	res, lines, err := apply(t, w)
	if err != nil {
		t.Fatalf("ApplySetup() error = %v", err)
	}
	if n := w.host.nodes[target]; string(n.data) != autheliaConfig || n.mode != 0o640 {
		t.Errorf("%s = %q mode %s, want the template with mode 0640", target, n.data, n.mode)
	}
	if res.Overwritten != 1 {
		t.Errorf("result = %s, want 1 overwritten", res.Summary())
	}
	want := "overwrite config " + target + " mode 0640 (differs from copy of compose/authelia/config/configuration.yml; overwrite chosen)"
	if !slices.Contains(lines, want) {
		t.Errorf("report lacks %q; got %q", want, lines)
	}
	res, _, err = apply(t, w)
	if err != nil || res.Overwritten != 0 || res.Unchanged != 1 {
		t.Errorf("second run = %s, %v; want the config unchanged", res.Summary(), err)
	}
}

func TestApplySetupEncodings(t *testing.T) {
	w := newAuthelia(t)
	w.in.Setup = parseSetup(t, `[[secrets]]
file = "${DATABASE_STORAGE}/hex"
generate = { bytes = 16, encoding = "hex" }
[[secrets]]
file = "${DATABASE_STORAGE}/alnum"
generate = { bytes = 40, encoding = "alphanumeric" }
mode = "0400"
[[secrets]]
file = "${DATABASE_STORAGE}/default"
generate = { bytes = 1 }
`)
	delete(w.repo.files, "compose/authelia/example.env")
	if _, _, err := apply(t, w); err != nil {
		t.Fatalf("ApplySetup() error = %v", err)
	}
	tests := []struct {
		path string
		re   string
		mode homelab.FileMode
	}{
		{"/mnt/db/hex", `^[0-9a-f]{32}$`, 0o600},
		{"/mnt/db/alnum", `^[A-Za-z0-9]{40}$`, 0o400},
		{"/mnt/db/default", `^[A-Za-z0-9+/]{2}==$`, 0o600},
	}
	for _, tt := range tests {
		n := w.host.nodes[tt.path]
		if n == nil {
			t.Fatalf("%s was not created", tt.path)
		}
		if !regexp.MustCompile(tt.re).Match(n.data) || n.mode != tt.mode {
			t.Errorf("%s = %d bytes mode %s, want %s mode %s", tt.path, len(n.data), n.mode, tt.re, tt.mode)
		}
	}
	if w.store.writes != 0 {
		t.Errorf("env file written %d times, want 0: no prompts and no template", w.store.writes)
	}
}

func TestApplySetupHostFailure(t *testing.T) {
	w := newAuthelia(t)
	w.host.beforeWrite = func(_, p string) {
		if p == "/mnt/appconfig/authelia/secrets" {
			// a symlink swapped in after planning
			w.host.refuse["/mnt/appconfig/authelia/secrets/JWT_SECRET"] = true
		}
	}
	res, lines, err := apply(t, w)
	if !errors.Is(err, homelab.ErrValidation) {
		t.Fatalf("ApplySetup() error = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "secret /mnt/appconfig/authelia/secrets/JWT_SECRET") {
		t.Errorf("error %q does not name the failed step", err)
	}
	if res.Created != 5 { // env file, env key, three dirs
		t.Errorf("partial result = %s, want 5 created", res.Summary())
	}
	if last := lines[len(lines)-1]; last != "stopped: 5 created, 0 kept, 0 overwritten, 0 unchanged" {
		t.Errorf("last report line = %q", last)
	}
}

func TestApplySetupIgnoresTemplateValues(t *testing.T) {
	const committed = "committed-default"
	w := newAuthelia(t)
	w.repo.files["compose/authelia/example.env"] = []byte(pgKey + "=" + committed + "\n")
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil {
		t.Fatalf("PlanSetup() error = %v", err)
	}
	if got := plan.Prompts[0].State; got != homelab.PromptMissing {
		t.Errorf("prompt state = %s, want missing: a template value is not an existing secret", got)
	}
	if _, _, err := apply(t, w); err != nil {
		t.Fatalf("ApplySetup() error = %v", err)
	}
	pw := envValues(t, w.store.files["authelia"])[pgKey]
	if pw == committed || len(pw) != base64.StdEncoding.EncodedLen(24) {
		t.Errorf(".env %s is the committed template value or not generated", pgKey)
	}
	if got := string(w.host.nodes["/mnt/appconfig/authelia/secrets/STORAGE_PASSWORD"].data); got != pw {
		t.Error("STORAGE_PASSWORD differs from the generated .env value")
	}
}

func TestApplySetupSymlinkAppearsBeforeWrite(t *testing.T) {
	const jwt = "/mnt/appconfig/authelia/secrets/JWT_SECRET"
	w := newAuthelia(t)
	w.host.beforeWrite = func(_, p string) {
		if p == jwt {
			w.host.symlink(jwt) // dangling: the secret would look present forever
		}
	}
	res, lines, err := apply(t, w)
	if err == nil || !strings.Contains(err.Error(), "secret "+jwt) {
		t.Fatalf("ApplySetup() error = %v, want a failure naming %s", err, jwt)
	}
	if slices.ContainsFunc(res.Steps, func(s homelab.SetupStepResult) bool { return s.Path == jwt }) {
		t.Errorf("JWT_SECRET recorded as an outcome: %+v", res.Steps)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "stopped: ") {
		t.Errorf("last report line = %q, want the stopped summary", lines[len(lines)-1])
	}
}

func TestApplySetupDigest(t *testing.T) {
	t.Run("unchanged plan runs", func(t *testing.T) {
		w := newAuthelia(t)
		plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
		if err != nil {
			t.Fatal(err)
		}
		w.in.ExpectDigest = plan.Digest
		if _, _, err := apply(t, w); err != nil {
			t.Fatalf("ApplySetup(same digest) error = %v", err)
		}
	})
	t.Run("changed state is refused with nothing written", func(t *testing.T) {
		w := newAuthelia(t)
		plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
		if err != nil {
			t.Fatal(err)
		}
		w.host.file("/mnt/appconfig/authelia/secrets/JWT_SECRET", "appeared", 0o600)
		w.in.ExpectDigest = plan.Digest
		before := len(w.host.writes)
		_, lines, err := apply(t, w)
		if !errors.Is(err, homelab.ErrConflict) || !strings.Contains(err.Error(), "the plan changed since it was shown") {
			t.Fatalf("ApplySetup(stale digest) error = %v, want ErrConflict", err)
		}
		if len(w.host.writes) != before || w.store.writes != 0 || len(lines) != 0 {
			t.Errorf("a refused run wrote: host %v, store %d, report %q", w.host.writes[before:], w.store.writes, lines)
		}
	})
	t.Run("a decision does not change the digest", func(t *testing.T) {
		const target = "/mnt/appconfig/authelia/config/configuration.yml"
		w := newAuthelia(t)
		w.in.Setup.ConfigFiles[0].OnExists = "prompt"
		w.host.file(target, "local edit", 0o640)
		shown, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
		if err != nil || len(shown.Decisions) != 1 {
			t.Fatalf("PlanSetup() = %v decisions, %v", shown.Decisions, err)
		}
		w.in.Overwrite = map[string]bool{target: false}
		decided, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
		if err != nil || decided.Digest != shown.Digest {
			t.Fatalf("digest after a decision = %q, want %q (%v)", decided.Digest, shown.Digest, err)
		}
		w.in.ExpectDigest = shown.Digest
		if _, _, err := apply(t, w); err != nil {
			t.Fatalf("ApplySetup(decided) error = %v", err)
		}
		if got := string(w.host.nodes[target].data); got != "local edit" {
			t.Errorf("%s = %q, want kept", target, got)
		}
	})
}

func TestPlanSetupDeferred(t *testing.T) {
	w := newAuthelia(t)
	w.in.Setup = parseSetup(t, autheliaSetup+`
[[provision.postgres]]
stack = "db-postgres"
database = "authelia"
user = "authelia"
password_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
`)
	plan, err := homelab.PlanSetup(t.Context(), w.in, w.deps())
	if err != nil || !plan.Runnable {
		t.Fatalf("PlanSetup() = %q, %v; deferred work must not block", plan.Problems, err)
	}
	want := []string{"provision.postgres db-postgres database authelia: not run until M5"}
	if diff := cmp.Diff(want, plan.Deferred); diff != "" {
		t.Errorf("Deferred mismatch (-want +got):\n%s", diff)
	}
}
