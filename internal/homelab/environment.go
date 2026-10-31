package homelab

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// GlobalEnvPath is the repo-relative path of the env file every stack shares.
const GlobalEnvPath = "compose/.global.env"

// DefaultSelfApp is the TrueNAS app name the orchestrator runs as unless configured otherwise.
const DefaultSelfApp = "truenas-orchestrator"

// HomelabAccount is the name the bootstrap gives the group and user it creates (D32).
const HomelabAccount = "homelab"

// homelabFullName is the display name of the user the bootstrap creates.
const homelabFullName = "Homelab containers (truenas-orchestrator)"

// The account ids the bootstrap may create: TrueNAS keeps 1000-2999 free for this
// (MIN_AUTO_XID = 3000), and 950 is truenas_admin.
const (
	minHomelabID   = 1000
	maxHomelabID   = 2999
	truenasAdminID = 950
)

// Expected modes of the storage roots and the state dataset (D32).
const (
	rootMode  FileMode = 0o750
	stateMode FileMode = 0o700
)

// globalEnvKeys are the keys compose/.global.env must define for the bootstrap (B10).
var globalEnvKeys = []string{
	"HOMELAB_STORAGE", "APPCONFIG_STORAGE", "DATABASE_STORAGE", "MEDIA_STORAGE", "PUID", "PGID",
}

// EnvironmentConfig is what the environment-readiness check starts from: the orchestrator's own
// configuration and compose/.global.env.
type EnvironmentConfig struct {
	// SelfApp is the orchestrator's own TrueNAS app name (ORCH_SELF_APP), which is never modified.
	SelfApp string
	// HomelabStorage is the injected HOMELAB_STORAGE. When non-empty it wins over .global.env, as
	// it does everywhere else (D31).
	HomelabStorage string
	// StateStorage is the host path of the orchestrator's state dataset (ORCH_STATE_STORAGE).
	StateStorage string
	// Global is compose/.global.env.
	Global DotEnv
	// CheckIdentity enables the identity check: ProcessUID and ProcessGID, the uid and gid the
	// orchestrator process runs as, are compared with PUID and PGID (D32). `serve` sets all three
	// from the process for a real TrueNAS; the zero value skips the check, as against the fake.
	CheckIdentity bool
	// ProcessUID and ProcessGID are read only when CheckIdentity is true.
	ProcessUID, ProcessGID int
}

// RootRole names one of the writable storage roots the bootstrap provisions.
type RootRole string

// Root roles.
const (
	RoleHomelab   RootRole = "homelab"   // $HOMELAB_STORAGE
	RoleAppConfig RootRole = "appconfig" // $APPCONFIG_STORAGE
	RoleDatabase  RootRole = "database"  // $DATABASE_STORAGE
)

// StorageRoot is one writable storage root and the dataset properties it is created with.
type StorageRoot struct {
	Role RootRole
	// Path is the absolute, cleaned host path.
	Path string
	// Props are the properties a new dataset at Path gets (D32).
	Props DatasetProps
}

// EnvironmentSpec is the resolved end state the readiness check compares TrueNAS against. Build
// it with NewEnvironmentSpec; a field whose input was invalid is zero.
type EnvironmentSpec struct {
	// SelfApp is the orchestrator's own app, never modified.
	SelfApp string
	// PUID and PGID are the homelab identity; -1 when not set or invalid.
	PUID, PGID int
	// Roots are the writable storage roots with valid paths, in the order homelab, appconfig,
	// database.
	Roots []StorageRoot
	// StateStorage is the orchestrator's state dataset path; "" when not set or invalid.
	StateStorage string
	// MediaStorage is $MEDIA_STORAGE; "" when not set or invalid.
	MediaStorage string
	// CheckIdentity, ProcessUID and ProcessGID are EnvironmentConfig's.
	CheckIdentity          bool
	ProcessUID, ProcessGID int

	// problems forbid running; globalProblems is the subset about compose/.global.env.
	problems       []string
	globalProblems []string
}

// datasetProps returns the properties a storage root dataset is created with (D32).
func datasetProps(role RootRole) DatasetProps {
	p := DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	if role == RoleDatabase {
		p.RecordSize = "16K"
	}
	return p
}

