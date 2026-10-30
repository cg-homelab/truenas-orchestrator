package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/compose"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

func TestStacksMatchValidateRepo(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))

	want, err := homelab.ValidateRepo(t.Context(), env.repo.Repo, &compose.Parser{}, homelab.Options{HomelabStorage: testStorage})
	if err != nil {
		t.Fatalf("ValidateRepo: %v", err)
	}

	var list StackList
	decode(t, env.do(t, http.MethodGet, "/api/stacks", nil, cookie), &list)
	wantList := StackList{Stacks: []StackSummary{}}
	for _, s := range want.Stacks {
		wantList.Stacks = append(wantList.Stacks, StackSummary{Name: s.Name, Status: string(s.Status), Counts: countFindings(s.Findings)})
	}
	if diff := cmp.Diff(wantList, list); diff != "" {
		t.Errorf("GET /api/stacks mismatch (-ValidateRepo +server):\n%s", diff)
	}

	for _, s := range want.Stacks {
		var got StackDetail
		decode(t, env.do(t, http.MethodGet, "/api/stacks/"+s.Name, nil, cookie), &got)
		if diff := cmp.Diff(newStackDetail(s, false), got); diff != "" {
			t.Errorf("GET /api/stacks/%s mismatch (-want +got):\n%s", s.Name, diff)
		}
	}

	var repo RepoView
	r := env.do(t, http.MethodGet, "/api/repo", nil, cookie)
	decode(t, r, &repo)
	if diff := cmp.Diff(newRepoView(want), repo); diff != "" {
		t.Errorf("GET /api/repo mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(r.body, `"last_fetch":null`) || !strings.Contains(r.body, `"upstream":""`) {
		t.Errorf("GET /api/repo body = %s, want last_fetch null and upstream empty", r.body)
	}
}

func TestGetStackRedaction(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))

	lookup := func(vars []EnvVar, key string) (EnvVar, bool) {
		for _, v := range vars {
			if v.Key == key {
				return v, true
			}
		}
		return EnvVar{}, false
	}

	r := env.do(t, http.MethodGet, "/api/stacks/db-postgres", nil, cookie)
	if strings.Contains(r.body, fixtureSecret) {
		t.Errorf("GET /api/stacks/db-postgres leaks a .env value: %s", r.body)
	}
	var masked StackDetail
	decode(t, r, &masked)
	if len(masked.Interpolation) == 0 || len(masked.Container) == 0 {
		t.Fatalf("db-postgres scopes are empty: %+v", masked)
	}
	for _, v := range append(masked.Interpolation, masked.Container...) {
		if v.Value != redactedValue {
			t.Errorf("%s = %q (from %s), want %s", v.Key, v.Value, v.Source, redactedValue)
		}
	}
	if v, ok := lookup(masked.Container, "POSTGRES_PASSWORD"); !ok || v.Source != "compose/db-postgres/.env" || v.Line != 2 {
		t.Errorf("container POSTGRES_PASSWORD = %+v, %v, want source compose/db-postgres/.env line 2", v, ok)
	}

	if strings.Contains(env.logs.String(), "env values revealed") {
		t.Fatal("reveal logged before any reveal")
	}
	r = env.do(t, http.MethodGet, "/api/stacks/db-postgres?reveal=true", nil, cookie)
	if got := r.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("revealed response Cache-Control = %q, want no-store", got)
	}
	var revealed StackDetail
	decode(t, r, &revealed)
	for _, scope := range [][]EnvVar{revealed.Interpolation, revealed.Container} {
		if v, ok := lookup(scope, "POSTGRES_PASSWORD"); !ok || v.Value != fixtureSecret {
			t.Errorf("revealed POSTGRES_PASSWORD = %+v, %v, want %s", v, ok, fixtureSecret)
		}
	}
	if v, ok := lookup(revealed.Interpolation, "HOMELAB_STORAGE"); !ok || v.Value != testStorage || v.Source != homelab.SourceInjected {
		t.Errorf("revealed HOMELAB_STORAGE = %+v, %v, want %s injected", v, ok, testStorage)
	}

	logs := env.logs.String()
	if !strings.Contains(logs, `"msg":"env values revealed","stack":"db-postgres","user":"admin"`) {
		t.Errorf("logs lack the reveal line with stack and user:\n%s", logs)
	}
	if strings.Contains(logs, fixtureSecret) {
		t.Errorf("logs contain a revealed value:\n%s", logs)
	}

	// The cached report is still masked for the next plain request.
	if r := env.do(t, http.MethodGet, "/api/stacks/db-postgres", nil, cookie); strings.Contains(r.body, fixtureSecret) {
		t.Errorf("plain request after a reveal leaks a value: %s", r.body)
	}
	if r := env.do(t, http.MethodGet, "/api/stacks/no-such-stack", nil, cookie); r.status != http.StatusNotFound {
		t.Errorf("GET unknown stack status = %d, want 404", r.status)
	}
}

