package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// knownSecret is a value the env tests write; no log, job output or audit entry may contain it.
const knownSecret = "hunter2hunter2"

// auditEntries returns the audit log through list-audit, and the raw body.
func (e *testEnv) auditEntries(t *testing.T, c cred) (entries []AuditEntryView, body string) {
	t.Helper()
	r := e.do(t, http.MethodGet, "/api/audit?limit=500", nil, c)
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/audit status = %d, want 200; body %s", r.status, r.body)
	}
	var l AuditList
	decode(t, r, &l)
	return l.Entries, r.body
}

// findAudit returns the newest entry with action.
func findAudit(entries []AuditEntryView, action string) (AuditEntryView, bool) {
	for _, e := range entries {
		if e.Action == action {
			return e, true
		}
	}
	return AuditEntryView{}, false
}

func TestGetStackEnv(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))
	identifier := "identifier"

	tests := []struct {
		name string
		path string
		want EnvFileView
	}{
		{
			name: "masked with a validator",
			path: "/api/stacks/ok-control/env",
			want: EnvFileView{
				Exists: true, Ignored: true, Entries: []EnvEntry{{Key: "SERVICE_NAME", Value: redactedValue, Line: 1, Validator: &identifier}},
				Missing: []EnvMissingKey{},
			},
		},
		{
			name: "revealed",
			path: "/api/stacks/ok-control/env?reveal=true",
			want: EnvFileView{
				Exists: true, Ignored: true, Entries: []EnvEntry{{Key: "SERVICE_NAME", Value: "ok-control", Line: 1, Validator: &identifier}},
				Missing: []EnvMissingKey{},
			},
		},
		{
			name: "tracked, with drift from example.env",
			path: "/api/stacks/env-drift/env",
			want: EnvFileView{
				Exists: true, Tracked: true, Ignored: true,
				Entries: []EnvEntry{
					{Key: "SERVICE_NAME", Value: redactedValue, Line: 2},
					{Key: "UNDOCUMENTED_KEY", Value: redactedValue, Line: 5},
				},
				Missing: []EnvMissingKey{{Key: "GREETING", Line: 2}},
			},
		},
		{
			name: "placeholder flagged without its value",
			path: "/api/stacks/db-postgres/env",
			want: EnvFileView{
				Exists: true, Ignored: true,
				Entries: []EnvEntry{
					{Key: "SERVICE_NAME", Value: redactedValue, Line: 1},
					{Key: "POSTGRES_PASSWORD", Value: redactedValue, Line: 2, Problem: ptr(msgPlaceholder)},
				},
				Missing: []EnvMissingKey{},
			},
		},
		{
			name: "absent env file lists every example key as missing",
			path: "/api/stacks/db-client/env",
			want: EnvFileView{
				Ignored: true, Entries: []EnvEntry{},
				Missing: []EnvMissingKey{{Key: "SERVICE_NAME", Line: 1}, {Key: "HTTP_PORT", Line: 2}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := env.do(t, http.MethodGet, tt.path, nil, cookie)
			if r.status != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body %s", tt.path, r.status, r.body)
			}
			var got EnvFileView
			decode(t, r, &got)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("GET %s mismatch (-want +got):\n%s", tt.path, diff)
			}
			if !strings.Contains(tt.path, "reveal") && strings.Contains(r.body, fixtureSecret) {
				t.Errorf("GET %s leaks a value: %s", tt.path, r.body)
			}
		})
	}

	if r := env.do(t, http.MethodGet, "/api/stacks/no-such-stack/env", nil, cookie); r.status != http.StatusNotFound {
		t.Errorf("GET unknown stack env status = %d, want 404", r.status)
	}
	if r := env.do(t, http.MethodGet, "/api/stacks/ok-control/env", nil); r.status != http.StatusUnauthorized {
		t.Errorf("GET env without credentials status = %d, want 401", r.status)
	}

	logs := env.logs.String()
	if !strings.Contains(logs, `"msg":"env values revealed","stack":"ok-control","user":"admin"`) {
		t.Errorf("logs lack the reveal line:\n%s", logs)
	}
	entries, _ := env.auditEntries(t, cookie)
	if diff := cmp.Diff([]string{auditEnvReveal}, actions(entries)); diff != "" {
		t.Errorf("audit actions mismatch (-want +got):\n%s", diff)
	}
	if e := entries[0]; e.User != testUser || e.Target != "compose/ok-control/.env" {
		t.Errorf("reveal audit entry = %+v, want user %s, target compose/ok-control/.env", e, testUser)
	}
}

func actions(entries []AuditEntryView) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