// NewEnvironmentSpec resolves cfg into the end state the bootstrap converges on. The second result
// lists the problems that forbid a bootstrap run, by key name and path, never by any other value;
// CheckEnvironment reports them again in Readiness.Problems.
func NewEnvironmentSpec(cfg EnvironmentConfig) (spec EnvironmentSpec, problems []string) {
	spec = EnvironmentSpec{
		SelfApp: cfg.SelfApp, PUID: -1, PGID: -1,
		CheckIdentity: cfg.CheckIdentity, ProcessUID: cfg.ProcessUID, ProcessGID: cfg.ProcessGID,
	}
	global := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		spec.globalProblems = append(spec.globalProblems, msg)
		spec.problems = append(spec.problems, msg)
	}
	config := func(format string, args ...any) {
		spec.problems = append(spec.problems, fmt.Sprintf(format, args...))
	}

	// Every value read here is a path or an id, which plans and the audit log show.
	values := map[string]string{}
	for _, key := range globalEnvKeys {
		e, ok := cfg.Global.Lookup(key)
		v := ""
		if ok {
			v = strings.TrimSpace(e.Value.Reveal())
		}
		if v == "" {
			global("%s does not define %s", GlobalEnvPath, key)
			continue
		}
		values[key] = v
	}
	homelabStorage := strings.TrimSpace(cfg.HomelabStorage)
	if homelabStorage != "" {
		values["HOMELAB_STORAGE"] = homelabStorage
	}
	for _, key := range []string{"PUID", "PGID"} {
		raw, ok := values[key]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			global("%s %s must be a non-negative integer", GlobalEnvPath, key)
			continue
		}
		if key == "PUID" {
			spec.PUID = n
		} else {
			spec.PGID = n
		}
	}

	type namedPath struct{ key, path string }
	var paths []namedPath
	clean := func(key, raw string, problem func(string, ...any)) string {
		if raw == "" {
			return ""
		}
		if !path.IsAbs(raw) || slices.Contains(strings.Split(raw, "/"), "..") || path.Clean(raw) == "/" {
			problem("%s %s must be an absolute path other than / without .. segments", key, raw)
			return ""
		}
		c := path.Clean(raw)
		paths = append(paths, namedPath{key, c})
		return c
	}
	for _, r := range []struct {
		key  string
		role RootRole
	}{{"HOMELAB_STORAGE", RoleHomelab}, {"APPCONFIG_STORAGE", RoleAppConfig}, {"DATABASE_STORAGE", RoleDatabase}} {
		problem := global
		if r.key == "HOMELAB_STORAGE" && homelabStorage != "" {
			problem = config
		}
		if p := clean(r.key, values[r.key], problem); p != "" {
			spec.Roots = append(spec.Roots, StorageRoot{Role: r.role, Path: p, Props: datasetProps(r.role)})
		}
	}
	spec.MediaStorage = clean("MEDIA_STORAGE", values["MEDIA_STORAGE"], global)
	if state := strings.TrimSpace(cfg.StateStorage); state == "" {
		config("ORCH_STATE_STORAGE is not set: the orchestrator needs its state dataset path")
	} else {
		spec.StateStorage = clean("ORCH_STATE_STORAGE", state, config)
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			switch {
			case a.path == b.path:
				config("%s and %s are the same path %s; every storage root must be distinct", a.key, b.key, a.path)
			case isUnder(a.path, b.path) || isUnder(b.path, a.path):
				config("%s %s and %s %s are nested; storage roots must not contain each other", a.key, a.path, b.key, b.path)
			}
		}
	}

	if cfg.SelfApp == NetworksApp {
		config("ORCH_SELF_APP is %s, the app that owns the homelab networks: the orchestrator never modifies its own app, so it could not create them", NetworksApp)
	}
	return spec, slices.Clone(spec.problems)
}

