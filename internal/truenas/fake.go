package truenas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"go.yaml.in/yaml/v3"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Fake is a stateful in-process homelab.TrueNAS, seeded from a scenario under
// fixtures/truenas/scenarios (LoadScenario, FakeDialer). Mutations change its state the way the
// middleware would, a scenario's errors fire through the same *Error decoding as the client's,
// and every mutating call that reaches it is recorded (Calls).
//
// The zero value is not usable: build one with LoadScenario or FakeDialer. Sessions from one
// FakeDialer share state, as sessions on one server do. A Fake is safe for concurrent use.
type Fake struct {
	state  *fakeState
	closed atomic.Bool
}

var _ homelab.TrueNAS = (*Fake)(nil)

type fakeState struct {
	mu       sync.Mutex
	scenario string
	version  string
	login    string // auth.login_ex response_type
	selfApp  string
	docker   homelab.DockerStatus
	datasets []homelab.Dataset
	users    []homelab.User
	groups   []homelab.Group
	stat     map[string]homelab.PathStat
	networks []homelab.Network
	apps     []homelab.App
	errors   map[string]*fakeError
	calls    []string
	lastID   int
}

type fakeError struct {
	once  bool
	fired bool
	wire  wireError
}

// LoadScenario returns a Fake seeded from scenarios/<name>.json in fsys (the fixtures/truenas
// directory). It skips the login and version gate; FakeDialer runs them.
func LoadScenario(fsys fs.FS, name string, opts FakeOptions) (*Fake, error) {
	st, err := loadState(fsys, name, opts)
	if err != nil {
		return nil, err
	}
	return &Fake{state: st}, nil
}

// FakeDialer returns a dial function with Dial's behaviour over one shared Fake state: the
// scenario's auth field fails the login with the same errors as Dial (an empty username or key is
// AUTH_ERR), its version goes through the same homelab.CheckTrueNASVersion gate, and scenario
// errors on auth.login_ex, system.version and core.set_options fail the dial as they would fail
// Dial's handshake. The scenario is loaded on the first dial.
func FakeDialer(fsys fs.FS, name string, opts FakeOptions) func(ctx context.Context, creds homelab.TrueNASCredentials) (*Fake, error) {
	var (
		mu sync.Mutex
		st *fakeState
	)
	return func(ctx context.Context, creds homelab.TrueNASCredentials) (*Fake, error) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("truenas fake: dial: %w", err)
		}
		mu.Lock()
		if st == nil {
			loaded, err := loadState(fsys, name, opts)
			if err != nil {
				mu.Unlock()
				return nil, err
			}
			st = loaded
		}
		s := st
		mu.Unlock()

		if err := s.handshake(creds); err != nil {
			return nil, err
		}
		return &Fake{state: s}, nil
	}
}

// handshake runs Dial's login, version and set_options steps against the scenario.
func (s *fakeState) handshake(creds homelab.TrueNASCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fire("auth.login_ex"); err != nil {
		return err
	}
	login := s.login
	// The Fake checks only that a key is present; it never compares or stores one.
	if creds.Username == "" || creds.APIKey.Reveal() == "" {
		login = "AUTH_ERR"
	}
	if err := loginResult(login, creds.Username); err != nil {
		return err
	}
	if err := s.fire("system.version"); err != nil {
		return err
	}
	if err := homelab.CheckTrueNASVersion(s.version); err != nil {
		return err
	}
	return s.fire("core.set_options")
}

// fire returns the scenario error for method, if one applies, and marks a once error spent. The
// caller holds s.mu.
func (s *fakeState) fire(method string) error {
	if e := s.errors[method]; e != nil && (!e.once || !e.fired) {
		e.fired = true
		return newError(method, &e.wire)
	}
	return nil
}

// Close ends this session; later calls on it fail with homelab.ErrUnavailable. The shared state
// stays for the next dial.
func (f *Fake) Close() error {
	f.closed.Store(true)
	return nil
}

// Calls returns the mutating calls that reached the Fake, in order, as "<method> <target>".
func (f *Fake) Calls() []string {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	return slices.Clone(f.state.calls)
}

// with runs fn under the state lock after the session, context and scenario-error checks. A
// non-empty target records the call as a mutation before the scenario errors fire, because the
// call reached the server.
func (f *Fake) with(ctx context.Context, method, target string, fn func(s *fakeState) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("truenas fake: %s: %w", method, err)
	}
	if f.closed.Load() {
		return fmt.Errorf("truenas fake: %s: session closed: %w", method, homelab.ErrUnavailable)
	}
	s := f.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if target != "" {
		s.calls = append(s.calls, method+" "+target)
	}
	if err := s.fire(method); err != nil {
		return err
	}
	return fn(s)
}

func (s *fakeState) nextID() int {
	s.lastID++
	return s.lastID
}

