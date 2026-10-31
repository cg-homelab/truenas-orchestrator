package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// testAPIKey is the key the fake TrueNAS accepts. Tests grep every response, log line and audit
// entry for it.
const testAPIKey = "1-k3yV4lu3ThatMustNeverLeak"

// fakeTrueNAS is a stateful in-memory TrueNAS behind a TrueNASDialer: every dial is a new session
// on the same state, so a bootstrap job's changes are visible to the next get-environment. It
// mirrors the middleware's effects that the bootstrap relies on (a created dataset becomes a
// 0:0 0755 directory; the networks app creates its networks).
type fakeTrueNAS struct {
	mu       sync.Mutex
	version  string
	docker   homelab.DockerStatus
	groups   []homelab.Group
	users    []homelab.User
	datasets []homelab.Dataset
	stats    map[string]homelab.PathStat
	networks []homelab.Network
	apps     []homelab.App
	// dialErr, when set, is what every dial returns.
	dialErr error
	// createDatasetErr, when set, is what every CreateDataset returns.
	createDatasetErr error
	// calls logs every mutating call, e.g. "CreateApp homelab-networks".
	calls         []string
	dials, closes int
	// block, when set, makes CreateGroup wait for it to close; entered is closed on the wait.
	block, entered chan struct{}
}

// newFakeTrueNAS is a fresh, supported TrueNAS whose pool tank is mounted at base, with the
// state dataset at base/state and the media root at base/media.
func newFakeTrueNAS(base string) *fakeTrueNAS {
	props := homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	return &fakeTrueNAS{
		version: "TrueNAS-25.10.1",
		docker:  homelab.DockerStatus{Status: "RUNNING"},
		groups:  []homelab.Group{{ID: 1, GID: 950, Name: "truenas_admin"}},
		users:   []homelab.User{{ID: 1, UID: 950, GID: 950, Name: "truenas_admin"}},
		datasets: []homelab.Dataset{
			{Name: "tank", Mountpoint: base, Props: props},
			{Name: "tank/state", Mountpoint: base + "/state", Props: props},
		},
		stats: map[string]homelab.PathStat{
			base:            {Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true},
			base + "/state": {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o700, IsMountpoint: true},
			base + "/media": {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o755},
		},
		apps: []homelab.App{{Name: homelab.DefaultSelfApp, State: "RUNNING", Custom: true}},
	}
}

// dial is the TrueNASDialer: the auth and version gate of truenas.Dial.
func (f *fakeTrueNAS) dial(_ context.Context, creds homelab.TrueNASCredentials) (TrueNASConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	if creds.APIKey.Reveal() != testAPIKey {
		return nil, fmt.Errorf("TrueNAS rejected the API key for user %s: %w", creds.Username, homelab.ErrUnauthenticated)
	}
	if err := homelab.CheckTrueNASVersion(f.version); err != nil {
		return nil, err
	}
	return &fakeConn{f: f}, nil
}

func (f *fakeTrueNAS) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeTrueNAS) sessions() (dials, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials, f.closes
}

// fakeConn is one session on a fakeTrueNAS.
type fakeConn struct {
	f *fakeTrueNAS
}

var _ TrueNASConn = (*fakeConn)(nil)

func (c *fakeConn) Close() error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.closes++
	return nil
}

func (c *fakeConn) SystemVersion(context.Context) (string, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.f.version, nil
}

func (c *fakeConn) DockerStatus(context.Context) (homelab.DockerStatus, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.f.docker, nil
}

func (c *fakeConn) Datasets(context.Context) ([]homelab.Dataset, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.f.datasets), nil
}

func (c *fakeConn) CreateDataset(_ context.Context, spec homelab.DatasetSpec) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.calls = append(c.f.calls, "CreateDataset "+spec.Name)
	if c.f.createDatasetErr != nil {
		return c.f.createDatasetErr
	}
	i := strings.LastIndex(spec.Name, "/")
	idx := slices.IndexFunc(c.f.datasets, func(d homelab.Dataset) bool { return d.Name == spec.Name[:max(i, 0)] })
	if i < 0 || idx < 0 {
		return fmt.Errorf("parent of %s: %w", spec.Name, homelab.ErrNotFound)
	}
	parent := c.f.datasets[idx]
	mp := path.Join(parent.Mountpoint, spec.Name[i+1:])
	d := homelab.Dataset{Name: spec.Name, Mountpoint: mp, Props: parent.Props}
	if spec.Props != nil {
		d.Props = *spec.Props
	}
	c.f.datasets = append(c.f.datasets, d)
	c.f.stats[mp] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true}
	return nil
}

func (c *fakeConn) Users(context.Context) ([]homelab.User, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.f.users), nil
}

func (c *fakeConn) CreateUser(_ context.Context, spec homelab.UserSpec) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.calls = append(c.f.calls, "CreateUser "+spec.Name)
	c.f.users = append(c.f.users, homelab.User{ID: len(c.f.users) + 1, UID: spec.UID, GID: spec.GID, Name: spec.Name})
	return nil
}

