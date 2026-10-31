package homelab_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// testGlobal is a compose/.global.env whose roots sit on the fake's pool tank.
const testGlobal = `PUID=1000
PGID=1000
HOMELAB_STORAGE=/mnt/tank/apps/homelab
APPCONFIG_STORAGE=/mnt/tank/apps/appconfigs
DATABASE_STORAGE=/mnt/tank/databases
MEDIA_STORAGE=/mnt/tank/media
`

func parseGlobal(t *testing.T, src string) homelab.DotEnv {
	t.Helper()
	d, err := homelab.ParseDotEnv([]byte(src))
	if err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}
	return d
}

func testConfig(t *testing.T) homelab.EnvironmentConfig {
	t.Helper()
	return homelab.EnvironmentConfig{
		SelfApp: homelab.DefaultSelfApp, StateStorage: "/mnt/tank/state", Global: parseGlobal(t, testGlobal),
	}
}

func testSpec(t *testing.T, cfg homelab.EnvironmentConfig) homelab.EnvironmentSpec {
	t.Helper()
	spec, problems := homelab.NewEnvironmentSpec(cfg)
	if len(problems) > 0 {
		t.Fatalf("NewEnvironmentSpec problems = %q, want none", problems)
	}
	return spec
}

// check observes tn and checks it against spec.
func check(t *testing.T, spec homelab.EnvironmentSpec, tn homelab.TrueNAS) homelab.Readiness {
	t.Helper()
	obs, err := homelab.ObserveEnvironment(t.Context(), spec, tn)
	if err != nil {
		t.Fatalf("ObserveEnvironment: %v", err)
	}
	return homelab.CheckEnvironment(spec, obs)
}

// itemsByState lists "kind target" per item state, for compact assertions.
func itemsByState(r homelab.Readiness) map[homelab.ItemState][]string {
	out := map[homelab.ItemState][]string{}
	for _, it := range r.Items {
		label := string(it.Kind)
		if it.Target != "" {
			label += " " + it.Target
		}
		out[it.State] = append(out[it.State], label)
	}
	return out
}

func stepKinds(steps []homelab.BootstrapStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, string(s.Kind)+" "+s.Target)
	}
	return out
}

func rootDetail(mountpoint, role, recordsize string) map[string]string {
	return map[string]string{
		"mountpoint": mountpoint, "role": role, "acltype": "POSIX", "aclmode": "DISCARD", "atime": "OFF",
		"compression": "LZ4", "recordsize": recordsize,
	}
}

func newPerm(path string) homelab.BootstrapStep {
	return homelab.BootstrapStep{
		Kind: homelab.StepPermissionsSet, Target: path, Action: homelab.BootstrapCreate,
		Detail: map[string]string{
			"from_owner": "", "from_mode": "", "from_acl": "", "to_owner": "1000:1000", "to_mode": "0750", "stripacl": "true",
		},
	}
}