// validationError builds the error ValidationErrors produces on the wire, through newError.
func validationError(method, field, msg string, errno int) error {
	data, _ := json.Marshal(map[string]any{ // plain map of strings and ints; cannot fail
		"error":   errnoInval,
		"errname": "EINVAL",
		"reason":  fmt.Sprintf("[%s] %s: %s", errnoName(errno), field, msg),
		"extra":   [][]any{{field, msg, errno}},
		"trace":   nil,
	})
	return newError(method, &wireError{Code: codeInvalidParams, Message: "Invalid params", Data: data})
}

// callError builds the error a CallError produces on the wire.
func callError(method, reason string, errno int) error {
	data, _ := json.Marshal(map[string]any{ // plain map of strings and ints; cannot fail
		"error": errno, "errname": errnoName(errno), "reason": reason, "extra": nil, "trace": nil,
	})
	return newError(method, &wireError{Code: codeMethodCallError, Message: "Method call error", Data: data})
}

// SystemVersion returns the scenario's version.
func (f *Fake) SystemVersion(ctx context.Context) (string, error) {
	var v string
	err := f.with(ctx, "system.version", "", func(s *fakeState) error {
		v = s.version
		return nil
	})
	return v, err
}

// DockerStatus returns the scenario's docker.status.
func (f *Fake) DockerStatus(ctx context.Context) (homelab.DockerStatus, error) {
	var d homelab.DockerStatus
	err := f.with(ctx, "docker.status", "", func(s *fakeState) error {
		d = s.docker
		return nil
	})
	return d, err
}

// Datasets returns every dataset.
func (f *Fake) Datasets(ctx context.Context) ([]homelab.Dataset, error) {
	var out []homelab.Dataset
	err := f.with(ctx, "pool.dataset.query", "", func(s *fakeState) error {
		out = slices.Clone(s.datasets)
		return nil
	})
	return out, err
}

// CreateDataset adds a dataset under an existing, unlocked parent, mounted below the parent's
// mountpoint, with a stat entry 0:0 0755. A nil spec.Props inherits the parent's properties.
func (f *Fake) CreateDataset(ctx context.Context, spec homelab.DatasetSpec) error {
	const method = "pool.dataset.create"
	return f.with(ctx, method, spec.Name, func(s *fakeState) error {
		const field = "pool_dataset_create.name"
		if s.dataset(spec.Name) != nil {
			return validationError(method, field, fmt.Sprintf("Dataset %s already exists", spec.Name), errnoExist)
		}
		parentName, base := path.Split(spec.Name)
		parentName = strings.TrimSuffix(parentName, "/")
		parent := s.dataset(parentName)
		if parentName == "" || parent == nil {
			return validationError(method, field, fmt.Sprintf("Parent dataset %s does not exist", parentName), errnoNoEnt)
		}
		if parent.Locked {
			return validationError(method, field, fmt.Sprintf("Parent dataset %s is locked", parentName), errnoInval)
		}
		d := homelab.Dataset{Name: spec.Name, Encrypted: parent.Encrypted, Props: parent.Props}
		if spec.Props != nil {
			d.Props = *spec.Props
		}
		if parent.Mountpoint != "" {
			d.Mountpoint = parent.Mountpoint + "/" + base
			if st, ok := s.stat[d.Mountpoint]; ok && st.Exists {
				return validationError(method, field, fmt.Sprintf("Path %s already exists", d.Mountpoint), errnoInval)
			}
			s.stat[d.Mountpoint] = homelab.PathStat{
				Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true,
			}
		}
		s.datasets = append(s.datasets, d)
		return nil
	})
}

func (s *fakeState) dataset(name string) *homelab.Dataset {
	for i := range s.datasets {
		if s.datasets[i].Name == name {
			return &s.datasets[i]
		}
	}
	return nil
}

// Users returns the local users.
func (f *Fake) Users(ctx context.Context) ([]homelab.User, error) {
	var out []homelab.User
	err := f.with(ctx, "user.query", "", func(s *fakeState) error {
		out = slices.Clone(s.users)
		return nil
	})
	return out, err
}

// CreateUser resolves spec.GID like Client (a missing group never reaches the server), then adds
// the user unless the name or uid is taken.
func (f *Fake) CreateUser(ctx context.Context, spec homelab.UserSpec) error {
	const method = "user.create"
	groups, err := f.Groups(ctx)
	if err != nil {
		return err
	}
	if _, err := groupRowID(groups, spec); err != nil {
		return err
	}
	return f.with(ctx, method, spec.Name, func(s *fakeState) error {
		for _, u := range s.users {
			if u.Name == spec.Name {
				return validationError(method, "user_create.username",
					fmt.Sprintf("The username %q already exists.", spec.Name), errnoExist)
			}
			if u.UID == spec.UID {
				return validationError(method, "user_create.uid", fmt.Sprintf("Uid %d is already used.", spec.UID), errnoExist)
			}
		}
		s.users = append(s.users, homelab.User{ID: s.nextID(), UID: spec.UID, GID: spec.GID, Name: spec.Name})
		return nil
	})
}

// Groups returns the local groups.
func (f *Fake) Groups(ctx context.Context) ([]homelab.Group, error) {
	var out []homelab.Group
	err := f.with(ctx, "group.query", "", func(s *fakeState) error {
		out = slices.Clone(s.groups)
		return nil
	})
	return out, err
}

