package truenas

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func fixturesFS() fs.FS { return os.DirFS("../../fixtures/truenas") }

func mapScenario(name, body string) fstest.MapFS {
	return fstest.MapFS{"scenarios/" + name + ".json": &fstest.MapFile{Data: []byte(body)}}
}

func loadFake(t *testing.T, name string, opts FakeOptions) *Fake {
	t.Helper()
	f, err := LoadScenario(fixturesFS(), name, opts)
	if err != nil {
		t.Fatalf("LoadScenario(%s) error = %v", name, err)
	}
	return f
}

var shippedScenarios = []string{
	"fresh", "ready", "partial", "unmanaged-networks", "mixed-networks", "docker-unconfigured",
	"name-collision", "version-out-of-range", "auth-failure", "dataset-create-einval",
}

func TestShippedScenariosLoad(t *testing.T) {
	entries, err := fs.ReadDir(fixturesFS(), "scenarios")
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		onDisk = append(onDisk, strings.TrimSuffix(e.Name(), ".json"))
	}
	if diff := cmp.Diff(len(shippedScenarios), len(onDisk)); diff != "" {
		t.Errorf("scenario count mismatch: on disk %v", onDisk)
	}
	for _, name := range shippedScenarios {
		t.Run(name, func(t *testing.T) {
			f := loadFake(t, name, FakeOptions{})
			ctx := t.Context()
			if _, err := f.SystemVersion(ctx); err != nil {
				t.Errorf("SystemVersion() error = %v", err)
			}
			ds, err := f.Datasets(ctx)
			if err != nil || len(ds) == 0 || ds[0].Name != "tank" || ds[0].Mountpoint != "/mnt/tank" {
				t.Errorf("Datasets() = %+v, %v, want the pool root tank at /mnt/tank first", ds, err)
			}
			if st, err := f.Stat(ctx, "/mnt/tank"); err != nil || !st.Exists {
				t.Errorf("Stat(/mnt/tank) = %+v, %v, want it to exist", st, err)
			}
		})
	}
}

func TestLoadScenarioErrors(t *testing.T) {
	tests := []struct {
		name   string
		fsys   fs.FS
		sc     string
		wantIs error
	}{
		{"unknown scenario", fixturesFS(), "nope", homelab.ErrNotFound},
		{"path traversal", fixturesFS(), "../wire/25.10/core.ping", homelab.ErrValidation},
		{"empty name", fixturesFS(), "", homelab.ErrValidation},
		{"not json", mapScenario("x", "{"), "x", homelab.ErrValidation},
		{"unknown field", mapScenario("x", `{"version":"TrueNAS-25.10.1","resutls":{}}`), "x", homelab.ErrValidation},
		{"no version", mapScenario("x", `{"auth":"ok"}`), "x", homelab.ErrValidation},
		{"bad auth", mapScenario("x", `{"version":"TrueNAS-25.10.1","auth":"maybe"}`), "x", homelab.ErrValidation},
		{"bad seed shape", mapScenario("x", `{"version":"TrueNAS-25.10.1","results":{"user.query":{"id":1}}}`), "x", homelab.ErrValidation},
		{"unknown errors key", mapScenario("x", `{"version":"TrueNAS-25.10.1","errors":{"pool.dataset.creat":{"code":-32602}}}`), "x", homelab.ErrValidation},
		{"trailing data", mapScenario("x", `{"version":"TrueNAS-25.10.1"} {"version":"x"}`), "x", homelab.ErrValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadScenario(tt.fsys, tt.sc, FakeOptions{})
			if !errors.Is(err, tt.wantIs) {
				t.Errorf("LoadScenario() error = %v, want %v", err, tt.wantIs)
			}
		})
	}
}