func TestUpdateStackEnvCreatesFromExample(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))
	root := env.repo.Root()

	var before StackDetail
	decode(t, env.do(t, http.MethodGet, "/api/stacks/db-client", nil, cookie), &before)
	if before.Status != "NeedsSetup" {
		t.Fatalf("db-client status before = %s, want NeedsSetup", before.Status)
	}

	r := env.do(t, http.MethodPut, "/api/stacks/db-client/env", map[string]any{}, cookie)
	if r.status != http.StatusOK {
		t.Fatalf("PUT db-client env status = %d, want 200; body %s", r.status, r.body)
	}
	var got EnvFileView
	decode(t, r, &got)
	want := EnvFileView{
		Exists: true, Ignored: true,
		Entries: []EnvEntry{
			{Key: "SERVICE_NAME", Value: redactedValue, Line: 1},
			{Key: "HTTP_PORT", Value: redactedValue, Line: 2},
		},
		Missing: []EnvMissingKey{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PUT response mismatch (-want +got):\n%s", diff)
	}

	tmpl, err := os.ReadFile(filepath.Join(root, "compose", "db-client", "example.env"))
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(root, "compose", "db-client", ".env"))
	if err != nil {
		t.Fatalf("read created .env: %v", err)
	}
	if diff := cmp.Diff(string(tmpl), string(written)); diff != "" {
		t.Errorf(".env differs from example.env (-want +got):\n%s", diff)
	}
	if fi, err := os.Stat(filepath.Join(root, "compose", "db-client", ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("created .env mode = %v, %v, want 0600", fi.Mode().Perm(), err)
	}

	// The report is invalidated by the write, so the stack no longer lacks its env file.
	var after StackDetail
	decode(t, env.do(t, http.MethodGet, "/api/stacks/db-client", nil, cookie), &after)
	for _, f := range after.Findings {
		if f.Code == "ENV004" {
			t.Errorf("db-client still reports ENV004 after the env file was created: %+v", f)
		}
	}

	entries, _ := env.auditEntries(t, cookie)
	e, ok := findAudit(entries, auditEnvWrite)
	wantDetails := map[string]string{"keys_set": "", "keys_unset": "", "created": "true"}
	if !ok || e.Target != "compose/db-client/.env" || !cmp.Equal(e.Details, wantDetails) {
		t.Errorf("env.write audit entry = %+v, %v, want target compose/db-client/.env and details %v", e, ok, wantDetails)
	}
}