func TestBootstrapFreshServer(t *testing.T) {
	tn := newFreshTrueNAS()
	spec := testSpec(t, testConfig(t))

	got := check(t, spec, tn)
	wantSteps := []homelab.BootstrapStep{
		{Kind: homelab.StepGroupCreate, Target: "homelab", Action: homelab.BootstrapCreate, Detail: map[string]string{"gid": "1000"}},
		{Kind: homelab.StepUserCreate, Target: "homelab", Action: homelab.BootstrapCreate, Detail: map[string]string{"uid": "1000", "gid": "1000"}},
		{Kind: homelab.StepDatasetCreate, Target: "tank/apps", Action: homelab.BootstrapCreate, Detail: map[string]string{"mountpoint": "/mnt/tank/apps", "props": "inherit"}},
		{Kind: homelab.StepDatasetCreate, Target: "tank/apps/homelab", Action: homelab.BootstrapCreate, Detail: rootDetail("/mnt/tank/apps/homelab", "homelab", "128K")},
		{Kind: homelab.StepDatasetCreate, Target: "tank/apps/appconfigs", Action: homelab.BootstrapCreate, Detail: rootDetail("/mnt/tank/apps/appconfigs", "appconfig", "128K")},
		{Kind: homelab.StepDatasetCreate, Target: "tank/databases", Action: homelab.BootstrapCreate, Detail: rootDetail("/mnt/tank/databases", "database", "16K")},
		newPerm("/mnt/tank/apps/homelab"),
		newPerm("/mnt/tank/apps/appconfigs"),
		newPerm("/mnt/tank/databases"),
		{Kind: homelab.StepAppCreate, Target: "homelab-networks", Action: homelab.BootstrapCreate, Detail: map[string]string{
			"networks": "home,proxy,isolated,databases", "compose_sha256": got.Steps[len(got.Steps)-1].Detail["compose_sha256"],
		}},
	}
	if diff := cmp.Diff(wantSteps, got.Steps); diff != "" {
		t.Errorf("fresh plan steps mismatch (-want +got):\n%s", diff)
	}
	if !got.Runnable || got.Ready || len(got.Problems) != 0 || len(got.Digest) != 64 {
		t.Errorf("fresh readiness runnable %v ready %v problems %q digest %q, want runnable, not ready, a digest",
			got.Runnable, got.Ready, got.Problems, got.Digest)
	}
	wantStates := map[homelab.ItemState][]string{
		homelab.ItemOK: {
			"version 25.10.1", "docker", "global_env compose/.global.env", "state_dataset /mnt/tank/state",
			"media /mnt/tank/media", "self_app truenas-orchestrator",
		},
		homelab.ItemMissing: {
			"group homelab", "user homelab", "dataset /mnt/tank/apps/homelab", "dataset /mnt/tank/apps/appconfigs",
			"dataset /mnt/tank/databases", "permissions /mnt/tank/apps/homelab", "permissions /mnt/tank/apps/appconfigs",
			"permissions /mnt/tank/databases", "networks home,proxy,isolated,databases", "networks_app homelab-networks",
		},
	}
	if diff := cmp.Diff(wantStates, itemsByState(got)); diff != "" {
		t.Errorf("fresh item states mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(homelab.VersionStatus{Detected: "25.10.1", Supported: "TrueNAS 25.10 through 27.x", OK: true}, got.Version); diff != "" {
		t.Errorf("version mismatch (-want +got):\n%s", diff)
	}
	if m := tn.mutations(); len(m) != 0 {
		t.Fatalf("checking mutated: %v", m)
	}

	var lines []string
	res, err := homelab.ApplyBootstrap(t.Context(), spec, tn, got.Digest, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if res.Created != 10 || res.Changed != 0 || res.Unchanged != 0 || len(res.Steps) != 10 {
		t.Errorf("result %s with %d steps, want 10 created", res.Summary(), len(res.Steps))
	}
	wantCalls := []string{
		"CreateGroup homelab", "CreateUser homelab", "CreateDataset tank/apps", "CreateDataset tank/apps/homelab",
		"CreateDataset tank/apps/appconfigs", "CreateDataset tank/databases", "SetPermissions /mnt/tank/apps/homelab",
		"SetPermissions /mnt/tank/apps/appconfigs", "SetPermissions /mnt/tank/databases", "CreateApp homelab-networks",
	}
	if diff := cmp.Diff(wantCalls, tn.mutations()); diff != "" {
		t.Errorf("mutations mismatch (-want +got):\n%s", diff)
	}
	wantLines := []string{
		"create group homelab gid 1000",
		"create user homelab uid 1000 gid 1000",
		"create dataset tank/apps at /mnt/tank/apps",
		"create dataset tank/apps/homelab at /mnt/tank/apps/homelab recordsize 128K",
		"create dataset tank/apps/appconfigs at /mnt/tank/apps/appconfigs recordsize 128K",
		"create dataset tank/databases at /mnt/tank/databases recordsize 16K",
		"create permissions /mnt/tank/apps/homelab 1000:1000 0750",
		"create permissions /mnt/tank/apps/appconfigs 1000:1000 0750",
		"create permissions /mnt/tank/databases 1000:1000 0750",
		"create app homelab-networks (networks home,proxy,isolated,databases)",
		"10 created, 0 changed, 0 unchanged",
	}
	if diff := cmp.Diff(wantLines, lines); diff != "" {
		t.Errorf("report lines mismatch (-want +got):\n%s", diff)
	}

	// Criterion 4: the end state needs nothing more, and a second run changes nothing.
	again := check(t, spec, tn)
	if len(again.Steps) != 0 || !again.Ready || !again.Runnable {
		t.Errorf("after apply: steps %v ready %v runnable %v problems %q, want no steps and ready",
			stepKinds(again.Steps), again.Ready, again.Runnable, again.Problems)
	}
	lines = nil
	res, err = homelab.ApplyBootstrap(t.Context(), spec, tn, again.Digest, func(l string) { lines = append(lines, l) })
	if err != nil || len(res.Steps) != 0 {
		t.Fatalf("second ApplyBootstrap = %+v, %v, want no steps", res, err)
	}
	if diff := cmp.Diff([]string{"nothing to do"}, lines); diff != "" {
		t.Errorf("second run lines mismatch (-want +got):\n%s", diff)
	}
	if n := len(tn.mutations()); n != len(wantCalls) {
		t.Errorf("second run made %d more mutations", n-len(wantCalls))
	}
}

// readyTrueNAS is a fresh TrueNAS after a bootstrap: everything in place.
func readyTrueNAS(t *testing.T) *fakeTrueNAS {
	t.Helper()
	tn := newFreshTrueNAS()
	spec := testSpec(t, testConfig(t))
	if _, err := homelab.ApplyBootstrap(t.Context(), spec, tn, "", nil); err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	tn.calls = nil
	return tn
}

func TestCheckEnvironmentScenarios(t *testing.T) {
	tests := []struct {
		name string
		// tn builds the server; cfg, when set, changes the config.
		tn  func(t *testing.T) *fakeTrueNAS
		cfg func(cfg *homelab.EnvironmentConfig)
		// wantSteps lists "kind target"; wantWrong and wantDiffers list "kind target" substrings
		// with the message substring each must carry.
		wantSteps    []string
		wantRunnable bool
		wantReady    bool
		wantState    map[string]homelab.ItemState // "kind target" -> state
		wantMessage  map[string]string            // "kind target" -> message substring
		wantAbsent   []string                     // "kind target" that must not be reported
	}{
		{
			name:         "ready",
			tn:           readyTrueNAS,
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantState: map[string]homelab.ItemState{"networks_app homelab-networks": homelab.ItemOK},
		},
		{
			name: "partial: one root with the wrong owner",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.stats["/mnt/tank/apps/appconfigs"] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o770, ExtendedACL: true}
				return tn
			},
			wantSteps:    []string{"permissions_set /mnt/tank/apps/appconfigs"},
			wantRunnable: true,
			wantState:    map[string]homelab.ItemState{"permissions /mnt/tank/apps/appconfigs": homelab.ItemMissing},
			wantMessage:  map[string]string{"permissions /mnt/tank/apps/appconfigs": "owned by 0:0 with mode 0770 with an extended ACL"},
		},
		{
			name: "unmanaged networks are accepted",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.addNetworks("", homelab.HomelabNetworks...)
				return tn
			},
			wantSteps: []string{
				"group_create homelab", "user_create homelab", "dataset_create tank/apps", "dataset_create tank/apps/homelab",
				"dataset_create tank/apps/appconfigs", "dataset_create tank/databases", "permissions_set /mnt/tank/apps/homelab",
				"permissions_set /mnt/tank/apps/appconfigs", "permissions_set /mnt/tank/databases",
			},
			wantRunnable: true,
			wantState:    map[string]homelab.ItemState{"networks home,proxy,isolated,databases": homelab.ItemOK},
		},
		{
			name: "mixed networks name the docker commands",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.networks = nil
				tn.apps = tn.apps[:1]
				tn.addNetworks("", "home", "proxy")
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{"networks home,proxy,isolated,databases": homelab.ItemWrong},
			wantMessage: map[string]string{
				"networks home,proxy,isolated,databases": "docker network create --driver bridge --internal isolated; docker network create --driver bridge databases",
			},
		},
		{
			name: "app-made networks partly missing need the app restarted",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.networks = tn.networks[:2]
				tn.apps[1].State = "STOPPED"
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{
				"networks home,proxy,isolated,databases": homelab.ItemWrong,
				"networks_app homelab-networks":          homelab.ItemWrong,
			},
			wantMessage: map[string]string{"networks home,proxy,isolated,databases": "start or redeploy it in Apps"},
		},
		{
			name: "networks app stopped with every network present is advisory",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.apps[1].State = "STOPPED"
				return tn
			},
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantState: map[string]homelab.ItemState{"networks_app homelab-networks": homelab.ItemDiffers},
		},
		{
			name: "name collision: homelab user with another uid",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.users = append(tn.users, homelab.User{ID: 9, UID: 1001, GID: 1001, Name: "homelab"})
				return tn
			},
			wantSteps: []string{
				"group_create homelab", "dataset_create tank/apps", "dataset_create tank/apps/homelab",
				"dataset_create tank/apps/appconfigs", "dataset_create tank/databases", "permissions_set /mnt/tank/apps/homelab",
				"permissions_set /mnt/tank/apps/appconfigs", "permissions_set /mnt/tank/databases", "app_create homelab-networks",
			},
			wantState:   map[string]homelab.ItemState{"user homelab": homelab.ItemWrong},
			wantMessage: map[string]string{"user homelab": "user homelab exists with uid 1001, not PUID 1000"},
		},
		{
			name: "an existing account outside the range is accepted",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.users = append(tn.users, homelab.User{ID: 9, UID: 3001, GID: 568, Name: "media"})
				return tn
			},
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.Global.Entries[0].Value = homelab.NewSecret("3001")
			},
			wantSteps:    []string{"permissions_set /mnt/tank/apps/homelab", "permissions_set /mnt/tank/apps/appconfigs", "permissions_set /mnt/tank/databases"},
			wantRunnable: true,
			wantState:    map[string]homelab.ItemState{"user media": homelab.ItemDiffers},
		},
		{
			name: "PUID outside the range with no account",
			tn:   func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.Global.Entries[0].Value = homelab.NewSecret("3001")
			},
			wantState:   map[string]homelab.ItemState{"user homelab": homelab.ItemWrong},
			wantMessage: map[string]string{"user homelab": "PUID 3001 is outside 1000-2999"},
		},
		{
			name: "PUID 950 is refused although truenas_admin holds it",
			tn:   func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.Global.Entries[0].Value = homelab.NewSecret("950")
			},
			wantState:   map[string]homelab.ItemState{"user 950": homelab.ItemWrong},
			wantMessage: map[string]string{"user 950": "truenas_admin"},
		},
		{
			name: "a plain directory ancestor blocks dataset creation",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.stats["/mnt/tank/apps"] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755}
				return tn
			},
			wantState: map[string]homelab.ItemState{
				"dataset /mnt/tank/apps/homelab":    homelab.ItemWrong,
				"dataset /mnt/tank/apps/appconfigs": homelab.ItemWrong,
				"dataset /mnt/tank/databases":       homelab.ItemMissing,
			},
			wantMessage: map[string]string{"dataset /mnt/tank/apps/homelab": "nearest existing ancestor /mnt/tank/apps is a plain directory"},
		},
		{
			name: "existing roots: symlink, plain directory, differing properties",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.stats["/mnt/tank/apps/homelab"] = homelab.PathStat{Exists: true, Type: homelab.PathSymlink}
				tn.datasets = slices.DeleteFunc(tn.datasets, func(d homelab.Dataset) bool { return d.Name == "tank/apps/appconfigs" })
				for i := range tn.datasets {
					if tn.datasets[i].Name == "tank/databases" {
						tn.datasets[i].Props.Compression = "ZSTD"
					}
				}
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{
				"dataset /mnt/tank/apps/homelab":    homelab.ItemWrong,
				"dataset /mnt/tank/apps/appconfigs": homelab.ItemDiffers,
				"dataset /mnt/tank/databases":       homelab.ItemDiffers,
			},
			wantMessage: map[string]string{
				"dataset /mnt/tank/apps/homelab":    "is a symlink",
				"dataset /mnt/tank/apps/appconfigs": "plain directory inside dataset tank/apps",
				"dataset /mnt/tank/databases":       "compression ZSTD (expected LZ4)",
			},
		},
		{
			name: "a locked parent dataset",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.datasets[1].Encrypted, tn.datasets[1].Locked = true, true
				return tn
			},
			wantState:   map[string]homelab.ItemState{"dataset /mnt/tank/databases": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /mnt/tank/databases": "locked dataset tank"},
		},
		{
			name: "a root outside any pool",
			tn:   func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.Global.Entries[4].Value = homelab.NewSecret("/srv/databases")
			},
			wantState:   map[string]homelab.ItemState{"dataset /srv/databases": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /srv/databases": "not under any mounted dataset"},
		},
		{
			name: "unsupported version and docker unconfigured",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.version = "TrueNAS-25.04.2.6"
				tn.docker = homelab.DockerStatus{Status: "UNCONFIGURED"}
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{"version 25.04.2.6": homelab.ItemWrong, "docker": homelab.ItemWrong},
			wantMessage: map[string]string{
				"version 25.04.2.6": "TrueNAS 25.04.2.6 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x",
				"docker":            "choose an apps pool",
			},
		},
		{
			name: "a failed read is unknown",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.errs["Groups"] = errors.New("truenas: group.query: EACCES: Not authorized")
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{"group homelab": homelab.ItemUnknown},
			wantMessage: map[string]string{
				"group homelab": "could not list groups: truenas: group.query: EACCES: Not authorized",
			},
		},
		{
			name: "a failed stat is unknown",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.errs["Stat"] = errors.New("EXDEV")
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{
				"dataset /mnt/tank/databases": homelab.ItemUnknown, "state_dataset /mnt/tank/state": homelab.ItemUnknown,
				"media /mnt/tank/media": homelab.ItemUnknown,
			},
		},
		{
			name: "state dataset absent blocks; media absent is advisory",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				delete(tn.stats, "/mnt/tank/state")
				delete(tn.stats, "/mnt/tank/media")
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{
				"state_dataset /mnt/tank/state": homelab.ItemWrong, "media /mnt/tank/media": homelab.ItemDiffers,
			},
		},
		{
			name: "review 1: a root at a pool's root dataset",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.datasets = append(tn.datasets, homelab.Dataset{Name: "fast", Mountpoint: "/mnt/fast"})
				tn.stats["/mnt/fast"] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true}
				return tn
			},
			cfg: func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[4].Value = homelab.NewSecret("/mnt/fast") },
			wantSteps: []string{
				"group_create homelab", "user_create homelab", "dataset_create tank/apps", "dataset_create tank/apps/homelab",
				"dataset_create tank/apps/appconfigs", "permissions_set /mnt/tank/apps/homelab",
				"permissions_set /mnt/tank/apps/appconfigs", "app_create homelab-networks",
			},
			wantState:   map[string]homelab.ItemState{"dataset /mnt/fast": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /mnt/fast": "a storage root must be a child dataset, not the pool root"},
		},
		{
			name: "review 1: HOMELAB_STORAGE at the pool root plans no permissions there",
			tn:   func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:  func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[2].Value = homelab.NewSecret("/mnt/tank") },
			wantSteps: []string{
				"group_create homelab", "user_create homelab", "dataset_create tank/apps", "dataset_create tank/apps/appconfigs",
				"dataset_create tank/databases", "permissions_set /mnt/tank/apps/appconfigs", "permissions_set /mnt/tank/databases",
				"app_create homelab-networks",
			},
			wantState:   map[string]homelab.ItemState{"dataset /mnt/tank": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /mnt/tank": "not the pool root"},
		},
		{
			name:        "review 1: /mnt is never a root",
			tn:          func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:         func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[4].Value = homelab.NewSecret("/mnt") },
			wantState:   map[string]homelab.ItemState{"dataset /mnt": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /mnt": "not the pool root"},
		},
		{
			name: "review 2: app-labelled networks partly missing without the app plan the app",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.networks = tn.networks[:2]
				tn.apps = tn.apps[:1]
				return tn
			},
			wantSteps:    []string{"app_create homelab-networks"},
			wantRunnable: true,
			wantState: map[string]homelab.ItemState{
				"networks home,proxy,isolated,databases": homelab.ItemMissing,
				"networks_app homelab-networks":          homelab.ItemMissing,
			},
			wantMessage: map[string]string{"networks home,proxy,isolated,databases": "isolated, databases are missing while home, proxy carry the ix-homelab-networks project label"},
		},
		{
			name: "review 2: app-labelled networks partly missing with apps unreadable",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.networks = tn.networks[:2]
				tn.errs["Apps"] = errors.New("EACCES")
				return tn
			},
			wantSteps: []string{},
			wantState: map[string]homelab.ItemState{"networks home,proxy,isolated,databases": homelab.ItemUnknown},
		},
		{
			name:        "review 3: PUID 0 is refused",
			tn:          func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:         func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[0].Value = homelab.NewSecret("0") },
			wantState:   map[string]homelab.ItemState{"user 0": homelab.ItemWrong},
			wantMessage: map[string]string{"user 0": "PUID 0 is root; choose an id between 1000 and 2999"},
		},
		{
			name:        "review 3: PGID 0 is refused",
			tn:          func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:         func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[1].Value = homelab.NewSecret("0") },
			wantState:   map[string]homelab.ItemState{"group 0": homelab.ItemWrong},
			wantMessage: map[string]string{"group 0": "PGID 0 is root"},
		},
		{
			name:        "review 3: PUID of a builtin account is refused",
			tn:          func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:         func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[0].Value = homelab.NewSecret("568") },
			wantState:   map[string]homelab.ItemState{"user apps": homelab.ItemWrong},
			wantMessage: map[string]string{"user apps": "PUID 568 is held by the builtin TrueNAS user apps"},
		},
		{
			name:        "review 3: PGID of a builtin group is refused",
			tn:          func(*testing.T) *fakeTrueNAS { return newFreshTrueNAS() },
			cfg:         func(cfg *homelab.EnvironmentConfig) { cfg.Global.Entries[1].Value = homelab.NewSecret("568") },
			wantState:   map[string]homelab.ItemState{"group apps": homelab.ItemWrong},
			wantMessage: map[string]string{"group apps": "PGID 568 is held by the builtin TrueNAS group apps"},
		},
		{
			name: "review 4: an unmounted dataset holds a planned name",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.datasets = append(tn.datasets, homelab.Dataset{Name: "tank/apps", Encrypted: true, Locked: true})
				return tn
			},
			wantState: map[string]homelab.ItemState{
				"dataset /mnt/tank/apps/homelab":    homelab.ItemWrong,
				"dataset /mnt/tank/apps/appconfigs": homelab.ItemWrong,
				"dataset /mnt/tank/databases":       homelab.ItemMissing,
			},
			wantMessage: map[string]string{
				"dataset /mnt/tank/apps/homelab": "dataset tank/apps exists but is not mounted, not at /mnt/tank/apps: unlock it or fix its mountpoint",
			},
		},
		{
			name: "review 7: a freenas-boot dataset never contains a root",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.datasets = append(tn.datasets, homelab.Dataset{Name: "freenas-boot/ROOT", Mountpoint: "/mnt/fb"})
				return tn
			},
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.Global.Entries[4].Value = homelab.NewSecret("/mnt/fb/databases")
			},
			wantState:   map[string]homelab.ItemState{"dataset /mnt/fb/databases": homelab.ItemWrong},
			wantMessage: map[string]string{"dataset /mnt/fb/databases": "not under any mounted dataset"},
		},
		{
			name: "review 9: networks app deploying with the networks absent",
			tn: func(*testing.T) *fakeTrueNAS {
				tn := newFreshTrueNAS()
				tn.apps = append(tn.apps, homelab.App{Name: homelab.NetworksApp, State: "DEPLOYING", Custom: true})
				return tn
			},
			wantState: map[string]homelab.ItemState{
				"networks home,proxy,isolated,databases": homelab.ItemUnknown,
				"networks_app homelab-networks":          homelab.ItemUnknown,
			},
			wantMessage: map[string]string{
				"networks home,proxy,isolated,databases": "deploying: check again",
				"networks_app homelab-networks":          "deploying; check again",
			},
		},
		{
			name: "review 10: running as another uid is wrong",
			tn:   readyTrueNAS,
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.CheckIdentity, cfg.ProcessUID, cfg.ProcessGID = true, 0, 1000
			},
			wantSteps:   []string{},
			wantState:   map[string]homelab.ItemState{"identity process": homelab.ItemWrong},
			wantMessage: map[string]string{"identity process": "the orchestrator runs as uid 0 but PUID is 1000; the app must run as PUID:PGID (the M4 image runs as uid 1000)"},
		},
		{
			name: "review 10: running as another gid differs",
			tn:   readyTrueNAS,
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.CheckIdentity, cfg.ProcessUID, cfg.ProcessGID = true, 1000, 568
			},
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantState:   map[string]homelab.ItemState{"identity process": homelab.ItemDiffers},
			wantMessage: map[string]string{"identity process": "the orchestrator runs as gid 568 but PGID is 1000"},
		},
		{
			name: "review 10: running as PUID:PGID",
			tn:   readyTrueNAS,
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.CheckIdentity, cfg.ProcessUID, cfg.ProcessGID = true, 1000, 1000
			},
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantState: map[string]homelab.ItemState{"identity process": homelab.ItemOK},
		},
		{
			name:         "review 10: the zero value skips the identity check",
			tn:           readyTrueNAS,
			cfg:          func(cfg *homelab.EnvironmentConfig) { cfg.ProcessUID, cfg.ProcessGID = 0, 0 },
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantAbsent: []string{"identity process"},
		},
		{
			name: "self app absent is advisory",
			tn: func(t *testing.T) *fakeTrueNAS {
				tn := readyTrueNAS(t)
				tn.apps = tn.apps[1:]
				return tn
			},
			wantSteps:    []string{},
			wantRunnable: true, wantReady: true,
			wantState: map[string]homelab.ItemState{"self_app truenas-orchestrator": homelab.ItemDiffers},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tn := tt.tn(t)
			cfg := testConfig(t)
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			spec, _ := homelab.NewEnvironmentSpec(cfg)
			got := check(t, spec, tn)
			if tt.wantSteps != nil {
				if diff := cmp.Diff(tt.wantSteps, stepKinds(got.Steps)); diff != "" {
					t.Errorf("steps mismatch (-want +got):\n%s", diff)
				}
			}
			if got.Runnable != tt.wantRunnable || got.Ready != tt.wantReady {
				t.Errorf("runnable %v ready %v, want %v %v; problems %q", got.Runnable, got.Ready, tt.wantRunnable, tt.wantReady, got.Problems)
			}
			if got.Runnable != (len(got.Problems) == 0) {
				t.Errorf("runnable %v with problems %q", got.Runnable, got.Problems)
			}
			items := map[string]homelab.ReadinessItem{}
			for _, it := range got.Items {
				items[strings.TrimSpace(string(it.Kind)+" "+it.Target)] = it
			}
			for label, want := range tt.wantState {
				it, ok := items[label]
				if !ok {
					t.Errorf("no item %q; items %v", label, itemsByState(got))
					continue
				}
				if it.State != want {
					t.Errorf("item %q state %s (%s), want %s", label, it.State, it.Message, want)
				}
			}
			for _, label := range tt.wantAbsent {
				if it, ok := items[label]; ok {
					t.Errorf("item %q reported: %+v", label, it)
				}
			}
			for label, want := range tt.wantMessage {
				if it := items[label]; !strings.Contains(it.Message, want) {
					t.Errorf("item %q message %q, want it to contain %q", label, it.Message, want)
				}
			}
			if m := tn.mutations(); len(m) != 0 {
				t.Errorf("checking mutated: %v", m)
			}
		})
	}
}