func TestFakeDialerGates(t *testing.T) {
	expired := mapScenario("expired", `{"version":"TrueNAS-25.10.1","auth":"expired"}`)
	gateErr := func(method string, code, errno int, errname string) fstest.MapFS {
		return mapScenario("gate", fmt.Sprintf(`{"version":"TrueNAS-25.10.1","errors":{%q:{"code":%d,"message":"m",`+
			`"data":{"error":%d,"errname":%q,"reason":"from scenario","trace":null,"extra":null}}}}`, method, code, errno, errname))
	}
	tests := []struct {
		name        string
		fsys        fs.FS
		sc          string
		creds       *homelab.TrueNASCredentials
		wantIs      error
		wantMsg     string
		wantVersion *homelab.UnsupportedVersionError
	}{
		{
			name: "empty username", fsys: fixturesFS(), sc: "ready",
			creds:  &homelab.TrueNASCredentials{APIKey: homelab.NewSecret(testKey)},
			wantIs: homelab.ErrUnauthenticated, wantMsg: "TrueNAS rejected the API key for user : unauthenticated",
		},
		{
			name: "empty key", fsys: fixturesFS(), sc: "ready",
			creds:  &homelab.TrueNASCredentials{Username: "truenas_admin"},
			wantIs: homelab.ErrUnauthenticated, wantMsg: "TrueNAS rejected the API key for user truenas_admin: unauthenticated",
		},
		{
			name: "login error", fsys: gateErr("auth.login_ex", -32001, 207, "ENOTAUTHENTICATED"), sc: "gate",
			wantIs: homelab.ErrUnauthenticated, wantMsg: "truenas: auth.login_ex: ENOTAUTHENTICATED: from scenario",
		},
		{
			name: "version call error", fsys: gateErr("system.version", -32601, 0, ""), sc: "gate",
			wantIs: homelab.ErrUnavailable, wantMsg: "truenas: system.version: METHOD_NOT_FOUND: from scenario: middleware API drift; run just smoke",
		},
		{
			name: "set_options error", fsys: gateErr("core.set_options", -32601, 0, ""), sc: "gate",
			wantIs: homelab.ErrUnavailable, wantMsg: "truenas: core.set_options: METHOD_NOT_FOUND: from scenario: middleware API drift; run just smoke",
		},
		{name: "ready", fsys: fixturesFS(), sc: "ready"},
		{
			name: "auth failure", fsys: fixturesFS(), sc: "auth-failure", wantIs: homelab.ErrUnauthenticated,
			wantMsg: "TrueNAS rejected the API key for user truenas_admin: unauthenticated",
		},
		{
			name: "expired", fsys: expired, sc: "expired", wantIs: homelab.ErrUnauthenticated,
			wantMsg: "the API key for user truenas_admin is expired or revoked; TrueNAS revokes a key used over plain HTTP: unauthenticated",
		},
		{
			name: "version out of range", fsys: fixturesFS(), sc: "version-out-of-range", wantIs: homelab.ErrValidation,
			wantMsg:     "TrueNAS 25.04.2.6 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x",
			wantVersion: &homelab.UnsupportedVersionError{Detected: "25.04.2.6", Supported: "TrueNAS 25.10 through 27.x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds := testCreds()
			if tt.creds != nil {
				creds = *tt.creds
			}
			f, err := FakeDialer(tt.fsys, tt.sc, FakeOptions{})(t.Context(), creds)
			if tt.wantIs == nil {
				if err != nil || f == nil {
					t.Fatalf("dial = %v, %v, want a session", f, err)
				}
				return
			}
			if !errors.Is(err, tt.wantIs) || err.Error() != tt.wantMsg {
				t.Errorf("dial error = %v, want %q wrapping %v", err, tt.wantMsg, tt.wantIs)
			}
			if tt.wantVersion != nil {
				var ve *homelab.UnsupportedVersionError
				if !errors.As(err, &ve) {
					t.Fatalf("dial error = %T, want *homelab.UnsupportedVersionError", err)
				}
				if diff := cmp.Diff(tt.wantVersion, ve); diff != "" {
					t.Errorf("UnsupportedVersionError mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestFakeReadyState(t *testing.T) {
	f := loadFake(t, "ready", FakeOptions{Mnt: "/srv/dev/mnt/"})
	ctx := t.Context()
	root := homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	ds, err := f.Datasets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]homelab.Dataset{}
	for _, d := range ds {
		byName[d.Name] = d
	}
	for name, mount := range map[string]string{
		"tank/apps/homelab": "/srv/dev/mnt/tank/apps/homelab", "tank/apps/appconfigs": "/srv/dev/mnt/tank/apps/appconfigs",
		"tank/orchestrator": "/srv/dev/mnt/tank/orchestrator", "tank/apps": "/srv/dev/mnt/tank/apps",
		"tank/databases": "/srv/dev/mnt/tank/databases", "tank/media": "/srv/dev/mnt/tank/media",
	} {
		if byName[name].Mountpoint != mount {
			t.Errorf("dataset %s mountpoint = %q, want %q", name, byName[name].Mountpoint, mount)
		}
	}
	if diff := cmp.Diff(root, byName["tank/apps/homelab"].Props); diff != "" {
		t.Errorf("homelab root props mismatch (-want +got):\n%s", diff)
	}
	if byName["tank/databases"].Props.RecordSize != "16K" {
		t.Errorf("databases recordsize = %q, want 16K", byName["tank/databases"].Props.RecordSize)
	}

	for p, want := range map[string]homelab.PathStat{
		"/srv/dev/mnt/tank/apps/appconfigs": {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o750, IsMountpoint: true},
		"/srv/dev/mnt/tank/orchestrator/":   {Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o700, IsMountpoint: true},
		"/srv/dev/mnt/tank/media":           {Exists: true, Type: homelab.PathDirectory, Mode: 0o770, ExtendedACL: true, IsMountpoint: true},
		"/srv/dev/mnt/tank/nothing":         {},
	} {
		got, err := f.Stat(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Stat(%s) mismatch (-want +got):\n%s", p, diff)
		}
	}

	users, err := f.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsUser(users, homelab.User{ID: 72, UID: 1000, GID: 1000, Name: "homelab"}) {
		t.Errorf("Users() = %+v, want homelab 1000:1000", users)
	}
	nets, err := f.Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range nets {
		names = append(names, n.Name+"/"+n.ComposeProject)
	}
	want := []string{"home/ix-homelab-networks", "proxy/ix-homelab-networks", "isolated/ix-homelab-networks", "databases/ix-homelab-networks"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("Networks() mismatch, other networks must be filtered out (-want +got):\n%s", diff)
	}
}

func containsUser(us []homelab.User, want homelab.User) bool {
	for _, u := range us {
		if u == want {
			return true
		}
	}
	return false
}

// TestFakeMutationsOnFresh walks the bootstrap steps against fresh and checks each changes the
// state the way the readiness check will read it.
func TestFakeMutationsOnFresh(t *testing.T) {
	f := loadFake(t, "fresh", FakeOptions{SelfApp: "truenas-orchestrator"})
	ctx := t.Context()
	props := &homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	steps := []func() error{
		func() error { return f.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000}) },
		func() error { return f.CreateUser(ctx, homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000}) },
		func() error { return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps"}) },
		func() error {
			return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps/homelab", Props: props})
		},
		func() error {
			return f.SetPermissions(ctx, homelab.PermissionSpec{Path: "/mnt/tank/apps/homelab", UID: 1000, GID: 1000, Mode: 0o750})
		},
		func() error {
			return f.CreateApp(ctx, homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose})
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d error = %v", i, err)
		}
	}

	wantCalls := []string{
		"group.create homelab", "user.create homelab", "pool.dataset.create tank/apps",
		"pool.dataset.create tank/apps/homelab", "filesystem.setperm /mnt/tank/apps/homelab",
		"app.create homelab-networks",
	}
	if diff := cmp.Diff(wantCalls, f.Calls()); diff != "" {
		t.Errorf("Calls() mismatch (-want +got):\n%s", diff)
	}

	groups, _ := f.Groups(ctx)
	users, _ := f.Users(ctx)
	g := groups[len(groups)-1]
	if g != (homelab.Group{ID: 72, GID: 1000, Name: "homelab"}) {
		t.Errorf("new group = %+v, want homelab 1000 with the next row id (72)", g)
	}
	if u := users[len(users)-1]; u != (homelab.User{ID: 73, UID: 1000, GID: 1000, Name: "homelab"}) {
		t.Errorf("new user = %+v, want homelab 1000:1000", u)
	}

	ds, _ := f.Datasets(ctx)
	inherit := ds[0].Props // tank
	wantNew := []homelab.Dataset{
		{Name: "tank/apps", Mountpoint: "/mnt/tank/apps", Props: inherit},
		{Name: "tank/apps/homelab", Mountpoint: "/mnt/tank/apps/homelab", Props: *props},
	}
	if diff := cmp.Diff(wantNew, ds[len(ds)-2:]); diff != "" {
		t.Errorf("created datasets mismatch (-want +got):\n%s", diff)
	}
	st, _ := f.Stat(ctx, "/mnt/tank/apps")
	if diff := cmp.Diff(homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755, IsMountpoint: true}, st); diff != "" {
		t.Errorf("new dataset stat mismatch (-want +got):\n%s", diff)
	}
	st, _ = f.Stat(ctx, "/mnt/tank/apps/homelab")
	if diff := cmp.Diff(homelab.PathStat{Exists: true, Type: homelab.PathDirectory, UID: 1000, GID: 1000, Mode: 0o750, IsMountpoint: true}, st); diff != "" {
		t.Errorf("stat after SetPermissions mismatch (-want +got):\n%s", diff)
	}

	nets, _ := f.Networks(ctx)
	if len(nets) != 4 {
		t.Fatalf("Networks() after CreateApp = %+v, want the four homelab networks", nets)
	}
	for _, n := range nets {
		if n.ComposeProject != "ix-homelab-networks" || n.Driver != "bridge" || len(n.ID) != 64 {
			t.Errorf("network %+v, want bridge, project ix-homelab-networks and a 64-hex id", n)
		}
	}
	apps, _ := f.Apps(ctx)
	if got := apps[len(apps)-1]; got != (homelab.App{Name: homelab.NetworksApp, State: "RUNNING", Custom: true}) {
		t.Errorf("new app = %+v", got)
	}
}

func TestFakeCreateAppKeepsExistingNetworks(t *testing.T) {
	f := loadFake(t, "partial", FakeOptions{})
	ctx := t.Context()
	before, _ := f.Networks(ctx)
	if len(before) != 3 {
		t.Fatalf("partial has %d homelab networks, want 3", len(before))
	}
	if err := f.CreateApp(ctx, homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.Networks(ctx)
	if diff := cmp.Diff(before, after[:3]); diff != "" || len(after) != 4 || after[3].Name != "databases" {
		t.Errorf("Networks() after CreateApp = %+v, want the three kept and databases added (%s)", after, diff)
	}
}

func TestFakeMutationErrors(t *testing.T) {
	pathTaken := mapScenario("taken", `{"version":"TrueNAS-25.10.1","results":{
		"pool.dataset.query":[{"name":"tank","mountpoint":"/mnt/tank"},{"name":"tank/locked","mountpoint":null,"encrypted":true,"locked":true}],
		"filesystem.stat":{"/mnt/tank/apps":{"type":"DIRECTORY","mode":16877}}}}`)
	tests := []struct {
		name       string
		fsys       fs.FS
		sc         string
		call       func(context.Context, *Fake) error
		wantIs     error
		wantField  string
		wantRecord bool
	}{
		{
			name: "group name taken", sc: "ready",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 2000})
			},
			wantIs: homelab.ErrConflict, wantField: "group_create.name", wantRecord: true,
		},
		{
			name: "gid taken", sc: "ready",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateGroup(ctx, homelab.GroupSpec{Name: "other", GID: 1000})
			},
			wantIs: homelab.ErrConflict, wantField: "group_create.gid", wantRecord: true,
		},
		{
			name: "user without group never sent", sc: "fresh",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateUser(ctx, homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000})
			},
			wantIs: homelab.ErrNotFound,
		},
		{
			name: "username taken", sc: "name-collision",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateUser(ctx, homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000})
			},
			wantIs: homelab.ErrConflict, wantField: "user_create.username", wantRecord: true,
		},
		{
			name: "uid taken", sc: "name-collision",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateUser(ctx, homelab.UserSpec{Name: "other", UID: 1001, GID: 1000})
			},
			wantIs: homelab.ErrConflict, wantField: "user_create.uid", wantRecord: true,
		},
		{
			name: "dataset exists", sc: "ready",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps"})
			},
			wantIs: homelab.ErrConflict, wantField: "pool_dataset_create.name", wantRecord: true,
		},
		{
			name: "parent missing", sc: "fresh",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps/homelab"})
			},
			wantIs: homelab.ErrValidation, wantField: "pool_dataset_create.name", wantRecord: true,
		},
		{
			name: "mountpoint path exists", fsys: pathTaken, sc: "taken",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps"})
			},
			wantIs: homelab.ErrValidation, wantField: "pool_dataset_create.name", wantRecord: true,
		},
		{
			name: "parent locked", fsys: pathTaken, sc: "taken",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/locked/x"})
			},
			wantIs: homelab.ErrValidation, wantField: "pool_dataset_create.name", wantRecord: true,
		},
		{
			name: "setperm on absent path", sc: "fresh",
			call: func(ctx context.Context, f *Fake) error {
				return f.SetPermissions(ctx, homelab.PermissionSpec{Path: "/mnt/tank/nope", Mode: 0o750})
			},
			wantIs: homelab.ErrNotFound, wantRecord: true,
		},
		{
			name: "setperm with setuid never sent", sc: "ready",
			call: func(ctx context.Context, f *Fake) error {
				return f.SetPermissions(ctx, homelab.PermissionSpec{Path: "/mnt/tank/apps", Mode: 0o4755})
			},
			wantIs: homelab.ErrValidation,
		},
		{
			name: "app exists", sc: "ready",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateApp(ctx, homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose})
			},
			wantIs: homelab.ErrConflict, wantField: "app_create.app_name", wantRecord: true,
		},
		{
			name: "self app never sent", sc: "fresh",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateApp(ctx, homelab.AppSpec{Name: "truenas-orchestrator", Compose: "services: {}"})
			},
			wantIs: homelab.ErrForbidden,
		},
		{
			name: "invalid compose", sc: "fresh",
			call: func(ctx context.Context, f *Fake) error {
				return f.CreateApp(ctx, homelab.AppSpec{Name: "x", Compose: "networks: [unclosed"})
			},
			wantIs: homelab.ErrValidation, wantField: "app_create.custom_compose_config_string", wantRecord: true,
		},
		{
			name: "relative stat", sc: "fresh",
			call:   func(ctx context.Context, f *Fake) error { _, err := f.Stat(ctx, "tank"); return err },
			wantIs: homelab.ErrValidation, wantField: "filesystem.stat.path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := tt.fsys
			if fsys == nil {
				fsys = fixturesFS()
			}
			f, err := LoadScenario(fsys, tt.sc, FakeOptions{SelfApp: "truenas-orchestrator"})
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(t.Context(), f)
			if !errors.Is(err, tt.wantIs) {
				t.Fatalf("call error = %v, want %v", err, tt.wantIs)
			}
			if tt.wantField != "" {
				var e *Error
				if !errors.As(err, &e) || len(e.Fields) != 1 || e.Fields[0].Field != tt.wantField {
					t.Errorf("call error = %#v, want one field error on %s", err, tt.wantField)
				}
			}
			if got := len(f.Calls()) > 0; got != tt.wantRecord {
				t.Errorf("Calls() = %v, want recorded %v", f.Calls(), tt.wantRecord)
			}
		})
	}
}