func TestUpdateStackEnvSetThenReveal(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))

	body := map[string]any{"set": map[string]string{"API_SECRET_VALUE": knownSecret}}
	r := env.do(t, http.MethodPut, "/api/stacks/ok-control/env", body, cookie)
	if r.status != http.StatusOK {
		t.Fatalf("PUT ok-control env status = %d, want 200; body %s", r.status, r.body)
	}
	if strings.Contains(r.body, knownSecret) {
		t.Errorf("PUT response echoes the value: %s", r.body)
	}

	var revealed EnvFileView
	decode(t, env.do(t, http.MethodGet, "/api/stacks/ok-control/env?reveal=true", nil, cookie), &revealed)
	want := []EnvEntry{
		{Key: "SERVICE_NAME", Value: "ok-control", Line: 1, Validator: ptr("identifier")},
		{Key: "API_SECRET_VALUE", Value: knownSecret, Line: 2},
	}
	if diff := cmp.Diff(want, revealed.Entries); diff != "" {
		t.Errorf("revealed entries after PUT mismatch (-want +got):\n%s", diff)
	}

	// Unset removes it again.
	r = env.do(t, http.MethodPut, "/api/stacks/ok-control/env", map[string]any{"unset": []string{"API_SECRET_VALUE"}}, cookie)
	var unset EnvFileView
	decode(t, r, &unset)
	if len(unset.Entries) != 1 || unset.Entries[0].Key != "SERVICE_NAME" {
		t.Errorf("entries after unset = %+v, want only SERVICE_NAME", unset.Entries)
	}

	entries, auditBody := env.auditEntries(t, cookie)
	if diff := cmp.Diff([]string{auditEnvWrite, auditEnvReveal, auditEnvWrite}, actions(entries)); diff != "" {
		t.Errorf("audit actions mismatch (-want +got):\n%s", diff)
	}
	wantSet := map[string]string{"keys_set": "API_SECRET_VALUE", "keys_unset": "", "created": "false"}
	if diff := cmp.Diff(wantSet, entries[2].Details); diff != "" {
		t.Errorf("first env.write details mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(auditBody, knownSecret) {
		t.Errorf("audit log contains the value: %s", auditBody)
	}
	logs := env.logs.String()
	if strings.Contains(logs, knownSecret) {
		t.Errorf("logs contain the value:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"env file written","stack":"ok-control","keys":["API_SECRET_VALUE"]`) {
		t.Errorf("logs lack the env file written line:\n%s", logs)
	}
}

func TestUpdateStackEnvRejects(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))
	envFile := filepath.Join(env.repo.Root(), "compose", "ok-control", ".env")
	original, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		stack    string
		body     any
		status   int
		contains []string
	}{
		{
			name: "value fails its validator", stack: "ok-control",
			body:   map[string]any{"set": map[string]string{"SERVICE_NAME": "Hunter2 Not An Identifier"}},
			status: http.StatusUnprocessableEntity,
			contains: []string{
				`"location":"body.set.SERVICE_NAME"`,
				"is not an identifier",
			},
		},
		{
			name: "placeholder value", stack: "ok-control",
			body:     map[string]any{"set": map[string]string{"OTHER": "CHANGEME"}},
			status:   http.StatusUnprocessableEntity,
			contains: []string{`"location":"body.set.OTHER"`, msgPlaceholder},
		},
		{
			name: "key is not a variable name", stack: "ok-control",
			body:     map[string]any{"set": map[string]string{"1BAD": "x"}, "unset": []string{"ALSO BAD"}},
			status:   http.StatusUnprocessableEntity,
			contains: []string{`"location":"body.set.1BAD"`, `"location":"body.unset[0]"`},
		},
		{
			name: "malformed set", stack: "ok-control",
			body:   json.RawMessage(`{"set":"` + knownSecret + `"}`),
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "malformed value", stack: "ok-control",
			body:   json.RawMessage(`{"set":{"KEY":["` + knownSecret + `"]}}`),
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "tracked env file", stack: "env-drift",
			body:     map[string]any{"set": map[string]string{"GREETING": knownSecret}},
			status:   http.StatusConflict,
			contains: []string{"compose/env-drift/.env is tracked by git (GIT001); it is never written"},
		},
		{
			name: "control character", stack: "ok-control",
			body:     map[string]any{"set": map[string]string{"CTRL": "a\x00b"}},
			status:   http.StatusUnprocessableEntity,
			contains: []string{`"location":"body.set.CTRL"`, msgControlChar},
		},
		{
			name: "key both set and unset", stack: "ok-control",
			body:     map[string]any{"set": map[string]string{"BOTH": "x"}, "unset": []string{"BOTH"}},
			status:   http.StatusUnprocessableEntity,
			contains: []string{`"location":"body.unset[0]"`, "is also in set"},
		},
		{
			name: "JSON syntax error", stack: "ok-control",
			body:     rawBody(`{` + knownSecret + `}`),
			status:   http.StatusBadRequest,
			contains: []string{`"message":"` + msgInvalidJSON + `"`},
		},
		{
			name: "no-op update of a tracked env file", stack: "env-drift",
			body:     map[string]any{},
			status:   http.StatusConflict,
			contains: []string{"compose/env-drift/.env is tracked by git (GIT001); it is never written"},
		},
		{
			name: "unknown stack", stack: "no-such-stack",
			body:   map[string]any{"set": map[string]string{"A": "b"}},
			status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := env.do(t, http.MethodPut, "/api/stacks/"+tt.stack+"/env", tt.body, cookie)
			if r.status != tt.status {
				t.Fatalf("PUT status = %d, want %d; body %s", r.status, tt.status, r.body)
			}
			for _, s := range tt.contains {
				if !strings.Contains(r.body, s) {
					t.Errorf("body lacks %q: %s", s, r.body)
				}
			}
			for _, v := range []string{knownSecret, "Hunter2", "CHANGEME", "invalid character"} {
				if strings.Contains(r.body, v) {
					t.Errorf("body echoes %q: %s", v, r.body)
				}
			}
		})
	}

	if got, err := os.ReadFile(envFile); err != nil || string(got) != string(original) {
		t.Errorf("ok-control .env changed by rejected updates: %q, %v", got, err)
	}
	drift, err := os.ReadFile(filepath.Join(env.repo.Root(), "compose", "env-drift", ".env"))
	if err != nil || strings.Contains(string(drift), knownSecret) {
		t.Errorf("tracked env-drift .env was written: %q, %v", drift, err)
	}
	if entries, _ := env.auditEntries(t, cookie); len(entries) != 0 {
		t.Errorf("rejected updates left audit entries: %+v", entries)
	}
	if strings.Contains(env.logs.String(), knownSecret) {
		t.Errorf("logs contain a rejected value:\n%s", env.logs.String())
	}
}