func TestNewEnvironmentSpecProblems(t *testing.T) {
	tests := []struct {
		name   string
		global string
		cfg    func(cfg *homelab.EnvironmentConfig)
		want   []string
	}{
		{name: "valid", global: testGlobal, want: nil},
		{
			name:   "missing keys",
			global: "PUID=1000\nPGID=\n",
			want: []string{
				"compose/.global.env does not define HOMELAB_STORAGE",
				"compose/.global.env does not define APPCONFIG_STORAGE",
				"compose/.global.env does not define DATABASE_STORAGE",
				"compose/.global.env does not define MEDIA_STORAGE",
				"compose/.global.env does not define PGID",
			},
		},
		{
			name:   "bad values",
			global: strings.Replace(strings.Replace(testGlobal, "PUID=1000", "PUID=abc", 1), "/mnt/tank/media", "media", 1),
			want: []string{
				"compose/.global.env PUID must be a non-negative integer",
				"MEDIA_STORAGE media must be an absolute path other than / without .. segments",
			},
		},
		{
			name:   "nested and duplicate roots",
			global: strings.Replace(testGlobal, "/mnt/tank/databases", "/mnt/tank/apps/homelab/db", 1),
			cfg:    func(cfg *homelab.EnvironmentConfig) { cfg.StateStorage = "/mnt/tank/media" },
			want: []string{
				"HOMELAB_STORAGE /mnt/tank/apps/homelab and DATABASE_STORAGE /mnt/tank/apps/homelab/db are nested; storage roots must not contain each other",
				"MEDIA_STORAGE and ORCH_STATE_STORAGE are the same path /mnt/tank/media; every storage root must be distinct",
			},
		},
		{
			name:   "config problems: no state storage, self app is the networks app",
			global: testGlobal,
			cfg: func(cfg *homelab.EnvironmentConfig) {
				cfg.StateStorage = ""
				cfg.SelfApp = homelab.NetworksApp
			},
			want: []string{
				"ORCH_STATE_STORAGE is not set: the orchestrator needs its state dataset path",
				"ORCH_SELF_APP is homelab-networks, the app that owns the homelab networks: the orchestrator never modifies its own app, so it could not create them",
			},
		},
		{
			name:   "review 6: a whitespace-only root is a problem",
			global: strings.Replace(testGlobal, "APPCONFIG_STORAGE=/mnt/tank/apps/appconfigs", "APPCONFIG_STORAGE=\"   \"", 1),
			cfg:    func(cfg *homelab.EnvironmentConfig) { cfg.StateStorage = "  " },
			want: []string{
				"compose/.global.env does not define APPCONFIG_STORAGE",
				"ORCH_STATE_STORAGE is not set: the orchestrator needs its state dataset path",
			},
		},
		{
			name:   "injected HOMELAB_STORAGE wins",
			global: testGlobal,
			cfg:    func(cfg *homelab.EnvironmentConfig) { cfg.HomelabStorage = "relative" },
			want:   []string{"HOMELAB_STORAGE relative must be an absolute path other than / without .. segments"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := homelab.EnvironmentConfig{SelfApp: homelab.DefaultSelfApp, StateStorage: "/mnt/tank/state", Global: parseGlobal(t, tt.global)}
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			_, got := homelab.NewEnvironmentSpec(cfg)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("problems mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestApplyBootstrapRefuses(t *testing.T) {
	t.Run("digest mismatch", func(t *testing.T) {
		tn := newFreshTrueNAS()
		spec := testSpec(t, testConfig(t))
		_, err := homelab.ApplyBootstrap(t.Context(), spec, tn, strings.Repeat("0", 64), nil)
		if !errors.Is(err, homelab.ErrConflict) {
			t.Errorf("ApplyBootstrap error = %v, want ErrConflict", err)
		}
		if m := tn.mutations(); len(m) != 0 {
			t.Errorf("mutated: %v", m)
		}
	})
	t.Run("not runnable", func(t *testing.T) {
		tn := newFreshTrueNAS()
		tn.docker.Status = "UNCONFIGURED"
		spec := testSpec(t, testConfig(t))
		r := check(t, spec, tn)
		_, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, nil)
		if !errors.Is(err, homelab.ErrValidation) || !strings.Contains(err.Error(), "docker: Docker is UNCONFIGURED") {
			t.Errorf("ApplyBootstrap error = %v, want ErrValidation naming docker", err)
		}
		if m := tn.mutations(); len(m) != 0 {
			t.Errorf("mutated: %v", m)
		}
	})
	t.Run("self app is the networks app (criterion 8)", func(t *testing.T) {
		tn := newFreshTrueNAS()
		cfg := testConfig(t)
		cfg.SelfApp = homelab.NetworksApp
		spec, problems := homelab.NewEnvironmentSpec(cfg)
		if len(problems) != 1 {
			t.Fatalf("problems = %q, want the self-app one", problems)
		}
		r := check(t, spec, tn)
		if r.Runnable {
			t.Fatalf("plan with self app %s is runnable", homelab.NetworksApp)
		}
		_, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, nil)
		if !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("ApplyBootstrap error = %v, want ErrValidation", err)
		}
		if slices.ContainsFunc(tn.mutations(), func(c string) bool { return strings.HasPrefix(c, "CreateApp") }) {
			t.Errorf("the fake received CreateApp: %v", tn.mutations())
		}
	})
}

func TestApplyBootstrapStopsAtFirstError(t *testing.T) {
	tn := newFreshTrueNAS()
	spec := testSpec(t, testConfig(t))
	r := check(t, spec, tn)
	tn.errs["CreateDataset"] = errors.New("truenas: pool.dataset.create: EINVAL: Path /mnt/tank/apps already exists")
	var lines []string
	res, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, func(l string) { lines = append(lines, l) })
	if err == nil || !strings.Contains(err.Error(), "bootstrap: dataset tank/apps: truenas: pool.dataset.create") {
		t.Fatalf("ApplyBootstrap error = %v, want one naming dataset tank/apps", err)
	}
	if res.Created != 2 || len(res.Steps) != 2 {
		t.Errorf("result %s, want the group and user done", res.Summary())
	}
	if diff := cmp.Diff([]string{"CreateGroup homelab", "CreateUser homelab", "CreateDataset tank/apps"}, tn.mutations()); diff != "" {
		t.Errorf("mutations mismatch (-want +got):\n%s", diff)
	}
	if last := lines[len(lines)-1]; last != "stopped: 2 created, 0 changed, 0 unchanged" {
		t.Errorf("last line %q", last)
	}
}