func TestReportIsCachedAndInvalidatedByFetch(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))

	for _, p := range []string{"/api/repo", "/api/stacks", "/api/stacks/ok-control"} {
		if r := env.do(t, http.MethodGet, p, nil, cookie); r.status != http.StatusOK {
			t.Fatalf("GET %s status = %d", p, r.status)
		}
	}
	if n := env.repo.statusCalls(); n != 1 {
		t.Errorf("validations for three requests within the TTL = %d, want 1", n)
	}
	env.clock.Advance(reportTTL)
	env.do(t, http.MethodGet, "/api/stacks", nil, cookie)
	if n := env.repo.statusCalls(); n != 2 {
		t.Errorf("validations after the TTL = %d, want 2", n)
	}

	var started JobStarted
	decode(t, env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie), &started)
	env.waitJob(t, started.JobID, cookie)
	before := env.repo.statusCalls()
	env.do(t, http.MethodGet, "/api/stacks", nil, cookie)
	if n := env.repo.statusCalls(); n != before+1 {
		t.Errorf("validations after a fetch = %d, want %d (cache invalidated)", n, before+1)
	}
}

func TestFetchJobStreamsLogThenDone(t *testing.T) {
	env := newEnv(t, envOptions{fetch: func(_ context.Context, report func(string)) error {
		report("Fetching origin")
		report("From https://user:hunter2@example.invalid/homelab.git")
		return nil
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))

	r := env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie)
	if r.status != http.StatusAccepted {
		t.Fatalf("POST /api/repo/fetch status = %d, want 202; body %s", r.status, r.body)
	}
	var started JobStarted
	decode(t, r, &started)

	wantEvents := []sseEvent{
		{"log", `{"line":"Fetching origin"}`},
		{"log", `{"line":"From https://[REDACTED]@example.invalid/homelab.git"}`},
		{"done", `{"state":"succeeded","error":null}`},
	}
	// Live (or replayed, if the job already finished) and then replayed from the database.
	for _, pass := range []string{"first", "second"} {
		if diff := cmp.Diff(wantEvents, env.events(t, started.JobID, cookie), cmp.AllowUnexported(sseEvent{})); diff != "" {
			t.Errorf("%s job-events stream mismatch (-want +got):\n%s", pass, diff)
		}
	}

	j := env.waitJob(t, started.JobID, cookie)
	want := JobView{
		ID: started.JobID, Kind: jobKindRepoFetch, State: "succeeded",
		CreatedAt: env.clock.Now(), StartedAt: ptr(env.clock.Now()), FinishedAt: ptr(env.clock.Now()),
		Log: []string{"Fetching origin", "From https://[REDACTED]@example.invalid/homelab.git"},
	}
	if diff := cmp.Diff(want, j); diff != "" {
		t.Errorf("GET /api/jobs/%s mismatch (-want +got):\n%s", started.JobID, diff)
	}
	if strings.Contains(env.logs.String(), "hunter2") {
		t.Errorf("logs contain URL credentials")
	}
}