func TestUpdateStackEnvRefusesWhenNotIgnored(t *testing.T) {
	env := newEnv(t, envOptions{prepareRepo: func(t *testing.T, root string) {
		t.Helper()
		// Drop the compose/*/.env rule: env files are then one git add away from a commit.
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("compose/.global.env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))

	var got EnvFileView
	decode(t, env.do(t, http.MethodGet, "/api/stacks/db-client/env", nil, cookie), &got)
	if got.Ignored || got.Tracked {
		t.Errorf("db-client env git state = tracked %v ignored %v, want neither", got.Tracked, got.Ignored)
	}
	r := env.do(t, http.MethodPut, "/api/stacks/db-client/env", map[string]any{}, cookie)
	if r.status != http.StatusConflict || !strings.Contains(r.body, "compose/db-client/.env is not gitignored; add compose/*/.env to .gitignore first") {
		t.Errorf("PUT with no ignore rule = %d %s, want 409 not gitignored", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(env.repo.Root(), "compose", "db-client", ".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("db-client .env after a refused PUT: %v, want absent", err)
	}
	// Also when the update would change nothing.
	if r := env.do(t, http.MethodPut, "/api/stacks/ok-control/env", map[string]any{}, cookie); r.status != http.StatusConflict {
		t.Errorf("no-op PUT with no ignore rule = %d %s, want 409", r.status, r.body)
	}
}

// pausingStore pauses the first ReadEnv of app until release is closed, after closing reached.
type pausingStore struct {
	homelab.SecretStore
	app     string
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (p *pausingStore) ReadEnv(ctx context.Context, app string) ([]byte, error) {
	b, err := p.SecretStore.ReadEnv(ctx, app)
	if app == p.app {
		p.once.Do(func() {
			close(p.reached)
			<-p.release
		})
	}
	return b, err
}

// send is a request that may run off the test goroutine; it returns the status code.
func (e *testEnv) send(ctx context.Context, method, path, body string, c cred) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c(req)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func TestUpdateStackEnvSerialisesWrites(t *testing.T) {
	ps := &pausingStore{app: "ok-control", reached: make(chan struct{}), release: make(chan struct{})}
	env := newEnv(t, envOptions{wrapSecrets: func(s homelab.SecretStore) homelab.SecretStore {
		ps.SecretStore = s
		return ps
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))

	type result struct {
		status int
		err    error
	}
	put := func(key string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			st, err := env.send(t.Context(), http.MethodPut, "/api/stacks/ok-control/env", `{"set":{"`+key+`":"v"}}`, cookie)
			ch <- result{st, err}
		}()
		return ch
	}
	first := put("FIRST")
	<-ps.reached // the first update has read the file and is paused before writing
	second := put("SECOND")
	select {
	case r := <-second:
		t.Fatalf("second update finished (%d, %v) while the first held the env file", r.status, r.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(ps.release)
	for name, ch := range map[string]<-chan result{"first": first, "second": second} {
		if r := <-ch; r.err != nil || r.status != http.StatusOK {
			t.Errorf("%s update = %d, %v, want 200", name, r.status, r.err)
		}
	}
	b, err := os.ReadFile(filepath.Join(env.repo.Root(), "compose", "ok-control", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"FIRST=v", "SECOND=v"} {
		if !strings.Contains(string(b), k) {
			t.Errorf(".env lacks %s after two overlapping updates:\n%s", k, b)
		}
	}
}

func TestEnvAndSetupWithoutAdapters(t *testing.T) {
	tests := []struct {
		name string
		opts envOptions
		reqs map[string]int // "METHOD path" -> status
	}{
		{
			name: "no secret store", opts: envOptions{noSecrets: true},
			reqs: map[string]int{
				"GET /api/stacks/ok-control/env":         http.StatusInternalServerError,
				"PUT /api/stacks/ok-control/env":         http.StatusInternalServerError,
				"POST /api/stacks/ok-control/setup/plan": http.StatusInternalServerError,
				"POST /api/stacks/ok-control/setup/run":  http.StatusInternalServerError,
			},
		},
		{
			name: "no setup host", opts: envOptions{noHost: true},
			reqs: map[string]int{
				"GET /api/stacks/ok-control/env":         http.StatusOK,
				"POST /api/stacks/ok-control/setup/plan": http.StatusInternalServerError,
				"POST /api/stacks/ok-control/setup/run":  http.StatusInternalServerError,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, tt.opts)
			env.setup(t)
			cookie := withCookie(env.login(t))
			for req, want := range tt.reqs {
				method, path, _ := strings.Cut(req, " ")
				r := env.do(t, method, path, map[string]any{}, cookie)
				if r.status != want {
					t.Errorf("%s status = %d, want %d; body %s", req, r.status, want, r.body)
				}
				if want == http.StatusInternalServerError && !strings.Contains(r.body, `"detail":"internal error"`) {
					t.Errorf("%s body = %s, want the fixed internal error", req, r.body)
				}
			}
		})
	}
}
