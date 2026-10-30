package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// withStorage points the fixture repo's storage roots at fresh temp directories: the fixture's
// compose/.global.env names /mnt/... paths, and the materialised copy's .global.env is
// gitignored, so rewriting it leaves the repo clean. It returns the storage directory, which
// holds appconfigs/ and databases/.
func withStorage(t *testing.T, opts *envOptions, extra func(t *testing.T, root string)) string {
	t.Helper()
	storage := t.TempDir()
	for _, d := range []string{"appconfigs", "databases"} {
		if err := os.Mkdir(filepath.Join(storage, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts.prepareRepo = func(t *testing.T, root string) {
		t.Helper()
		p := filepath.Join(root, "compose", ".global.env")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "APPCONFIG_STORAGE="):
				lines[i] = "APPCONFIG_STORAGE=" + filepath.Join(storage, "appconfigs")
			case strings.HasPrefix(l, "DATABASE_STORAGE="):
				lines[i] = "DATABASE_STORAGE=" + filepath.Join(storage, "databases")
			}
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		if extra != nil {
			extra(t, root)
		}
	}
	return storage
}

// hostPathExists is the PATH001 check against the real filesystem.
func hostPathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// treeOf lists every path under dir, relative to it.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	out := []string{}
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(dir, p); rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *testEnv) stackStatus(t *testing.T, name string, c cred) string {
	t.Helper()
	var d StackDetail
	decode(t, e.do(t, http.MethodGet, "/api/stacks/"+name, nil, c), &d)
	return d.Status
}

func (e *testEnv) plan(t *testing.T, name string, body any, c cred) SetupPlanView {
	t.Helper()
	r := e.do(t, http.MethodPost, "/api/stacks/"+name+"/setup/plan", body, c)
	if r.status != http.StatusOK {
		t.Fatalf("POST plan %s status = %d, want 200; body %s", name, r.status, r.body)
	}
	var v SetupPlanView
	decode(t, r, &v)
	return v
}

// runSetup plans, starts a setup job with that plan's digest and waits for it.
func (e *testEnv) runSetup(t *testing.T, name string, c cred) JobView {
	t.Helper()
	p := e.plan(t, name, map[string]any{}, c)
	r := e.do(t, http.MethodPost, "/api/stacks/"+name+"/setup/run", map[string]any{"plan_digest": p.Digest}, c)
	if r.status != http.StatusAccepted {
		t.Fatalf("POST run %s status = %d, want 202; body %s", name, r.status, r.body)
	}
	var started JobStarted
	decode(t, r, &started)
	return e.waitJob(t, started.JobID, c)
}