func TestFakeScenarioErrors(t *testing.T) {
	f := loadFake(t, "dataset-create-einval", FakeOptions{Mnt: "/srv/m"})
	ctx := t.Context()
	for i := range 2 {
		err := f.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps"})
		var e *Error
		if !errors.As(err, &e) || !errors.Is(err, homelab.ErrValidation) {
			t.Fatalf("CreateDataset() #%d error = %v, want the scenario's EINVAL", i, err)
		}
		want := []FieldError{{Field: "pool_dataset_create.name", Message: "Path /srv/m/tank/apps already exists", Errno: 22}}
		if diff := cmp.Diff(want, e.Fields); diff != "" {
			t.Errorf("fields mismatch (-want +got):\n%s", diff)
		}
	}
	if diff := cmp.Diff([]string{"pool.dataset.create tank/apps", "pool.dataset.create tank/apps"}, f.Calls()); diff != "" {
		t.Errorf("Calls() mismatch (-want +got):\n%s", diff)
	}

	once := mapScenario("once", `{"version":"TrueNAS-25.10.1","results":{"group.query":[]},
		"errors":{"group.create":{"code":-32001,"message":"Method call error","once":true,
		"data":{"error":17,"errname":"EEXIST","reason":"busy","trace":null,"extra":null}}}}`)
	g, err := LoadScenario(once, "once", FakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000}); !errors.Is(err, homelab.ErrConflict) {
		t.Errorf("first CreateGroup() error = %v, want the once error (ErrConflict)", err)
	}
	if err := g.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000}); err != nil {
		t.Errorf("second CreateGroup() error = %v, want nil", err)
	}
}