func TestApplyBootstrapRechecksAtPointOfWrite(t *testing.T) {
	tn := newFreshTrueNAS()
	spec := testSpec(t, testConfig(t))
	r := check(t, spec, tn)
	// The group appears between the run's own observation and its write.
	groupsReads := 0
	tn.before = func(method string) {
		if method != "Groups" {
			return
		}
		groupsReads++
		if groupsReads == 2 {
			tn.mu.Lock()
			tn.groups = append(tn.groups, homelab.Group{ID: 7, GID: 1000, Name: "family"})
			tn.mu.Unlock()
		}
	}
	res, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, nil)
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if res.Steps[0].Outcome != homelab.OutcomeBootstrapUnchanged || res.Unchanged != 1 || res.Created != 9 {
		t.Errorf("result %s, first outcome %s, want the group unchanged", res.Summary(), res.Steps[0].Outcome)
	}
	if slices.Contains(tn.mutations(), "CreateGroup homelab") {
		t.Errorf("CreateGroup ran although the gid appeared: %v", tn.mutations())
	}
}

func TestBootstrapPermissionsChangeDetail(t *testing.T) {
	tn := readyTrueNAS(t)
	tn.stats["/mnt/tank/databases"] = homelab.PathStat{Exists: true, Type: homelab.PathDirectory, UID: 0, GID: 0, Mode: 0o755, ExtendedACL: true}
	spec := testSpec(t, testConfig(t))
	r := check(t, spec, tn)
	want := []homelab.BootstrapStep{{
		Kind: homelab.StepPermissionsSet, Target: "/mnt/tank/databases", Action: homelab.BootstrapChange,
		Detail: map[string]string{
			"from_owner": "0:0", "from_mode": "0755", "from_acl": "true", "to_owner": "1000:1000", "to_mode": "0750", "stripacl": "true",
		},
	}}
	if diff := cmp.Diff(want, r.Steps); diff != "" {
		t.Fatalf("steps mismatch (-want +got):\n%s", diff)
	}
	var lines []string
	res, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, func(l string) { lines = append(lines, l) })
	if err != nil || res.Changed != 1 {
		t.Fatalf("ApplyBootstrap = %s, %v, want 1 changed", res.Summary(), err)
	}
	if lines[0] != "change permissions /mnt/tank/databases 0:0 0755 -> 1000:1000 0750" {
		t.Errorf("line %q", lines[0])
	}
}