func TestSetupOkControlReachesReady(t *testing.T) {
	opts := envOptions{hostPathExists: hostPathExists}
	storage := withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))
	appconfigs := filepath.Join(storage, "appconfigs")
	html := filepath.Join(appconfigs, "ok-control", "html")

	if got := env.stackStatus(t, "ok-control", cookie); got != "NeedsSetup" {
		t.Fatalf("ok-control status before setup = %s, want NeedsSetup (PATH001 on the html dir)", got)
	}

	got := env.plan(t, "ok-control", map[string]any{}, cookie)
	want := SetupPlanView{
		Declared: true, Digest: got.Digest, Runnable: true, Problems: []string{}, Decisions: []string{}, Deferred: []string{},
		Requires: []RequireView{}, Prompts: []PromptView{},
		Steps: []SetupStepView{
			{
				Kind: "dir", RootKind: "appconfig", Path: filepath.Join(appconfigs, "ok-control"), Mode: "0750",
				Action: "create", Reason: "parent of " + html,
			},
			{Kind: "dir", RootKind: "appconfig", Path: html, Mode: "0755", Action: "create", Reason: "declared"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" || got.Digest == "" {
		t.Errorf("first plan digest %q; mismatch (-want +got):\n%s", got.Digest, diff)
	}
	if tree := treeOf(t, appconfigs); len(tree) != 0 {
		t.Fatalf("planning wrote %v", tree)
	}

	j := env.runSetup(t, "ok-control", cookie)
	if j.State != "succeeded" || j.Kind != jobKindSetupRun {
		t.Fatalf("setup job = %+v, want a succeeded %s job", j, jobKindSetupRun)
	}
	log := strings.Join(j.Log, "\n")
	for _, s := range []string{"create dir " + html + " mode 0755", "2 created, 0 kept, 0 overwritten, 0 unchanged"} {
		if !strings.Contains(log, s) {
			t.Errorf("job log lacks %q:\n%s", s, log)
		}
	}
	fi, err := os.Stat(html)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("html dir after setup = %v, %v, want a directory with mode 0755", fi, err)
	}
	if got := env.stackStatus(t, "ok-control", cookie); got != "Ready" {
		t.Errorf("ok-control status after setup = %s, want Ready", got)
	}

	// A second plan and run change nothing.
	got = env.plan(t, "ok-control", map[string]any{}, cookie)
	wantSteps := []SetupStepView{
		{Kind: "dir", RootKind: "appconfig", Path: html, Mode: "0755", Action: "keep", Reason: "already present"},
	}
	if diff := cmp.Diff(wantSteps, got.Steps); diff != "" || !got.Runnable {
		t.Errorf("second plan runnable %v, steps mismatch (-want +got):\n%s", got.Runnable, diff)
	}
	j = env.runSetup(t, "ok-control", cookie)
	if log := strings.Join(j.Log, "\n"); j.State != "succeeded" || !strings.Contains(log, "0 created, 1 kept, 0 overwritten, 0 unchanged") {
		t.Errorf("second setup job = %s, log:\n%s\nwant succeeded with 0 created", j.State, log)
	}

	entries, _ := env.auditEntries(t, cookie)
	wantActions := []string{auditSetupRun, auditSetupRun, auditDirCreate, auditDirCreate}
	if diff := cmp.Diff(wantActions, actions(entries)); diff != "" {
		t.Fatalf("audit actions mismatch (-want +got):\n%s", diff)
	}
	first := entries[1]
	wantDetails := map[string]string{
		"job_id": first.Details["job_id"], "state": "succeeded",
		"created": "2", "kept": "0", "overwritten": "0", "unchanged": "0",
	}
	if diff := cmp.Diff(wantDetails, first.Details); diff != "" || first.Details["job_id"] == "" || first.Target != "ok-control" || first.User != testUser {
		t.Errorf("first setup.run entry %+v; details mismatch (-want +got):\n%s", first, diff)
	}
	if entries[2].Target != html || entries[2].Details["mode"] != "0755" {
		t.Errorf("dir.create entry = %+v, want target %s with mode 0755", entries[2], html)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, `"msg":"setup finished","stack":"ok-control","job_id":"`) {
		t.Errorf("logs lack the setup finished line:\n%s", logs)
	}
}