func TestFakeDialerSharesState(t *testing.T) {
	dial := FakeDialer(fixturesFS(), "fresh", FakeOptions{})
	ctx := t.Context()
	f1, err := dial(ctx, testCreds())
	if err != nil {
		t.Fatal(err)
	}
	if err := f1.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := f1.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f1.Groups(ctx); !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("Groups() on a closed session error = %v, want ErrUnavailable", err)
	}
	f2, err := dial(ctx, testCreds())
	if err != nil {
		t.Fatal(err)
	}
	groups, err := f2.Groups(ctx)
	if err != nil || groups[len(groups)-1].Name != "homelab" {
		t.Errorf("second session Groups() = %+v, %v, want the group the first session created", groups, err)
	}
	if diff := cmp.Diff([]string{"group.create homelab"}, f2.Calls()); diff != "" {
		t.Errorf("Calls() mismatch (-want +got):\n%s", diff)
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	f := loadFake(t, "fresh", FakeOptions{})
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if err := f.CreateGroup(ctx, homelab.GroupSpec{Name: fmt.Sprintf("g%d", i), GID: 2000 + i}); err != nil {
				t.Errorf("CreateGroup(%d) error = %v", i, err)
			}
			_, _ = f.Datasets(ctx)
			_, _ = f.Stat(ctx, "/mnt/tank")
			_ = f.Calls()
		})
	}
	wg.Wait()
	groups, _ := f.Groups(ctx)
	if len(groups) != 6+16 {
		t.Errorf("Groups() = %d groups, want %d", len(groups), 6+16)
	}
}