func (c *fakeConn) Groups(context.Context) ([]homelab.Group, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.f.groups), nil
}

func (c *fakeConn) CreateGroup(ctx context.Context, spec homelab.GroupSpec) error {
	c.f.mu.Lock()
	block, entered := c.f.block, c.f.entered
	c.f.mu.Unlock()
	if block != nil {
		close(entered)
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.calls = append(c.f.calls, "CreateGroup "+spec.Name)
	c.f.groups = append(c.f.groups, homelab.Group{ID: len(c.f.groups) + 1, GID: spec.GID, Name: spec.Name})
	return nil
}

func (c *fakeConn) Stat(_ context.Context, p string) (homelab.PathStat, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.f.stats[p], nil
}

func (c *fakeConn) SetPermissions(_ context.Context, spec homelab.PermissionSpec) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.calls = append(c.f.calls, "SetPermissions "+spec.Path)
	st := c.f.stats[spec.Path]
	st.UID, st.GID, st.Mode, st.ExtendedACL = spec.UID, spec.GID, spec.Mode, false
	c.f.stats[spec.Path] = st
	return nil
}

func (c *fakeConn) Networks(context.Context) ([]homelab.Network, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.f.networks), nil
}

func (c *fakeConn) Apps(context.Context) ([]homelab.App, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return slices.Clone(c.f.apps), nil
}

func (c *fakeConn) CreateApp(_ context.Context, spec homelab.AppSpec) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.calls = append(c.f.calls, "CreateApp "+spec.Name)
	c.f.apps = append(c.f.apps, homelab.App{Name: spec.Name, State: "RUNNING", Custom: true})
	for _, n := range homelab.HomelabNetworks {
		c.f.networks = append(c.f.networks, homelab.Network{Name: n, Driver: "bridge", ComposeProject: "ix-" + spec.Name})
	}
	return nil
}

// truenasEnv is a server wired to a fake TrueNAS, with storage roots under base.
type truenasEnv struct {
	*testEnv
	tn     *fakeTrueNAS
	base   string
	cookie cred
}

type truenasOptions struct {
	// noDialer leaves Deps.TrueNAS nil.
	noDialer bool
	// selfApp overrides ORCH_SELF_APP.
	selfApp string
	// globalEnv, when set, replaces compose/.global.env; "-" deletes it.
	globalEnv func(base string) string
}

// newTrueNASEnv starts a server whose compose/.global.env points at storage under a temp dir,
// which the fake TrueNAS reports as pool tank, logs in, and returns it.
func newTrueNASEnv(t *testing.T, o truenasOptions) *truenasEnv {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	tn := newFakeTrueNAS(base)
	selfApp := homelab.DefaultSelfApp
	if o.selfApp != "" {
		selfApp = o.selfApp
	}
	opts := envOptions{
		prepareRepo: func(t *testing.T, root string) {
			t.Helper()
			p := filepath.Join(root, "compose", ".global.env")
			content := "PUID=1000\nPGID=1000\nHOMELAB_STORAGE=" + base + "/apps/homelab\nAPPCONFIG_STORAGE=" + base +
				"/apps/appconfigs\nDATABASE_STORAGE=" + base + "/databases\nMEDIA_STORAGE=" + base + "/media\n"
			if o.globalEnv != nil {
				content = o.globalEnv(base)
			}
			if content == "-" {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		deps: func(d *Deps) {
			if !o.noDialer {
				d.TrueNAS = tn.dial
			}
			d.TrueNASURL = "wss://truenas.test"
			d.Credentials = d.Store
			d.Environment = homelab.EnvironmentConfig{SelfApp: selfApp, StateStorage: filepath.Join(base, "state")}
		},
	}
	env := newEnv(t, opts)
	env.setup(t)
	return &truenasEnv{testEnv: env, tn: tn, base: base, cookie: withCookie(env.login(t))}
}

// saveKey stores the test key through put-truenas-settings.
func (e *truenasEnv) saveKey(t *testing.T) {
	t.Helper()
	r := e.do(t, http.MethodPut, "/api/truenas/settings", map[string]string{"username": "truenas_admin", "api_key": testAPIKey}, e.cookie)
	if r.status != http.StatusOK {
		t.Fatalf("PUT /api/truenas/settings status = %d, want 200; body %s", r.status, r.body)
	}
}

// environment gets the readiness view.
func (e *truenasEnv) environment(t *testing.T) EnvironmentView {
	t.Helper()
	r := e.do(t, http.MethodGet, "/api/environment", nil, e.cookie)
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/environment status = %d, want 200; body %s", r.status, r.body)
	}
	var v EnvironmentView
	decode(t, r, &v)
	return v
}

// assertNoKey fails when the test key appears in s.
func assertNoKey(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, testAPIKey) {
		t.Errorf("the API key appears in %s", where)
	}
}
