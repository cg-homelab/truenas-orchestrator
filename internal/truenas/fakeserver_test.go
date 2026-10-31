package truenas

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// wireDir holds the recorded (initially synthetic) reply envelopes the fake server replays.
const wireDir = "../../fixtures/truenas/wire/25.10"

// fakeServer is a loopback TrueNAS stand-in: GET /api/versions and a websocket on
// /api/v25.10.0 that answers each request with a fixture envelope chosen by route. A route key is
// the method, or "<method> <first string param>", which wins when present; its value is a queue of
// fixture names whose last entry repeats. Unrouted methods get error.method_not_found.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server
	url string

	mu            sync.Mutex
	routes        map[string][]string
	overlay       map[string]string // fixture name -> envelope JSON, checked before wireDir
	reqs          []fakeRequest
	upgradeStatus int    // non-zero: answer the upgrade with this HTTP status instead of 101
	versions      string // fixture for GET /api/versions; empty answers 500
	pings         atomic.Int32
}

type fakeRequest struct {
	Method string
	Params json.RawMessage
}

func defaultRoutes() map[string][]string {
	r := map[string][]string{}
	for _, m := range []string{
		"auth.login_ex", "system.version", "core.set_options", "core.ping", "core.bulk",
		"docker.status", "pool.dataset.query", "pool.dataset.create", "user.query", "user.create",
		"group.query", "group.create", "filesystem.stat", "filesystem.setperm",
		"docker.network.query", "app.query", "app.create",
	} {
		r[m] = []string{m}
	}
	r["filesystem.stat /mnt/.orchestrator-smoke-absent"] = []string{"filesystem.stat.enoent"}
	r["filesystem.stat "] = []string{"filesystem.stat.einval"}
	return r
}

// fakeServerOption adjusts a fakeServer before it starts.
type fakeServerOption func(*fakeServer)

func withRoute(key string, fixtures ...string) fakeServerOption {
	return func(s *fakeServer) { s.routes[key] = fixtures }
}

func withFixture(name, envelope string) fakeServerOption {
	return func(s *fakeServer) { s.overlay[name] = envelope }
}

// withUpgradeStatus answers the websocket upgrade with status and GET /api/versions with the
// versions fixture (empty: HTTP 500).
func withUpgradeStatus(status int, versions string) fakeServerOption {
	return func(s *fakeServer) { s.upgradeStatus, s.versions = status, versions }
}

func withTLS(cert tls.Certificate) fakeServerOption {
	return func(s *fakeServer) {
		s.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
}

func newFakeServer(t *testing.T, opts ...fakeServerOption) *fakeServer {
	t.Helper()
	s := &fakeServer{t: t, routes: defaultRoutes(), overlay: map[string]string{}, versions: "api.versions"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/versions", s.serveVersions)
	mux.HandleFunc("/api/"+APIVersion, s.serveWS)
	s.srv = httptest.NewUnstartedServer(mux)
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the TLS tests fail handshakes on purpose
	for _, o := range opts {
		o(s)
	}
	if s.srv.TLS != nil {
		s.srv.StartTLS()
		s.url = "wss://" + s.srv.Listener.Addr().String()
	} else {
		s.srv.Start()
		s.url = "ws://" + s.srv.Listener.Addr().String()
	}
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeServer) fixture(name string) (map[string]json.RawMessage, bool) {
	s.mu.Lock()
	data, ok := s.overlay[name]
	s.mu.Unlock()
	raw := []byte(data)
	if !ok {
		var err error
		raw, err = fs.ReadFile(os.DirFS(wireDir), name+".json")
		if err != nil {
			s.t.Errorf("fake server: fixture %s: %v", name, err)
			return nil, false
		}
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		s.t.Errorf("fake server: fixture %s: %v", name, err)
		return nil, false
	}
	delete(env, "_provenance")
	return env, true
}

func (s *fakeServer) serveVersions(w http.ResponseWriter, _ *http.Request) {
	if s.versions == "" {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	env, ok := s.fixture(s.versions)
	if !ok {
		http.Error(w, "no fixture", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(env["http_body"])
}

func (s *fakeServer) serveWS(w http.ResponseWriter, r *http.Request) {
	if s.upgradeStatus != 0 {
		http.Error(w, http.StatusText(s.upgradeStatus), s.upgradeStatus)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool { s.pings.Add(1); return true },
	})
	if err != nil {
		s.t.Errorf("fake server: accept: %v", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx := r.Context()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			s.t.Errorf("fake server: request: %v", err)
			return
		}
		env, ok := s.fixture(s.next(req.Method, req.Params))
		if !ok {
			return
		}
		env["id"] = req.ID
		out, err := json.Marshal(env)
		if err != nil {
			s.t.Errorf("fake server: reply: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}

// next records the request and returns the fixture name that answers it.
func (s *fakeServer) next(method string, params json.RawMessage) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, fakeRequest{Method: method, Params: params})
	key := method
	var args []json.RawMessage
	if json.Unmarshal(params, &args) == nil && len(args) > 0 {
		var first string
		if json.Unmarshal(args[0], &first) == nil {
			if _, ok := s.routes[method+" "+first]; ok {
				key = method + " " + first
			}
		}
	}
	queue, ok := s.routes[key]
	if !ok || len(queue) == 0 {
		return "error.method_not_found"
	}
	name := queue[0]
	if len(queue) > 1 {
		s.routes[key] = queue[1:]
	}
	return name
}

func (s *fakeServer) requests() []fakeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeRequest(nil), s.reqs...)
}

func (s *fakeServer) methods() []string {
	var out []string
	for _, r := range s.requests() {
		out = append(out, r.Method)
	}
	return out
}

// lastParams returns the params of the last request for method.
func (s *fakeServer) lastParams(t *testing.T, method string) string {
	t.Helper()
	reqs := s.requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			return string(reqs[i].Params)
		}
	}
	t.Fatalf("no %s request; got %s", method, strings.Join(s.methods(), ", "))
	return ""
}