func TestSetupRefusesWhenRequiredStackNotReady(t *testing.T) {
	opts := envOptions{}
	storage := withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))

	const problem = "required stack db-postgres is Broken, not Ready"
	got := env.plan(t, "authelia", map[string]any{}, cookie)
	if got.Runnable || !got.Declared || !contains(got.Problems, problem) {
		t.Errorf("authelia plan runnable %v declared %v problems %q, want not runnable with %q", got.Runnable, got.Declared, got.Problems, problem)
	}
	if diff := cmp.Diff([]RequireView{{Stack: "db-postgres", Status: "Broken"}}, got.Requires); diff != "" {
		t.Errorf("requires mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"provision.postgres db-postgres database authelia: not run until M5"}, got.Deferred); diff != "" {
		t.Errorf("deferred mismatch (-want +got):\n%s", diff)
	}
	wantPrompt := PromptView{
		Key: "AUTHELIA_STORAGE_POSTGRES_PASSWORD", Label: "Authelia database password",
		Secret: true, CanGenerate: true, State: "missing",
	}
	if diff := cmp.Diff([]PromptView{wantPrompt}, got.Prompts); diff != "" {
		t.Errorf("prompts mismatch (-want +got):\n%s", diff)
	}

	r := env.do(t, http.MethodPost, "/api/stacks/authelia/setup/run", map[string]any{"plan_digest": got.Digest}, cookie)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, problem) {
		t.Errorf("POST run authelia = %d %s, want 422 naming %q", r.status, r.body, problem)
	}
	if tree := treeOf(t, storage); !cmp.Equal(tree, []string{"appconfigs", "databases"}) {
		t.Errorf("storage after a refused run = %v, want only the empty roots", tree)
	}
	if _, err := os.Stat(filepath.Join(env.repo.Root(), "compose", "authelia", ".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("authelia .env after a refused run: %v, want absent", err)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestSetupRejectsPathEscape(t *testing.T) {
	opts := envOptions{}
	storage := withStorage(t, &opts, func(t *testing.T, root string) {
		t.Helper()
		toml := "[[dirs]]\npath = \"${APPCONFIG_STORAGE}/../../x\"\nmode = \"0755\"\n"
		if err := os.WriteFile(filepath.Join(root, "compose", "ok-control", "setup.toml"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))

	got := env.plan(t, "ok-control", map[string]any{}, cookie)
	if got.Runnable || len(got.Steps) != 0 || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "contains a .. segment") {
		t.Errorf("escaping plan = %+v, want not runnable, no steps, one .. problem", got)
	}
	r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", map[string]any{"plan_digest": got.Digest}, cookie)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "contains a .. segment") {
		t.Errorf("POST run = %d %s, want 422 naming the .. segment", r.status, r.body)
	}
	if tree := treeOf(t, storage); !cmp.Equal(tree, []string{"appconfigs", "databases"}) {
		t.Errorf("storage after a refused run = %v, want only the empty roots", tree)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(storage), "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("escaped path exists: %v", err)
	}
}

func TestSetupEdgeCases(t *testing.T) {
	opts := envOptions{}
	withStorage(t, &opts, func(t *testing.T, root string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "compose", "db-client", "setup.toml"), []byte("[app]\nbogus = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))

	// No setup.toml: nothing declared, and nothing to run.
	if got := env.plan(t, "db-redis", map[string]any{}, cookie); !cmp.Equal(got, emptySetupPlanView()) {
		t.Errorf("plan without setup.toml = %+v, want the empty plan", got)
	}
	if r := env.do(t, http.MethodPost, "/api/stacks/db-redis/setup/run", map[string]any{}, cookie); r.status != http.StatusUnprocessableEntity {
		t.Errorf("run without setup.toml status = %d, want 422", r.status)
	}
	// A setup.toml with SETUP002 issues.
	for _, op := range []string{"plan", "run"} {
		r := env.do(t, http.MethodPost, "/api/stacks/db-client/setup/"+op, map[string]any{}, cookie)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, msgSetupInvalid) {
			t.Errorf("%s with a broken setup.toml = %d %s, want 422 %q", op, r.status, r.body, msgSetupInvalid)
		}
	}
	if r := env.do(t, http.MethodPost, "/api/stacks/no-such-stack/setup/plan", map[string]any{}, cookie); r.status != http.StatusNotFound {
		t.Errorf("plan for an unknown stack status = %d, want 404", r.status)
	}
	// An answer for a key that is not a prompt is a problem, never echoed with its value.
	body := map[string]any{"answers": map[string]any{"NOT_A_PROMPT": map[string]any{"value": knownSecret}}}
	r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/plan", body, cookie)
	if r.status != http.StatusOK || strings.Contains(r.body, knownSecret) || !strings.Contains(r.body, "NOT_A_PROMPT, which is not a [[prompts]] key") {
		t.Errorf("plan with a stray answer = %d %s, want 200 naming the key and not the value", r.status, r.body)
	}
}

func TestSetupAndEnvNeverLeakValues(t *testing.T) {
	opts := envOptions{}
	withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))

	body := map[string]any{"set": map[string]string{"EXTRA_SECRET": knownSecret}}
	if r := env.do(t, http.MethodPut, "/api/stacks/ok-control/env", body, cookie); r.status != http.StatusOK {
		t.Fatalf("PUT env status = %d; body %s", r.status, r.body)
	}
	j := env.runSetup(t, "ok-control", cookie)
	if j.State != "succeeded" {
		t.Fatalf("setup job = %+v, want succeeded", j)
	}
	r := env.do(t, http.MethodGet, "/api/jobs/"+j.ID, nil, cookie)
	_, auditBody := env.auditEntries(t, cookie)
	for name, s := range map[string]string{"server log": env.logs.String(), "job": r.body, "audit log": auditBody} {
		if strings.Contains(s, knownSecret) {
			t.Errorf("%s contains the value:\n%s", name, s)
		}
	}
}

