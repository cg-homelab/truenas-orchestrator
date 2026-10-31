package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/gitrepo"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/hostfs"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

const (
	testUser     = "admin"
	testPassword = "correct horse battery staple"
	// fixtureSecret is a .env value in the fixture repo (compose/db-postgres/.env).
	fixtureSecret = "REPLACE_THIS"
	testStorage   = "/srv/homelab"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var _ Repo = (*gitrepo.Repo)(nil)

// fixtureRepo materialises fixtures/homelab-repo as a git repository in a temp dir.
func fixtureRepo(t *testing.T) *gitrepo.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "init-fixture-repo.sh"))
	if err != nil {
		t.Fatalf("locate script: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	cmd := exec.CommandContext(t.Context(), "bash", script, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init fixture repo: %v\n%s", err, out)
	}
	return gitrepo.New(dir)
}

// fetchRepo wraps a Repo with a scripted Fetch and counts Status calls (one per validation).
type fetchRepo struct {
	homelab.Repo
	fetch func(ctx context.Context, report func(string)) error

	mu       sync.Mutex
	statuses int
}

func (r *fetchRepo) Status(ctx context.Context) (homelab.RepoStatus, error) {
	r.mu.Lock()
	r.statuses++
	r.mu.Unlock()
	return r.Repo.Status(ctx)
}

func (r *fetchRepo) statusCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statuses
}

func (r *fetchRepo) Fetch(ctx context.Context, report func(string)) (homelab.RepoStatus, error) {
	if r.fetch != nil {
		if err := r.fetch(ctx, report); err != nil {
			return homelab.RepoStatus{}, err
		}
	}
	return r.Repo.Status(ctx)
}

// testEnv is a running server with a temp database, a fake clock and captured logs.
type testEnv struct {
	srv   *httptest.Server
	store *sqlite.Store
	clock *fakeClock
	logs  *syncBuffer
	repo  *fetchRepo
	// stopJobs stops the job runner, as a shutdown does, and waits for it.
	stopJobs func()
}

type envOptions struct {
	secureCookies bool
	idle          time.Duration
	lifetime      time.Duration
	fetch         func(ctx context.Context, report func(string)) error
	// beforeRun runs against the store before the job runner starts.
	beforeRun func(t *testing.T, store *sqlite.Store)
	// prepareRepo changes the materialised fixture repo, rooted at root, before the server starts.
	prepareRepo func(t *testing.T, root string)
	// hostPathExists enables PATH001 in the server's validation.
	hostPathExists func(path string) (bool, error)
	// wrapSecrets wraps the env store the server uses.
	wrapSecrets func(homelab.SecretStore) homelab.SecretStore
	// noSecrets and noHost leave Deps.Secrets and Deps.Host nil.
	noSecrets, noHost bool
	// deps changes the server's Deps last, e.g. to add a TrueNAS dialer.
	deps func(d *Deps)
}