func TestBootstrapDigest(t *testing.T) {
	spec := testSpec(t, testConfig(t))
	a := check(t, spec, newFreshTrueNAS())
	b := check(t, spec, newFreshTrueNAS())
	if a.Digest != b.Digest {
		t.Errorf("the same state planned twice has digests %s and %s", a.Digest, b.Digest)
	}
	tn := newFreshTrueNAS()
	tn.groups = append(tn.groups, homelab.Group{ID: 9, GID: 1000, Name: "homelab"})
	if c := check(t, spec, tn); c.Digest == a.Digest {
		t.Errorf("a different plan has the same digest %s", c.Digest)
	}
}

func TestDeployKeyItem(t *testing.T) {
	tn := readyTrueNAS(t)
	spec := testSpec(t, testConfig(t))
	obs, err := homelab.ObserveEnvironment(t.Context(), spec, tn)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		observed  homelab.ItemState
		wantState homelab.ItemState
		wantReady bool
	}{
		{homelab.ItemOK, homelab.ItemOK, true},
		{homelab.ItemMissing, homelab.ItemMissing, false},
		{homelab.ItemUnknown, homelab.ItemUnknown, false},
	} {
		obs.DeployKey = tt.observed
		r := homelab.CheckEnvironment(spec, obs)
		last := r.Items[len(r.Items)-1]
		if last.Kind != homelab.ItemDeployKey || last.State != tt.wantState || last.Target != "/mnt/tank/state/ssh/id_ed25519" || r.Ready != tt.wantReady {
			t.Errorf("observed %s: item %+v ready %v, want %s ready %v", tt.observed, last, r.Ready, tt.wantState, tt.wantReady)
		}
		if len(r.Steps) != 0 || !r.Runnable {
			t.Errorf("observed %s: steps %v runnable %v; the deploy key must neither plan nor block", tt.observed, stepKinds(r.Steps), r.Runnable)
		}
	}
}

