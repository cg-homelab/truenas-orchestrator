package homelab_test

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// fakeTrueNAS is an in-memory homelab.TrueNAS whose mutations change its state the way the
// middleware does: a created dataset gets a mountpoint under its parent and a 0:0 0755 directory,
// SetPermissions rewrites the stat, and CreateApp adds the networks its compose declares,
// labelled with project ix-<app>. It records every mutating call.
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
	// errs makes every call of a method fail, by method name (e.g. "Groups", "CreateDataset").
	errs map[string]error
	// before runs at the start of every call with the method name, under no lock.
	before func(method string)
	// calls logs every mutating call, e.g. "CreateDataset tank/apps".
	calls []string
	// reads counts read calls by method.
	reads map[string]int
}

var _ homelab.TrueNAS = (*fakeTrueNAS)(nil)

// newFreshTrueNAS is a supported TrueNAS with Docker running, pool tank at /mnt/tank, the state
// dataset and media in place, and nothing else provisioned.
func newFreshTrueNAS() *fakeTrueNAS {
	return &fakeTrueNAS{
		version: "TrueNAS-25.10.1",
		docker:  homelab.DockerStatus{Status: "RUNNING", Description: "Application(s) are running"},
		groups:  []homelab.Group{{ID: 1, GID: 950, Name: "truenas_admin", Builtin: false}, {ID: 2, GID: 568, Name: "apps", Builtin: true}},
		users:   []homelab.User{{ID: 1, UID: 950, GID: 950, Name: "truenas_admin"}, {ID: 2, UID: 568, GID: 568, Name: "apps", Builtin: true}},
		datasets: []homelab.Dataset{
			{Name: "boot-pool/ROOT/25.10.1", Mountpoint: "/"},
			{Name: "tank", Mountpoint: "/mnt/tank", Props: homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}},
			{Name: "tank/state", Mountpoint: "/mnt/tank/state", Props: homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}},
		},
		stats: map[string]homelab.PathStat{
			"/mnt/tank":       {Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true},
			"/mnt/tank/state": {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o700, IsMountpoint: true},
			"/mnt/tank/media": {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o755},
		},
		apps:  []homelab.App{{Name: "truenas-orchestrator", State: "RUNNING", Custom: true}},
		errs:  map[string]error{},
		reads: map[string]int{},
	}
}

func (f *fakeTrueNAS) call(method string, mutating bool, target string) error {
	if f.before != nil {
		f.before(method)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if mutating {
		f.calls = append(f.calls, method+" "+target)
	} else {
		f.reads[method]++
	}
	return f.errs[method]
}

func (f *fakeTrueNAS) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeTrueNAS) SystemVersion(context.Context) (string, error) {
	if err := f.call("SystemVersion", false, ""); err != nil {
		return "", err
	}
	return f.version, nil
}

func (f *fakeTrueNAS) DockerStatus(context.Context) (homelab.DockerStatus, error) {
	if err := f.call("DockerStatus", false, ""); err != nil {
		return homelab.DockerStatus{}, err
	}
	return f.docker, nil
}

func (f *fakeTrueNAS) Datasets(context.Context) ([]homelab.Dataset, error) {
	if err := f.call("Datasets", false, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.datasets), nil
}

func (f *fakeTrueNAS) CreateDataset(_ context.Context, spec homelab.DatasetSpec) error {
	if err := f.call("CreateDataset", true, spec.Name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := strings.LastIndex(spec.Name, "/")
	if i < 0 {
		return fmt.Errorf("dataset %s has no parent: %w", spec.Name, homelab.ErrValidation)
	}
	parentName, leaf := spec.Name[:i], spec.Name[i+1:]
	idx := slices.IndexFunc(f.datasets, func(d homelab.Dataset) bool { return d.Name == parentName })
	if idx < 0 {
		return fmt.Errorf("parent of %s does not exist: %w", spec.Name, homelab.ErrNotFound)
	}
	if slices.ContainsFunc(f.datasets, func(d homelab.Dataset) bool { return d.Name == spec.Name }) {
		return fmt.Errorf("dataset %s exists: %w", spec.Name, homelab.ErrConflict)
	}
	parent := f.datasets[idx]
	mp := path.Join(parent.Mountpoint, leaf)
	if st, ok := f.stats[mp]; ok && st.Exists {
		return fmt.Errorf("path %s already exists: %w", mp, homelab.ErrValidation)
	}
	d := homelab.Dataset{Name: spec.Name, Mountpoint: mp, Props: parent.Props}
	if spec.Props != nil {
		d.Props = *spec.Props
	}
	f.datasets = append(f.datasets, d)
	f.stats[mp] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true}
	return nil
}

func (f *fakeTrueNAS) Users(context.Context) ([]homelab.User, error) {
	if err := f.call("Users", false, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.users), nil
}

func (f *fakeTrueNAS) CreateUser(_ context.Context, spec homelab.UserSpec) error {
	if err := f.call("CreateUser", true, spec.Name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.groups, func(g homelab.Group) bool { return g.GID == spec.GID }) {
		return fmt.Errorf("group %d: %w", spec.GID, homelab.ErrNotFound)
	}
	f.users = append(f.users, homelab.User{ID: len(f.users) + 1, UID: spec.UID, GID: spec.GID, Name: spec.Name})
	return nil
}

func (f *fakeTrueNAS) Groups(context.Context) ([]homelab.Group, error) {
	if err := f.call("Groups", false, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.groups), nil
}

func (f *fakeTrueNAS) CreateGroup(_ context.Context, spec homelab.GroupSpec) error {
	if err := f.call("CreateGroup", true, spec.Name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = append(f.groups, homelab.Group{ID: len(f.groups) + 1, GID: spec.GID, Name: spec.Name})
	return nil
}

func (f *fakeTrueNAS) Stat(_ context.Context, p string) (homelab.PathStat, error) {
	if err := f.call("Stat", false, ""); err != nil {
		return homelab.PathStat{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats[p], nil
}

func (f *fakeTrueNAS) SetPermissions(_ context.Context, spec homelab.PermissionSpec) error {
	if err := f.call("SetPermissions", true, spec.Path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.stats[spec.Path]
	if !ok || !st.Exists {
		return fmt.Errorf("path %s: %w", spec.Path, homelab.ErrNotFound)
	}
	st.UID, st.GID, st.Mode, st.ExtendedACL = spec.UID, spec.GID, spec.Mode, false
	f.stats[spec.Path] = st
	return nil
}

func (f *fakeTrueNAS) Networks(context.Context) ([]homelab.Network, error) {
	if err := f.call("Networks", false, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.networks), nil
}

func (f *fakeTrueNAS) Apps(context.Context) ([]homelab.App, error) {
	if err := f.call("Apps", false, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.apps), nil
}

func (f *fakeTrueNAS) CreateApp(_ context.Context, spec homelab.AppSpec) error {
	if err := f.call("CreateApp", true, spec.Name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps = append(f.apps, homelab.App{Name: spec.Name, State: "RUNNING", Custom: true})
	if spec.Compose == homelab.NetworksAppCompose {
		for _, n := range homelab.HomelabNetworks {
			f.networks = append(f.networks, homelab.Network{Name: n, ID: "id-" + n, Driver: "bridge", ComposeProject: "ix-" + spec.Name})
		}
	}
	return nil
}

// addNetworks adds networks by name with the given compose project label.
func (f *fakeTrueNAS) addNetworks(project string, names ...string) {
	for _, n := range names {
		f.networks = append(f.networks, homelab.Network{Name: n, ID: "id-" + n, Driver: "bridge", ComposeProject: project})
	}
}