func newEnv(t *testing.T, opts envOptions) *testEnv {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if opts.beforeRun != nil {
		opts.beforeRun(t, store)
	}
	gr := fixtureRepo(t)
	if opts.prepareRepo != nil {
		opts.prepareRepo(t, gr.Root())
	}
	env := &testEnv{
		store: store,
		clock: newFakeClock(),
		logs:  &syncBuffer{},
		repo:  &fetchRepo{Repo: gr, fetch: opts.fetch},
	}
	var secrets homelab.SecretStore = gitrepo.NewEnvStore(gr)
	if opts.wrapSecrets != nil {
		secrets = opts.wrapSecrets(secrets)
	}
	if opts.noSecrets {
		secrets = nil
	}
	var host homelab.SetupHost = hostfs.New()
	if opts.noHost {
		host = nil
	}
	deps := Deps{
		Logger:          slog.New(slog.NewJSONHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Store:           store,
		Repo:            env.repo,
		Secrets:         secrets,
		Host:            host,
		Validate:        homelab.Options{HomelabStorage: testStorage, HostPathExists: opts.hostPathExists},
		SecureCookies:   opts.secureCookies,
		SessionIdle:     opts.idle,
		SessionLifetime: opts.lifetime,
		Now:             env.clock.Now,
	}
	if opts.deps != nil {
		opts.deps(&deps)
	}
	s := NewServer(deps)
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	var stop sync.Once
	env.stopJobs = func() {
		stop.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(env.stopJobs)
	env.srv = httptest.NewServer(s)
	t.Cleanup(env.srv.Close)
	return env
}

// cred adds credentials to a request.
type cred func(*http.Request)

func withCookie(c *http.Cookie) cred { return func(r *http.Request) { r.AddCookie(c) } }

func withBearer(tok string) cred {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// response is a finished HTTP exchange.
type response struct {
	status int
	header http.Header
	body   string
	cookie *http.Cookie // the orch_session cookie set, if any
}

// rawBody is a request body sent as is, for malformed JSON.
type rawBody string

// do sends a request with an optional JSON body and returns the response.
func (e *testEnv) do(t *testing.T, method, path string, body any, creds ...cred) response {
	t.Helper()
	var rd io.Reader = http.NoBody
	if raw, ok := body.(rawBody); ok {
		rd = strings.NewReader(string(raw))
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range creds {
		c(req)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	r := response{status: resp.StatusCode, header: resp.Header, body: string(b)}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			r.cookie = c
		}
	}
	return r
}

// decode unmarshals a response body into v.
func decode(t *testing.T, r response, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

func credentials(user, pw string) map[string]string {
	return map[string]string{"username": user, "password": pw}
}

// setup creates the first user.
func (e *testEnv) setup(t *testing.T) {
	t.Helper()
	if r := e.do(t, http.MethodPost, "/api/auth/setup", credentials(testUser, testPassword)); r.status != http.StatusCreated {
		t.Fatalf("POST /api/auth/setup status = %d, want 201; body %s", r.status, r.body)
	}
}

// login logs in as the test user and returns the session cookie.
func (e *testEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	r := e.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, testPassword))
	if r.status != http.StatusNoContent || r.cookie == nil {
		t.Fatalf("POST /api/auth/login status = %d, cookie %v, want 204 and a cookie; body %s", r.status, r.cookie, r.body)
	}
	return r.cookie
}

// createToken creates an API token and returns its id and raw value.
func (e *testEnv) createToken(t *testing.T, c cred) (id, token string) {
	t.Helper()
	r := e.do(t, http.MethodPost, "/api/auth/tokens", map[string]string{"name": "cli"}, c)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /api/auth/tokens status = %d, want 201; body %s", r.status, r.body)
	}
	var got CreatedToken
	decode(t, r, &got)
	if got.Token == "" || got.ID == "" {
		t.Fatalf("created token = %+v, want id and token", got)
	}
	return got.ID, got.Token
}

// sseEvent is one server-sent event.
type sseEvent struct {
	name string
	data string
}

// events reads a job-events stream to its end.
func (e *testEnv) events(t *testing.T, jobID string, c cred) []sseEvent {
	t.Helper()
	out, err := e.readEvents(t.Context(), jobID, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// readEvents reads a job-events stream to its end, calling onEvent (if non-nil) as each event
// arrives. It reports failures as errors so it can run off the test goroutine.
func (e *testEnv) readEvents(ctx context.Context, jobID string, c cred, onEvent func(sseEvent)) ([]sseEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/jobs/"+jobID+"/events", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	c(req)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET events status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return nil, fmt.Errorf("GET events Content-Type = %q, want text/event-stream", ct)
	}
	var out []sseEvent
	var cur sseEvent
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.name != "" || cur.data != "" {
				out = append(out, cur)
				if onEvent != nil {
					onEvent(cur)
				}
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return out, nil
}

// waitJob polls get-job until the job reaches a terminal state.
func (e *testEnv) waitJob(t *testing.T, id string, c cred) JobView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := e.do(t, http.MethodGet, "/api/jobs/"+id, nil, c)
		if r.status != http.StatusOK {
			t.Fatalf("GET /api/jobs/%s status = %d, want 200; body %s", id, r.status, r.body)
		}
		var j JobView
		decode(t, r, &j)
		if j.State == "succeeded" || j.State == "failed" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s after 10s", id, j.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