// readyPostgres makes db-postgres Ready in the materialised repo: its compose.yaml uses a bare
// string for networks (COMPOSE001) and its .env a placeholder (ENV009).
func readyPostgres(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "compose", "db-postgres")
	b, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(b), "networks: databases", "networks:\n      - databases", 1)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SERVICE_NAME=db-postgres\nPOSTGRES_PASSWORD=not-a-placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupAutheliaEndToEnd(t *testing.T) {
	opts := envOptions{}
	storage := withStorage(t, &opts, readyPostgres)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))
	if got := env.stackStatus(t, "db-postgres", cookie); got != "Ready" {
		t.Fatalf("db-postgres status = %s, want Ready", got)
	}
	secrets := filepath.Join(storage, "appconfigs", "authelia", "secrets")

	if p := env.plan(t, "authelia", map[string]any{}, cookie); !p.Runnable || len(p.Decisions) != 0 {
		t.Fatalf("authelia plan = %+v, want runnable", p)
	}
	j := env.runSetup(t, "authelia", cookie)
	if j.State != "succeeded" {
		t.Fatalf("authelia setup job = %+v, want succeeded", j)
	}
	if fi, err := os.Stat(secrets); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir = %v, %v, want mode 0700", fi, err)
	}
	values := map[string]string{}
	for _, name := range []string{"JWT_SECRET", "SESSION_SECRET", "STORAGE_ENCRYPTION_KEY", "STORAGE_PASSWORD"} {
		p := filepath.Join(secrets, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read secret %s: %v", name, err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("secret %s mode = %v, want 0600", name, fi.Mode().Perm())
		}
		if len(b) < 16 {
			t.Errorf("secret %s is %d bytes, want a generated value", name, len(b))
		}
		values[name] = string(b)
	}
	var revealed EnvFileView
	decode(t, env.do(t, http.MethodGet, "/api/stacks/authelia/env?reveal=true", nil, cookie), &revealed)
	for _, e := range revealed.Entries {
		if e.Key == "AUTHELIA_STORAGE_POSTGRES_PASSWORD" && e.Value != values["STORAGE_PASSWORD"] {
			t.Errorf("the env file's database password differs from the STORAGE_PASSWORD secret file")
		}
	}

	again := env.runSetup(t, "authelia", cookie)
	if log := strings.Join(again.Log, "\n"); !strings.Contains(log, "0 created,") || !strings.Contains(log, "0 overwritten, ") {
		t.Errorf("second authelia run log:\n%s\nwant 0 created and 0 overwritten", log)
	}
	for name, v := range values {
		if b, _ := os.ReadFile(filepath.Join(secrets, name)); string(b) != v {
			t.Errorf("secret %s changed on the second run", name)
		}
	}

	entries, auditBody := env.auditEntries(t, cookie)
	n := 0
	for _, e := range entries {
		if e.Action == auditSecretWrite {
			n++
		}
	}
	if n != 4 {
		t.Errorf("secret.write entries = %d, want 4 (first run only)", n)
	}
	e, ok := findAudit(entries, auditEnvWrite)
	wantEnv := map[string]string{"job_id": j.ID, "keys_set": "AUTHELIA_STORAGE_POSTGRES_PASSWORD", "keys_unset": "", "created": "true"}
	if !ok || !cmp.Equal(wantEnv, e.Details) || e.Target != "compose/authelia/.env" {
		t.Errorf("env.write entry = %+v, %v, want details %v", e, ok, wantEnv)
	}

	jobBodies := env.do(t, http.MethodGet, "/api/jobs/"+j.ID, nil, cookie).body + env.do(t, http.MethodGet, "/api/jobs/"+again.ID, nil, cookie).body
	for name, v := range values {
		for where, s := range map[string]string{"server log": env.logs.String(), "job output": jobBodies, "audit log": auditBody} {
			if strings.Contains(s, v) {
				t.Errorf("%s contains the value of %s", where, name)
			}
		}
	}
}

func TestSetupRunNeedsTheReviewedPlan(t *testing.T) {
	opts := envOptions{}
	storage := withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))

	r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", map[string]any{}, cookie)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, `"location":"body.plan_digest"`) {
		t.Errorf("run without plan_digest = %d %s, want 422 at body.plan_digest", r.status, r.body)
	}

	p := env.plan(t, "ok-control", map[string]any{}, cookie)
	// The host changes between review and run: the html dir's parent appears.
	if err := os.Mkdir(filepath.Join(storage, "appconfigs", "ok-control"), 0o750); err != nil {
		t.Fatal(err)
	}
	r = env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", map[string]any{"plan_digest": p.Digest}, cookie)
	if r.status != http.StatusConflict || !strings.Contains(r.body, msgPlanChanged) {
		t.Errorf("run with a stale digest = %d %s, want 409 %q", r.status, r.body, msgPlanChanged)
	}
	if tree := treeOf(t, filepath.Join(storage, "appconfigs")); !cmp.Equal(tree, []string{"ok-control"}) {
		t.Errorf("appconfigs after a refused run = %v, want only the hand-made dir", tree)
	}
	if entries, _ := env.auditEntries(t, cookie); len(entries) != 0 {
		t.Errorf("refused runs left audit entries: %+v", entries)
	}
}