func TestJobEventsUnknownJobEndsWithDone(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	got := env.events(t, "nope", withCookie(env.login(t)))
	want := []sseEvent{{"done", `{"state":"failed","error":"job not found"}`}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(sseEvent{})); diff != "" {
		t.Errorf("events for an unknown job mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchJobFailure(t *testing.T) {
	env := newEnv(t, envOptions{fetch: func(context.Context, func(string)) error {
		return errors.New("git fetch: exit status 128")
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))
	var started JobStarted
	decode(t, env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie), &started)
	events := env.events(t, started.JobID, cookie)
	want := []sseEvent{{"done", `{"state":"failed","error":"git fetch: exit status 128"}`}}
	if diff := cmp.Diff(want, events, cmp.AllowUnexported(sseEvent{})); diff != "" {
		t.Errorf("events of a failed job mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchConflict(t *testing.T) {
	release := make(chan struct{})
	running := make(chan struct{})
	env := newEnv(t, envOptions{fetch: func(ctx context.Context, report func(string)) error {
		close(running)
		report("waiting")
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))

	var started JobStarted
	decode(t, env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie), &started)
	<-running
	if r := env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie); r.status != http.StatusConflict {
		t.Errorf("second fetch while running status = %d, want 409; body %s", r.status, r.body)
	}
	var j JobView
	decode(t, env.do(t, http.MethodGet, "/api/jobs/"+started.JobID, nil, cookie), &j)
	if j.State != "running" || j.StartedAt == nil || j.FinishedAt != nil || !cmp.Equal(j.Log, []string{"waiting"}) {
		t.Errorf("running job = %+v, want running with log [waiting]", j)
	}

	// Follow the job live: the stream must deliver the line already logged, then wait for the
	// job to finish. The job cannot finish before release is closed, so the stream attaches to
	// the running job rather than replaying a finished one.
	type result struct {
		events []sseEvent
		err    error
	}
	streamed := make(chan result, 1)
	firstEvent := make(chan struct{})
	go func() {
		var once sync.Once
		evs, err := env.readEvents(t.Context(), started.JobID, cookie, func(sseEvent) { once.Do(func() { close(firstEvent) }) })
		streamed <- result{evs, err}
	}()
	select {
	case <-firstEvent:
	case res := <-streamed:
		t.Fatalf("stream ended before the job finished: %v, %v", res.events, res.err)
	case <-time.After(10 * time.Second):
		t.Fatal("no live event within 10s")
	}
	close(release)
	res := <-streamed
	if res.err != nil {
		t.Fatal(res.err)
	}
	wantEvents := []sseEvent{{"log", `{"line":"waiting"}`}, {"done", `{"state":"succeeded","error":null}`}}
	if diff := cmp.Diff(wantEvents, res.events, cmp.AllowUnexported(sseEvent{})); diff != "" {
		t.Errorf("live job-events stream mismatch (-want +got):\n%s", diff)
	}
	if j := env.waitJob(t, started.JobID, cookie); j.State != "succeeded" {
		t.Errorf("job state = %s, want succeeded", j.State)
	}
	if r := env.do(t, http.MethodPost, "/api/repo/fetch", nil, cookie); r.status != http.StatusAccepted {
		t.Errorf("fetch after the first finished status = %d, want 202", r.status)
	}
}

func TestInterruptedJobsFailAtStartup(t *testing.T) {
	var id string
	env := newEnv(t, envOptions{beforeRun: func(t *testing.T, store *sqlite.Store) {
		j, err := store.CreateJob(t.Context(), "leftover", jobKindRepoFetch, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.StartJob(t.Context(), j.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		id = j.ID
	}})
	env.setup(t)
	cookie := withCookie(env.login(t))
	j := env.waitJob(t, id, cookie)
	if j.State != "failed" || j.Error == nil || *j.Error != errInterrupted {
		t.Errorf("leftover running job = %+v, want failed: interrupted", j)
	}
}

func TestNoSecretInLogs(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, "a wrong password"))
	env.do(t, http.MethodPost, "/api/auth/login", credentials(testPassword, testPassword)) // password typed as username
	cookie := env.login(t)
	_, token := env.createToken(t, withCookie(cookie))
	env.do(t, http.MethodGet, "/api/stacks/db-postgres?reveal=true", nil, withBearer(token))
	env.do(t, http.MethodGet, "/api/auth/me", nil, withBearer("guessed-token"))

	logs := env.logs.String()
	if logs == "" {
		t.Fatal("no logs captured")
	}
	for name, secret := range map[string]string{
		"password": testPassword, "wrong password": "a wrong password", "session token": cookie.Value,
		"session hash": hashToken(cookie.Value), "api token": token, "token hash": hashToken(token),
		"guessed token": "guessed-token", ".env value": fixtureSecret,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain the %s", name)
		}
	}
}

func TestScrub(t *testing.T) {
	tests := []struct{ in, want string }{
		{"From https://user:tok@github.com/x.git", "From https://[REDACTED]@github.com/x.git"},
		{"fatal: unable to access 'https://tok@host/r/': error", "fatal: unable to access 'https://[REDACTED]@host/r/': error"},
		{"git@github.com:x/y.git", "git@github.com:x/y.git"},
		{"plain line", "plain line"},
	}
	for _, tt := range tests {
		if got := scrub(tt.in); got != tt.want {
			t.Errorf("scrub(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestShutdownInterruptsJobsAndRefusesNew(t *testing.T) {
	started := make(chan struct{})
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := newJobRunner(store, slog.New(slog.DiscardHandler), time.Now, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(ctx)
	}()
	running, err := r.enqueue(t.Context(), "a", func(ctx context.Context, _ func(string)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	queued, err := r.enqueue(t.Context(), "b", func(context.Context, func(string)) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	for _, id := range []string{running.ID, queued.ID} {
		j, err := store.Job(t.Context(), id)
		if err != nil || j.State != sqlite.JobFailed || j.Error != errInterrupted {
			t.Errorf("job %s after shutdown = %+v, %v, want failed: interrupted", id, j, err)
		}
	}
	if _, err := r.enqueue(t.Context(), "c", func(context.Context, func(string)) error { return nil }); !errors.Is(err, errShuttingDown) {
		t.Errorf("enqueue after shutdown error = %v, want errShuttingDown", err)
	}
}