func TestFakeCancelledContext(t *testing.T) {
	f := loadFake(t, "ready", FakeOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.SystemVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("SystemVersion(cancelled) error = %v, want context.Canceled", err)
	}
	if err := f.CreateGroup(ctx, homelab.GroupSpec{Name: "x", GID: 2000}); !errors.Is(err, context.Canceled) {
		t.Errorf("CreateGroup(cancelled) error = %v, want context.Canceled", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("a cancelled call was recorded: %v", f.Calls())
	}
	if _, err := FakeDialer(fixturesFS(), "ready", FakeOptions{})(ctx, testCreds()); !errors.Is(err, context.Canceled) {
		t.Errorf("dial(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestFakeDialerOnceGateError(t *testing.T) {
	fsys := mapScenario("once", `{"version":"TrueNAS-25.10.1","errors":{"core.set_options":{"code":-32001,"message":"m","once":true,
		"data":{"error":22,"errname":"EINVAL","reason":"first dial only","trace":null,"extra":null}}}}`)
	dial := FakeDialer(fsys, "once", FakeOptions{})
	if _, err := dial(t.Context(), testCreds()); !errors.Is(err, homelab.ErrValidation) {
		t.Fatalf("first dial error = %v, want the once error", err)
	}
	if _, err := dial(t.Context(), testCreds()); err != nil {
		t.Errorf("second dial error = %v, want nil", err)
	}
}