func TestApplyBootstrapDatasetMountpointAtPointOfWrite(t *testing.T) {
	for _, tt := range []struct {
		name       string
		mountpoint string
		wantErr    error
	}{
		{name: "appeared at the planned mountpoint: unchanged", mountpoint: "/mnt/tank/apps"},
		{name: "appeared unmounted: conflict", mountpoint: "", wantErr: homelab.ErrConflict},
		{name: "appeared mounted elsewhere: conflict", mountpoint: "/mnt/elsewhere", wantErr: homelab.ErrConflict},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tn := newFreshTrueNAS()
			spec := testSpec(t, testConfig(t))
			r := check(t, spec, tn)
			// Datasets is read once by the run's observation, then by each dataset step.
			reads := 0
			tn.before = func(method string) {
				if method != "Datasets" {
					return
				}
				reads++
				if reads == 2 {
					tn.mu.Lock()
					tn.datasets = append(tn.datasets, homelab.Dataset{Name: "tank/apps", Mountpoint: tt.mountpoint})
					tn.mu.Unlock()
				}
			}
			res, err := homelab.ApplyBootstrap(t.Context(), spec, tn, r.Digest, nil)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || !strings.Contains(err.Error(), "dataset tank/apps appeared since planning but is not mounted at /mnt/tank/apps") {
					t.Errorf("ApplyBootstrap error = %v, want %v naming the mountpoint", err, tt.wantErr)
				}
				if slices.Contains(tn.mutations(), "CreateDataset tank/apps") {
					t.Errorf("created tank/apps anyway")
				}
				return
			}
			if err != nil || res.Steps[2].Outcome != homelab.OutcomeBootstrapUnchanged {
				t.Errorf("ApplyBootstrap = %s, %v; step 3 %s, want tank/apps unchanged", res.Summary(), err, res.Steps[2].Outcome)
			}
		})
	}
}