// isUnder reports whether p is strictly inside dir. Both are clean absolute paths.
func isUnder(p, dir string) bool {
	return dir != p && strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// statPaths returns, in a fixed order, the paths whose state the check needs: each root, the state
// dataset and the media root.
func (s EnvironmentSpec) statPaths() []string {
	var out []string
	for _, r := range s.Roots {
		out = append(out, r.Path)
	}
	if s.StateStorage != "" {
		out = append(out, s.StateStorage)
	}
	if s.MediaStorage != "" {
		out = append(out, s.MediaStorage)
	}
	return out
}

// EnvironmentObserved is what TrueNAS reported, read by ObserveEnvironment. A read that failed
// leaves its error in the matching ...Err field, and the check reports the items that depended on
// it as unknown.
type EnvironmentObserved struct {
	Version     string
	VersionErr  error
	Docker      DockerStatus
	DockerErr   error
	Groups      []Group
	GroupsErr   error
	Users       []User
	UsersErr    error
	Datasets    []Dataset
	DatasetsErr error
	// Stats holds each path stat'ed, by path; StatErrs the paths whose stat failed.
	Stats       map[string]PathStat
	StatErrs    map[string]error
	Networks    []Network
	NetworksErr error
	Apps        []App
	AppsErr     error
	// DeployKey is the state of the deploy key, which the caller observes through SetupHost
	// (ReadDeployKey) and sets: ItemOK, ItemMissing or ItemUnknown. Empty leaves the item out.
	DeployKey ItemState
}

// ObserveEnvironment reads, and never changes, everything CheckEnvironment needs, in a fixed
// order: version, Docker, groups, users, datasets, the stats, networks, apps. The stats are each
// storage root, the state dataset and the media root, and for a root that does not exist, its
// ancestors up to the dataset that would contain it, stopping at the first that exists. A failed
// read is recorded in the result, not returned; the error is only ctx's.
func ObserveEnvironment(ctx context.Context, spec EnvironmentSpec, tn TrueNAS) (EnvironmentObserved, error) {
	var obs EnvironmentObserved
	obs.Version, obs.VersionErr = tn.SystemVersion(ctx)
	obs.Docker, obs.DockerErr = tn.DockerStatus(ctx)
	obs.Groups, obs.GroupsErr = tn.Groups(ctx)
	obs.Users, obs.UsersErr = tn.Users(ctx)
	obs.Datasets, obs.DatasetsErr = tn.Datasets(ctx)
	obs.Stats, obs.StatErrs = map[string]PathStat{}, map[string]error{}
	stat := func(p string) (PathStat, bool) {
		if st, ok := obs.Stats[p]; ok {
			return st, true
		}
		if _, failed := obs.StatErrs[p]; failed {
			return PathStat{}, false
		}
		st, err := tn.Stat(ctx, p)
		if err != nil {
			obs.StatErrs[p] = err
			return PathStat{}, false
		}
		obs.Stats[p] = st
		return st, true
	}
	for _, p := range spec.statPaths() {
		st, ok := stat(p)
		if !ok || st.Exists || obs.DatasetsErr != nil {
			continue
		}
		parent, found := containingDataset(obs.Datasets, p)
		if !found || path.Clean(parent.Mountpoint) == p {
			continue
		}
		for a := path.Dir(p); a != path.Clean(parent.Mountpoint) && a != "/"; a = path.Dir(a) {
			if st, ok := stat(a); !ok || st.Exists {
				break
			}
		}
	}
	obs.Networks, obs.NetworksErr = tn.Networks(ctx)
	obs.Apps, obs.AppsErr = tn.Apps(ctx)
	if err := ctx.Err(); err != nil {
		return EnvironmentObserved{}, fmt.Errorf("observe the environment: %w", err)
	}
	return obs, nil
}

// bootPools are the names of TrueNAS boot pools: boot-pool, and freenas-boot on systems
// installed before TrueNAS SCALE renamed it.
var bootPools = []string{"boot-pool", "freenas-boot"}

// poolDataset reports whether d can hold a storage root: mounted, and not on a boot pool, whose
// root dataset is mounted at / and would otherwise contain every path.
func poolDataset(d Dataset) bool {
	if d.Mountpoint == "" || path.Clean(d.Mountpoint) == "/" {
		return false
	}
	pool, _, _ := strings.Cut(d.Name, "/")
	return !slices.Contains(bootPools, pool)
}

// isPoolRoot reports whether d is a pool's root dataset, such as "tank".
func isPoolRoot(d Dataset) bool { return !strings.Contains(d.Name, "/") }

// containingDataset returns the deepest pool dataset whose mountpoint is p or contains it.
func containingDataset(datasets []Dataset, p string) (Dataset, bool) {
	var best Dataset
	found := false
	for _, d := range datasets {
		if !poolDataset(d) {
			continue
		}
		mp := path.Clean(d.Mountpoint)
		if (mp == p || isUnder(p, mp)) && (!found || len(mp) > len(path.Clean(best.Mountpoint))) {
			best, found = d, true
		}
	}
	return best, found
}

// datasetAt returns the pool dataset mounted exactly at p.
func datasetAt(datasets []Dataset, p string) (Dataset, bool) {
	for _, d := range datasets {
		if poolDataset(d) && path.Clean(d.Mountpoint) == p {
			return d, true
		}
	}
	return Dataset{}, false
}