func TestSetupJobQueueing(t *testing.T) {
	block := make(chan struct{})
	opts := envOptions{fetch: func(ctx context.Context, _ func(string)) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil
	}}
	withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))
	root := env.repo.Root()

	var fetch JobStarted
	decode(t, env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie), &fetch)
	p := env.plan(t, "ok-control", map[string]any{}, cookie)
	body := map[string]any{"plan_digest": p.Digest}

	r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", body, cookie)
	if r.status != http.StatusAccepted {
		t.Fatalf("first run status = %d, want 202; body %s", r.status, r.body)
	}
	var run JobStarted
	decode(t, r, &run)
	if r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", body, cookie); r.status != http.StatusConflict ||
		!strings.Contains(r.body, "a setup job is already running") {
		t.Errorf("second run while one is queued = %d %s, want 409", r.status, r.body)
	}

	// The fetch ahead of the queued run changes setup.toml: the run must act on the new file,
	// whose plan no longer matches the digest the user reviewed.
	toml := "[[dirs]]\npath = \"${APPCONFIG_STORAGE}/ok-control/html\"\nmode = \"0700\"\n"
	if err := os.WriteFile(filepath.Join(root, "compose", "ok-control", "setup.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	close(block)
	env.waitJob(t, fetch.JobID, cookie)
	j := env.waitJob(t, run.JobID, cookie)
	if j.State != "failed" || j.Error == nil || !strings.Contains(*j.Error, msgPlanChanged) {
		t.Errorf("queued run after setup.toml changed = %s %v, want failed with %q", j.State, j.Error, msgPlanChanged)
	}
	if !strings.Contains(env.logs.String(), `"level":"WARN","msg":"setup failed"`) {
		t.Errorf("logs lack a WARN setup failed line:\n%s", env.logs.String())
	}
}

func TestSetupRunWhileShuttingDown(t *testing.T) {
	opts := envOptions{}
	withStorage(t, &opts, nil)
	env := newEnv(t, opts)
	env.setup(t)
	cookie := withCookie(env.login(t))
	p := env.plan(t, "ok-control", map[string]any{}, cookie)
	env.stopJobs()
	r := env.do(t, http.MethodPost, "/api/stacks/ok-control/setup/run", map[string]any{"plan_digest": p.Digest}, cookie)
	if r.status != http.StatusServiceUnavailable {
		t.Errorf("run while shutting down = %d %s, want 503", r.status, r.body)
	}
}

func TestSetupRefusesEnvGitWouldExpose(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string)
		problem string
	}{
		{
			name: "not ignored",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("compose/.global.env\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			problem: "compose/authelia/.env is not gitignored and is never written (GIT001)",
		},
		{
			name: "tracked",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				p := filepath.Join(root, "compose", "authelia", ".env")
				if err := os.WriteFile(p, []byte("SERVICE_NAME=authelia\nHTTP_PORT=3080\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"add", "-f", "compose/authelia/.env"}, {"commit", "-q", "-m", "track authelia env"}} {
					cmd := exec.CommandContext(t.Context(), "git", args...)
					cmd.Dir = root
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git %v: %v\n%s", args, err, out)
					}
				}
			},
			problem: "compose/authelia/.env is tracked by git and is never written (GIT001)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := envOptions{}
			storage := withStorage(t, &opts, func(t *testing.T, root string) {
				t.Helper()
				readyPostgres(t, root)
				tt.prepare(t, root)
			})
			env := newEnv(t, opts)
			env.setup(t)
			cookie := withCookie(env.login(t))

			p := env.plan(t, "authelia", map[string]any{}, cookie)
			if p.Runnable || !slices.ContainsFunc(p.Problems, func(s string) bool { return strings.HasPrefix(s, tt.problem) }) {
				t.Errorf("plan runnable %v problems %q, want a problem starting %q", p.Runnable, p.Problems, tt.problem)
			}
			r := env.do(t, http.MethodPost, "/api/stacks/authelia/setup/run", map[string]any{"plan_digest": p.Digest}, cookie)
			if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, tt.problem) || !strings.Contains(r.body, `"location":"body"`) {
				t.Errorf("run = %d %s, want 422 at location body naming %q", r.status, r.body, tt.problem)
			}
			if tree := treeOf(t, storage); !cmp.Equal(tree, []string{"appconfigs", "databases"}) {
				t.Errorf("storage after a refused run = %v, want only the empty roots", tree)
			}
		})
	}
}