// CreateGroup adds the group unless the name or gid is taken.
func (f *Fake) CreateGroup(ctx context.Context, spec homelab.GroupSpec) error {
	const method = "group.create"
	return f.with(ctx, method, spec.Name, func(s *fakeState) error {
		for _, g := range s.groups {
			if g.Name == spec.Name {
				return validationError(method, "group_create.name",
					fmt.Sprintf("A group with the name %q already exists.", spec.Name), errnoExist)
			}
			if g.GID == spec.GID {
				return validationError(method, "group_create.gid", fmt.Sprintf("Gid %d is already used.", spec.GID), errnoExist)
			}
		}
		s.groups = append(s.groups, homelab.Group{ID: s.nextID(), GID: spec.GID, Name: spec.Name})
		return nil
	})
}

// Stat returns the seeded or created stat for p; absent is PathStat{} and nil, and a relative
// path is a validation error, as on the server.
func (f *Fake) Stat(ctx context.Context, p string) (homelab.PathStat, error) {
	const method = "filesystem.stat"
	var out homelab.PathStat
	err := f.with(ctx, method, "", func(s *fakeState) error {
		if !path.IsAbs(p) {
			return validationError(method, "filesystem.stat.path", "path must be absolute", errnoInval)
		}
		out = s.stat[path.Clean(p)]
		return nil
	})
	return out, err
}

// SetPermissions rewrites the stat of an existing path: owner, mode, no extended ACL.
func (f *Fake) SetPermissions(ctx context.Context, spec homelab.PermissionSpec) error {
	const method = "filesystem.setperm"
	if err := checkPermissionSpec(spec); err != nil {
		return err
	}
	return f.with(ctx, method, spec.Path, func(s *fakeState) error {
		p := path.Clean(spec.Path)
		st, ok := s.stat[p]
		if !ok || !st.Exists {
			return callError(method, fmt.Sprintf("Path %s not found", spec.Path), errnoNoEnt)
		}
		st.UID, st.GID, st.Mode, st.ExtendedACL = spec.UID, spec.GID, spec.Mode, false
		s.stat[p] = st
		return nil
	})
}

// Networks returns the homelab networks that exist, like Client's filtered query.
func (f *Fake) Networks(ctx context.Context) ([]homelab.Network, error) {
	var out []homelab.Network
	err := f.with(ctx, "docker.network.query", "", func(s *fakeState) error {
		out = []homelab.Network{}
		for _, n := range s.networks {
			if slices.Contains(homelab.HomelabNetworks, n.Name) {
				out = append(out, n)
			}
		}
		return nil
	})
	return out, err
}

// Apps returns the installed apps.
func (f *Fake) Apps(ctx context.Context) ([]homelab.App, error) {
	var out []homelab.App
	err := f.with(ctx, "app.query", "", func(s *fakeState) error {
		out = slices.Clone(s.apps)
		return nil
	})
	return out, err
}

// composeNetworks is the part of a compose file CreateApp reads.
type composeNetworks struct {
	Networks map[string]*struct {
		Name     string `yaml:"name"`
		Driver   string `yaml:"driver"`
		External bool   `yaml:"external"`
	} `yaml:"networks"`
}

// CreateApp refuses the orchestrator's own app like Client (it never reaches the server), then
// adds a RUNNING custom app and the networks its compose declares, labelled with compose project
// ix-<app>. A network that already exists by name is left alone.
func (f *Fake) CreateApp(ctx context.Context, spec homelab.AppSpec) error {
	const method = "app.create"
	if err := homelab.CheckAppMutation(f.state.selfAppName(), spec.Name); err != nil {
		return err
	}
	return f.with(ctx, method, spec.Name, func(s *fakeState) error {
		for _, a := range s.apps {
			if a.Name == spec.Name {
				return validationError(method, "app_create.app_name",
					fmt.Sprintf("Application with name %s already exists", spec.Name), errnoExist)
			}
		}
		var c composeNetworks
		if err := yaml.Unmarshal([]byte(spec.Compose), &c); err != nil {
			return validationError(method, "app_create.custom_compose_config_string", "Invalid YAML provided", errnoInval)
		}
		project := "ix-" + spec.Name
		keys := make([]string, 0, len(c.Networks))
		for k := range c.Networks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n := c.Networks[k]
			name, driver := project+"_"+k, "bridge"
			if n != nil {
				if n.External {
					continue
				}
				if n.Name != "" {
					name = n.Name
				}
				if n.Driver != "" {
					driver = n.Driver
				}
			}
			if slices.ContainsFunc(s.networks, func(x homelab.Network) bool { return x.Name == name }) {
				continue
			}
			sum := sha256.Sum256([]byte(project + "/" + name))
			s.networks = append(s.networks, homelab.Network{
				Name: name, ID: hex.EncodeToString(sum[:]), Driver: driver, ComposeProject: project,
			})
		}
		s.apps = append(s.apps, homelab.App{Name: spec.Name, State: "RUNNING", Custom: true})
		return nil
	})
}

func (s *fakeState) selfAppName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selfApp
}
