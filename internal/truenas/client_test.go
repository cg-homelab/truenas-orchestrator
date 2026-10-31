package truenas

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// testKey is distinctive so a leak is easy to grep for.
const testKey = "1-LEAKCANARYzYx9WvU8tS7rQ6pO5nM4lK3jI2hG1fE0dC"

func testCreds() homelab.TrueNASCredentials {
	return homelab.TrueNASCredentials{Username: "truenas_admin", APIKey: homelab.NewSecret(testKey)}
}

// logBuffer is a concurrency-safe sink for a debug-level slog handler.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestLogger() (*slog.Logger, *logBuffer) {
	b := &logBuffer{}
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})), b
}

func jsonEqual(t *testing.T, label, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: want is not JSON: %v", label, err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("%s: got is not JSON: %v (%s)", label, err, got)
	}
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", label, diff)
	}
}

func dialFake(t *testing.T, s *fakeServer, logger *slog.Logger) *Client {
	t.Helper()
	cl, err := Dial(t.Context(), Config{URL: s.url, SelfApp: "truenas-orchestrator", Logger: logger}, testCreds())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	cl.jobPoll = time.Millisecond
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return cl
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		raw     string
		want    endpoint
		wantErr bool
	}{
		{"wss://truenas.lan", endpoint{ws: "wss://truenas.lan/api/v25.10.0", http: "https://truenas.lan", display: "wss://truenas.lan"}, false},
		{"wss://host.docker.internal:8443/", endpoint{ws: "wss://host.docker.internal:8443/api/v25.10.0", http: "https://host.docker.internal:8443", display: "wss://host.docker.internal:8443"}, false},
		{"ws://127.0.0.1:6000", endpoint{ws: "ws://127.0.0.1:6000/api/v25.10.0", http: "http://127.0.0.1:6000", display: "ws://127.0.0.1:6000"}, false},
		{"ws://localhost", endpoint{ws: "ws://localhost/api/v25.10.0", http: "http://localhost", display: "ws://localhost"}, false},
		{"ws://[::1]:80", endpoint{ws: "ws://[::1]:80/api/v25.10.0", http: "http://[::1]:80", display: "ws://[::1]:80"}, false},
		{"ws://truenas.lan", endpoint{}, true},
		{"ws://192.168.1.10", endpoint{}, true},
		{"https://truenas.lan", endpoint{}, true},
		{"wss://truenas.lan/api/current", endpoint{}, true},
		{"wss://user:SECRETPW@truenas.lan", endpoint{}, true},
		{"wss://truenas.lan/?token=SECRETPW", endpoint{}, true},
		{"wss://truenas.lan/#SECRETPW", endpoint{}, true},
		{"wss://tru enas.lan/SECRETPW%zz", endpoint{}, true},
		{"truenas.lan", endpoint{}, true},
		{"", endpoint{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseEndpoint(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, homelab.ErrValidation) {
					t.Errorf("parseEndpoint(%q) error = %v, want ErrValidation", tt.raw, err)
				}
				if strings.Contains(err.Error(), "SECRETPW") {
					t.Errorf("parseEndpoint(%q) error = %q echoes the raw URL", tt.raw, err)
				}
				if strings.Contains(tt.raw, "@") && !strings.Contains(err.Error(), "credentials in the URL are not allowed") {
					t.Errorf("parseEndpoint(%q) error = %q, want the credentials message", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEndpoint(%q) error = %v", tt.raw, err)
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(endpoint{})); diff != "" {
				t.Errorf("parseEndpoint(%q) mismatch (-want +got):\n%s", tt.raw, diff)
			}
		})
	}
}

func TestDialHandshake(t *testing.T) {
	s := newFakeServer(t)
	dialFake(t, s, nil)
	if diff := cmp.Diff([]string{"auth.login_ex", "system.version", "core.set_options"}, s.methods()); diff != "" {
		t.Errorf("handshake methods mismatch (-want +got):\n%s", diff)
	}
	jsonEqual(t, "auth.login_ex params",
		`[{"mechanism":"API_KEY_PLAIN","username":"truenas_admin","api_key":"`+testKey+`"}]`,
		s.lastParams(t, "auth.login_ex"))
	jsonEqual(t, "system.version params", `[]`, s.lastParams(t, "system.version"))
	jsonEqual(t, "core.set_options params", `[{"legacy_jobs":false}]`, s.lastParams(t, "core.set_options"))
}

func TestDialErrors(t *testing.T) {
	tests := []struct {
		name        string
		opts        []fakeServerOption
		wantIs      error
		wantMsg     string
		wantVersion *homelab.UnsupportedVersionError
		wantMethods []string
	}{
		{
			name:        "rejected key",
			opts:        []fakeServerOption{withRoute("auth.login_ex", "auth.login_ex.auth_err")},
			wantIs:      homelab.ErrUnauthenticated,
			wantMsg:     "TrueNAS rejected the API key for user truenas_admin",
			wantMethods: []string{"auth.login_ex"},
		},
		{
			name:        "revoked key",
			opts:        []fakeServerOption{withRoute("auth.login_ex", "auth.login_ex.expired")},
			wantIs:      homelab.ErrUnauthenticated,
			wantMsg:     "the API key for user truenas_admin is expired or revoked; TrueNAS revokes a key used over plain HTTP",
			wantMethods: []string{"auth.login_ex"},
		},
		{
			name:        "two-factor",
			opts:        []fakeServerOption{withRoute("auth.login_ex", "auth.login_ex.otp_required")},
			wantIs:      homelab.ErrValidation,
			wantMsg:     "two-factor login is not supported for API keys",
			wantMethods: []string{"auth.login_ex"},
		},
		{
			name:    "version out of range",
			opts:    []fakeServerOption{withRoute("system.version", "system.version.25.04")},
			wantIs:  homelab.ErrValidation,
			wantMsg: "TrueNAS 25.04.2.6 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x",
			wantVersion: &homelab.UnsupportedVersionError{
				Detected: "25.04.2.6", Supported: "TrueNAS 25.10 through 27.x",
			},
			wantMethods: []string{"auth.login_ex", "system.version"},
		},
		{
			name:    "pinned API not served",
			opts:    []fakeServerOption{withUpgradeStatus(http.StatusNotFound, "api.versions.25.04")},
			wantIs:  homelab.ErrValidation,
			wantMsg: "TrueNAS serves API versions v25.04.0, v25.04.1, v25.04.2, none of which is v25.10.0",
			wantVersion: &homelab.UnsupportedVersionError{
				Supported: "TrueNAS 25.10 through 27.x", ServedAPIs: []string{"v25.04.0", "v25.04.1", "v25.04.2"},
			},
		},
		{
			name:        "login before auth is a 207",
			opts:        []fakeServerOption{withRoute("auth.login_ex", "error.not_authenticated")},
			wantIs:      homelab.ErrUnauthenticated,
			wantMsg:     "truenas: auth.login_ex: ENOTAUTHENTICATED: Not authenticated",
			wantMethods: []string{"auth.login_ex"},
		},
		{
			name:        "set_options missing",
			opts:        []fakeServerOption{withRoute("core.set_options", "error.method_not_found")},
			wantIs:      homelab.ErrUnavailable,
			wantMsg:     "middleware API drift; run just smoke",
			wantMethods: []string{"auth.login_ex", "system.version", "core.set_options"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeServer(t, tt.opts...)
			logger, logs := newTestLogger()
			cl, err := Dial(t.Context(), Config{URL: s.url, Logger: logger}, testCreds())
			if err == nil {
				_ = cl.Close()
				t.Fatal("Dial() error = nil, want an error")
			}
			if !errors.Is(err, tt.wantIs) {
				t.Errorf("Dial() error = %v, want errors.Is %v", err, tt.wantIs)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Dial() error = %q, want it to contain %q", err, tt.wantMsg)
			}
			if tt.wantVersion != nil {
				var ve *homelab.UnsupportedVersionError
				if !errors.As(err, &ve) {
					t.Fatalf("Dial() error = %T, want *homelab.UnsupportedVersionError", err)
				}
				if diff := cmp.Diff(tt.wantVersion, ve); diff != "" {
					t.Errorf("UnsupportedVersionError mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(tt.wantMethods, s.methods()); diff != "" {
				t.Errorf("methods sent mismatch (-want +got):\n%s", diff)
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(logs.String(), testKey) {
				t.Errorf("the API key leaked into the error or the log")
			}
		})
	}
}

func TestDialUnreachable(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	_, err = Dial(t.Context(), Config{URL: "ws://" + addr}, testCreds())
	if !errors.Is(err, homelab.ErrUnavailable) {
		t.Fatalf("Dial(closed port) error = %v, want ErrUnavailable", err)
	}
	if !strings.HasPrefix(err.Error(), "TrueNAS unreachable at ws://"+addr) {
		t.Errorf("Dial(closed port) error = %q, want the unreachable message", err)
	}
}

func TestDialRefusesPlainRemote(t *testing.T) {
	_, err := Dial(t.Context(), Config{URL: "ws://truenas.lan"}, testCreds())
	if !errors.Is(err, homelab.ErrValidation) || !strings.Contains(err.Error(), "loopback host only") {
		t.Fatalf("Dial(ws://remote) error = %v, want the loopback-only ErrValidation", err)
	}
}

func TestDialScrubsKeyFromLoginError(t *testing.T) {
	echo := `{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params","data":{"error":22,"errname":"` + testKey + `",` +
		`"reason":"bad key ` + testKey + `","trace":null,"extra":[["auth_login_ex.api_key","value ` + testKey + ` rejected",22]]}}}`
	s := newFakeServer(t, withFixture("auth.echo", echo), withRoute("auth.login_ex", "auth.echo"))
	logger, logs := newTestLogger()
	_, err := Dial(t.Context(), Config{URL: s.url, Logger: logger}, testCreds())
	if !errors.Is(err, homelab.ErrValidation) {
		t.Fatalf("Dial() error = %v, want ErrValidation", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("Dial() error = %q contains the API key", err)
	}
	if strings.Contains(logs.String(), testKey) {
		t.Errorf("the log contains the API key reflected in errname:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "errname=EINVAL") {
		t.Errorf("the log lacks the errno-derived errname:\n%s", logs.String())
	}
}

func TestClientReads(t *testing.T) {
	s := newFakeServer(t)
	cl := dialFake(t, s, nil)
	ctx := t.Context()

	v, err := cl.SystemVersion(ctx)
	if err != nil || v != "TrueNAS-25.10.1" {
		t.Errorf("SystemVersion() = %q, %v, want TrueNAS-25.10.1", v, err)
	}

	ds, err := cl.DockerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(homelab.DockerStatus{Status: "RUNNING", Description: "Applications are running"}, ds); diff != "" {
		t.Errorf("DockerStatus() mismatch (-want +got):\n%s", diff)
	}

	datasets, err := cl.Datasets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	inherited := homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "ON", Compression: "LZ4", RecordSize: "128K"}
	db := root
	db.RecordSize = "16K"
	wantDatasets := []homelab.Dataset{
		{Name: "tank", Mountpoint: "/mnt/tank", Props: inherited},
		{Name: "tank/apps", Mountpoint: "/mnt/tank/apps", Props: inherited},
		{Name: "tank/apps/appconfigs", Mountpoint: "/mnt/tank/apps/appconfigs", Props: root},
		{Name: "tank/databases", Mountpoint: "/mnt/tank/databases", Props: db},
		{Name: "tank/media", Mountpoint: "/mnt/tank/media", Props: homelab.DatasetProps{
			ACLType: "NFSV4", ACLMode: "RESTRICTED", Atime: "OFF", Compression: "LZ4", RecordSize: "1M",
		}},
		{Name: "tank/secure", Encrypted: true, Locked: true, Props: root},
	}
	if diff := cmp.Diff(wantDatasets, datasets); diff != "" {
		t.Errorf("Datasets() mismatch (-want +got):\n%s", diff)
	}
	jsonEqual(t, "pool.dataset.query params",
		`[[["type","=","FILESYSTEM"]],{"extra":{"retrieve_children":false,"retrieve_user_props":false,`+
			`"properties":["acltype","aclmode","atime","compression","recordsize","mountpoint","encryption","keystatus"]}}]`,
		s.lastParams(t, "pool.dataset.query"))

	users, err := cl.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantUsers := []homelab.User{
		{ID: 1, UID: 0, GID: 0, Name: "root", Builtin: true},
		{ID: 30, UID: 568, GID: 568, Name: "apps", Builtin: true},
		{ID: 71, UID: 950, GID: 950, Name: "truenas_admin"},
		{ID: 72, UID: 1000, GID: 1000, Name: "homelab"},
	}
	if diff := cmp.Diff(wantUsers, users); diff != "" {
		t.Errorf("Users() mismatch (-want +got):\n%s", diff)
	}
	jsonEqual(t, "user.query params", `[[["local","=",true]]]`, s.lastParams(t, "user.query"))

	groups, err := cl.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 7 || groups[6] != (homelab.Group{ID: 45, GID: 1000, Name: "homelab"}) {
		t.Errorf("Groups() = %+v, want 7 groups ending with homelab 1000 (row 45)", groups)
	}

	st, err := cl.Stat(ctx, "/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(homelab.PathStat{Exists: true, Type: homelab.PathDirectory, Mode: 0o755}, st); diff != "" {
		t.Errorf("Stat(/mnt) mismatch (-want +got):\n%s", diff)
	}
	absent, err := cl.Stat(ctx, "/mnt/.orchestrator-smoke-absent")
	if err != nil || absent != (homelab.PathStat{}) {
		t.Errorf("Stat(absent) = %+v, %v, want PathStat{}, nil", absent, err)
	}

	nets, err := cl.Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 4 || nets[2].Name != "isolated" || nets[2].ComposeProject != "ix-homelab-networks" ||
		nets[2].Driver != "bridge" || nets[2].ID == "" {
		t.Errorf("Networks() = %+v, want the four managed networks", nets)
	}
	jsonEqual(t, "docker.network.query params", `[[["name","in",["home","proxy","isolated","databases"]]]]`,
		s.lastParams(t, "docker.network.query"))

	apps, err := cl.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantApps := []homelab.App{
		{Name: "truenas-orchestrator", State: "RUNNING", Custom: true},
		{Name: "homelab-networks", State: "RUNNING", Custom: true},
	}
	if diff := cmp.Diff(wantApps, apps); diff != "" {
		t.Errorf("Apps() mismatch (-want +got):\n%s", diff)
	}
	jsonEqual(t, "app.query params", `[[],{"extra":{"retrieve_config":false}}]`, s.lastParams(t, "app.query"))
}

func TestClientCreates(t *testing.T) {
	props := &homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "128K"}
	s := newFakeServer(t)
	cl := dialFake(t, s, nil)
	ctx := t.Context()

	if err := cl.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	jsonEqual(t, "group.create params", `[{"name":"homelab","gid":1000,"smb":false}]`, s.lastParams(t, "group.create"))

	spec := homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000, FullName: "Homelab containers (truenas-orchestrator)"}
	if err := cl.CreateUser(ctx, spec); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	jsonEqual(t, "user.create params", "["+string(EncodeUserCreate(spec, 45))+"]", s.lastParams(t, "user.create"))

	ds := homelab.DatasetSpec{Name: "tank/apps/appconfigs", Props: props}
	if err := cl.CreateDataset(ctx, ds); err != nil {
		t.Fatalf("CreateDataset() error = %v", err)
	}
	jsonEqual(t, "pool.dataset.create params", "["+string(EncodeDatasetCreate(ds))+"]", s.lastParams(t, "pool.dataset.create"))

	perm := homelab.PermissionSpec{Path: "/mnt/tank/apps/appconfigs", UID: 1000, GID: 1000, Mode: 0o750}
	if err := cl.SetPermissions(ctx, perm); err != nil {
		t.Fatalf("SetPermissions() error = %v", err)
	}
	jsonEqual(t, "filesystem.setperm params", "["+string(EncodeSetPerm(perm))+"]", s.lastParams(t, "filesystem.setperm"))

	app := homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose}
	if err := cl.CreateApp(ctx, app); err != nil {
		t.Fatalf("CreateApp() error = %v", err)
	}
	jsonEqual(t, "app.create params", "["+string(EncodeAppCreate(app))+"]", s.lastParams(t, "app.create"))

	for _, m := range s.methods() {
		if m == "core.get_jobs" {
			t.Errorf("non-legacy job replies polled core.get_jobs")
		}
	}
}

func TestClientCreateRefusals(t *testing.T) {
	s := newFakeServer(t)
	cl := dialFake(t, s, nil)
	ctx := t.Context()

	err := cl.CreateUser(ctx, homelab.UserSpec{Name: "x", UID: 4242, GID: 4242})
	if !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("CreateUser(unknown gid) error = %v, want ErrNotFound", err)
	}
	err = cl.CreateApp(ctx, homelab.AppSpec{Name: "truenas-orchestrator", Compose: "services: {}"})
	if !errors.Is(err, homelab.ErrForbidden) {
		t.Errorf("CreateApp(self) error = %v, want ErrForbidden", err)
	}
	err = cl.SetPermissions(ctx, homelab.PermissionSpec{Path: "/mnt/x", Mode: 0o4750})
	if !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("SetPermissions(setuid) error = %v, want ErrValidation", err)
	}
	for _, m := range s.methods() {
		if m == "user.create" || m == "app.create" || m == "filesystem.setperm" {
			t.Errorf("a refused mutation reached the server: %s", m)
		}
	}
}

func TestClientErrorReplies(t *testing.T) {
	s := newFakeServer(t,
		withRoute("pool.dataset.create", "pool.dataset.create.einval"),
		withRoute("docker.status", "error.method_not_found"),
		withRoute("user.query", "error.not_authenticated"),
	)
	cl := dialFake(t, s, nil)
	ctx := t.Context()

	err := cl.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps/appconfigs"})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("CreateDataset() error = %v, want *Error", err)
	}
	want := &Error{
		Method: "pool.dataset.create", Code: -32602, Errno: 22, Errname: "EINVAL",
		Reason: "[EINVAL] pool_dataset_create.name: Path /mnt/tank/apps/appconfigs already exists",
		Fields: []FieldError{{Field: "pool_dataset_create.name", Message: "Path /mnt/tank/apps/appconfigs already exists", Errno: 22}},
	}
	if diff := cmp.Diff(want, e); diff != "" {
		t.Errorf("CreateDataset() *Error mismatch (-want +got):\n%s", diff)
	}
	if !errors.Is(err, homelab.ErrValidation) {
		t.Errorf("CreateDataset() error does not wrap ErrValidation")
	}
	if got := err.Error(); got != "truenas: pool.dataset.create: EINVAL (pool_dataset_create.name: Path /mnt/tank/apps/appconfigs already exists)" {
		t.Errorf("CreateDataset() error text = %q", got)
	}
	if strings.Contains(err.Error(), "LOCALVALUE") {
		t.Errorf("the error trace leaked into the error text")
	}

	_, err = cl.DockerStatus(ctx)
	if !errors.Is(err, homelab.ErrUnavailable) {
		t.Errorf("DockerStatus(method missing) error = %v, want ErrUnavailable", err)
	}
	if got, want := err.Error(), "truenas: docker.status: METHOD_NOT_FOUND: Method does not exist: middleware API drift; run just smoke"; got != want {
		t.Errorf("DockerStatus(method missing) error = %q, want %q", got, want)
	}

	_, err = cl.Users(ctx)
	if !errors.Is(err, homelab.ErrUnauthenticated) {
		t.Errorf("Users(207) error = %v, want ErrUnauthenticated", err)
	}
}

func TestClientJobFallback(t *testing.T) {
	perm := homelab.PermissionSpec{Path: "/mnt/tank/apps/appconfigs", UID: 1000, GID: 1000, Mode: 0o750}
	app := homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose}
	tests := []struct {
		name      string
		opts      []fakeServerOption
		call      func(*Client) error
		wantIs    error
		wantPolls int
		jobID     string
	}{
		{
			name:      "setperm legacy job succeeds after polling",
			opts:      []fakeServerOption{withRoute("filesystem.setperm", "filesystem.setperm.legacy"), withRoute("core.get_jobs", "core.get_jobs.running", "core.get_jobs.running", "core.get_jobs.success")},
			call:      func(cl *Client) error { return cl.SetPermissions(t.Context(), perm) },
			wantPolls: 3,
			jobID:     "1234",
		},
		{
			name:      "setperm legacy job fails",
			opts:      []fakeServerOption{withRoute("filesystem.setperm", "filesystem.setperm.legacy"), withRoute("core.get_jobs", "core.get_jobs.running", "core.get_jobs.failed")},
			call:      func(cl *Client) error { return cl.SetPermissions(t.Context(), perm) },
			wantIs:    homelab.ErrForbidden,
			wantPolls: 2,
			jobID:     "1234",
		},
		{
			name:      "app.create legacy job succeeds",
			opts:      []fakeServerOption{withRoute("app.create", "app.create.legacy"), withRoute("core.get_jobs", "core.get_jobs.success")},
			call:      func(cl *Client) error { return cl.CreateApp(t.Context(), app) },
			wantPolls: 1,
			jobID:     "1240",
		},
		{
			name:      "app.create legacy job fails validation",
			opts:      []fakeServerOption{withRoute("app.create", "app.create.legacy"), withRoute("core.get_jobs", "core.get_jobs.failed_validation")},
			call:      func(cl *Client) error { return cl.CreateApp(t.Context(), app) },
			wantIs:    homelab.ErrValidation,
			wantPolls: 1,
			jobID:     "1240",
		},
		{
			name: "a non-job method's number reply is not a job id",
			opts: []fakeServerOption{withRoute("group.create", "app.create.legacy")},
			call: func(cl *Client) error { return cl.callJob(t.Context(), "group.create", []byte(`{}`)) },
		},
		{
			name: "app.create final result needs no polling",
			call: func(cl *Client) error { return cl.CreateApp(t.Context(), app) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeServer(t, tt.opts...)
			cl := dialFake(t, s, nil)
			err := tt.call(cl)
			if tt.wantIs == nil && err != nil {
				t.Fatalf("call error = %v", err)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("call error = %v, want %v", err, tt.wantIs)
			}
			polls := 0
			for _, r := range s.requests() {
				if r.Method == "core.get_jobs" {
					polls++
					jsonEqual(t, "core.get_jobs params",
						`[[["id","=",`+tt.jobID+`]],{"select":["id","state","error","exc_info"]}]`, string(r.Params))
				}
			}
			if polls != tt.wantPolls {
				t.Errorf("core.get_jobs calls = %d, want %d", polls, tt.wantPolls)
			}
		})
	}
	t.Run("validation job error fields", func(t *testing.T) {
		s := newFakeServer(t, withRoute("app.create", "app.create.legacy"), withRoute("core.get_jobs", "core.get_jobs.failed_validation"))
		cl := dialFake(t, s, nil)
		err := cl.CreateApp(t.Context(), app)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("CreateApp() error = %v, want *Error", err)
		}
		want := &Error{
			Method: "app.create", Code: codeInvalidParams, Errno: 22, Errname: "EINVAL",
			Reason: "[EINVAL] app_create.custom_compose_config_string: Failed 'docker compose config' validation",
			Fields: []FieldError{{Field: "app_create.custom_compose_config_string", Message: "Failed 'docker compose config' validation", Errno: 22}},
		}
		if diff := cmp.Diff(want, e); diff != "" {
			t.Errorf("CreateApp() *Error mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("failed job error text", func(t *testing.T) {
		s := newFakeServer(t, withRoute("filesystem.setperm", "filesystem.setperm.legacy"), withRoute("core.get_jobs", "core.get_jobs.failed"))
		cl := dialFake(t, s, nil)
		err := cl.SetPermissions(t.Context(), perm)
		want := "truenas: filesystem.setperm: EPERM: [EPERM] Filesystem permissions on path /mnt/tank/apps/appconfigs could not be changed"
		if err == nil || err.Error() != want {
			t.Errorf("SetPermissions() error = %v, want %q", err, want)
		}
	})
}

func TestEncoders(t *testing.T) {
	props := &homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "LZ4", RecordSize: "16K"}
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"group", EncodeGroupCreate(homelab.GroupSpec{Name: "homelab", GID: 1000}), `{"name":"homelab","gid":1000,"smb":false}`},
		{
			"user",
			EncodeUserCreate(homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000, FullName: "Homelab containers (truenas-orchestrator)"}, 45),
			`{"username":"homelab","uid":1000,"full_name":"Homelab containers (truenas-orchestrator)","group":45,"group_create":false,"password_disabled":true,"smb":false,"shell":"/usr/sbin/nologin","home":"/var/empty"}`,
		},
		{
			"dataset root",
			EncodeDatasetCreate(homelab.DatasetSpec{Name: "tank/databases", Props: props}),
			`{"name":"tank/databases","type":"FILESYSTEM","share_type":"GENERIC","acltype":"POSIX","aclmode":"DISCARD","atime":"OFF","compression":"LZ4","recordsize":"16K"}`,
		},
		{"dataset ancestor", EncodeDatasetCreate(homelab.DatasetSpec{Name: "tank/apps"}), `{"name":"tank/apps","type":"FILESYSTEM"}`},
		{
			"setperm",
			EncodeSetPerm(homelab.PermissionSpec{Path: "/mnt/tank/orchestrator", UID: 1000, GID: 1000, Mode: 0o700}),
			`{"path":"/mnt/tank/orchestrator","uid":1000,"gid":1000,"mode":"700","options":{"stripacl":true,"recursive":false,"traverse":false}}`,
		},
		{
			"setperm 0050",
			EncodeSetPerm(homelab.PermissionSpec{Path: "/p", Mode: 0o50}),
			`{"path":"/p","uid":0,"gid":0,"mode":"050","options":{"stripacl":true,"recursive":false,"traverse":false}}`,
		},
		{
			"app keeps compose text unescaped",
			EncodeAppCreate(homelab.AppSpec{Name: "x", Compose: "a: &anchor <b>\n"}),
			`{"app_name":"x","custom_app":true,"custom_compose_config_string":"a: &anchor <b>\n"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, string(tt.got)); diff != "" {
				t.Errorf("encoder mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAPIKeyNeverLogged drives every method, success and failure, through a debug logger and
// greps the output for the key.
func TestAPIKeyNeverLogged(t *testing.T) {
	logger, logs := newTestLogger()
	s := newFakeServer(t, withRoute("pool.dataset.create", "pool.dataset.create.einval"))
	cl := dialFake(t, s, logger)
	ctx := t.Context()
	_, _ = cl.SystemVersion(ctx)
	_, _ = cl.DockerStatus(ctx)
	_, _ = cl.Datasets(ctx)
	_, _ = cl.Users(ctx)
	_, _ = cl.Groups(ctx)
	_, _ = cl.Stat(ctx, "/mnt")
	_, _ = cl.Stat(ctx, "/mnt/.orchestrator-smoke-absent")
	_, _ = cl.Networks(ctx)
	_, _ = cl.Apps(ctx)
	_ = cl.CreateGroup(ctx, homelab.GroupSpec{Name: "homelab", GID: 1000})
	_ = cl.CreateUser(ctx, homelab.UserSpec{Name: "homelab", UID: 1000, GID: 1000})
	_ = cl.CreateDataset(ctx, homelab.DatasetSpec{Name: "tank/apps"})
	_ = cl.SetPermissions(ctx, homelab.PermissionSpec{Path: "/mnt/tank/apps", Mode: 0o750})
	_ = cl.CreateApp(ctx, homelab.AppSpec{Name: homelab.NetworksApp, Compose: homelab.NetworksAppCompose})

	bad := newFakeServer(t, withRoute("auth.login_ex", "auth.login_ex.auth_err"))
	_, _ = Dial(ctx, Config{URL: bad.url, Logger: logger}, testCreds())

	out := logs.String()
	if !strings.Contains(out, "method=auth.login_ex") || !strings.Contains(out, "errname=EINVAL") {
		t.Fatalf("debug log lacks the expected call lines:\n%s", out)
	}
	if strings.Contains(out, testKey) {
		t.Errorf("the API key appears in the log")
	}
	for _, leak := range []string{"LOCALVALUE", "api_key", "homelab-networks"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log contains %q: params, results or traces are logged", leak)
		}
	}
}

// truenasDefaultCert builds a certificate shaped like TrueNAS's truenas_default
// (truenas_crypto_utils generate_self_signed.py): self-signed RSA-2048, CN and SAN localhost,
// EKU serverAuth, no basic constraints.
func truenasDefaultCert(t *testing.T) (cert tls.Certificate, certPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Country: []string{"US"}, Organization: []string{"iXsystems Inc. dba TrueNAS"}, CommonName: "localhost",
			Province: []string{"Tennessee"}, Locality: []string{"Maryville"},
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(397 * 24 * time.Hour),
		DNSNames:    []string{"localhost"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

// TestDialTLSTrueNASDefaultCert reproduces D35's TLS setup: ORCH_TRUENAS_CA_FILE holds the UI
// certificate itself and ORCH_TRUENAS_TLS_SERVER_NAME is localhost, while the URL names another
// host (here 127.0.0.1, in production host.docker.internal).
func TestDialTLSTrueNASDefaultCert(t *testing.T) {
	cert, certPEM := truenasDefaultCert(t)
	caFile := filepath.Join(t.TempDir(), "truenas-ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM() = false")
	}
	tests := []struct {
		name   string
		tls    *tls.Config
		wantOK bool
	}{
		{"UI certificate as root, server name localhost", &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}, true},
		{"system roots", &tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS12}, false},
		{"wrong server name", &tls.Config{RootCAs: roots, ServerName: "truenas.lan", MinVersion: tls.VersionTLS12}, false},
		{"no server name override", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeServer(t, withTLS(cert))
			cl, err := Dial(t.Context(), Config{URL: s.url, TLS: tt.tls}, testCreds())
			if tt.wantOK {
				if err != nil {
					t.Fatalf("Dial() error = %v", err)
				}
				_ = cl.Close()
				return
			}
			if err == nil {
				_ = cl.Close()
				t.Fatal("Dial() error = nil, want a TLS failure")
			}
			if !errors.Is(err, homelab.ErrUnavailable) {
				t.Errorf("Dial() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

// TestDialStalledServer: a server that accepts and then never answers must not hold Dial past
// its context, and the result is an outage, not a bare context error.
func TestDialStalledServer(t *testing.T) {
	tests := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{"upgrade never answered", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
		{"login never answered", func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = ws.CloseNow() }()
			for { // read requests, never reply, until the client drops the connection
				if _, _, err := ws.Read(r.Context()); err != nil {
					return
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tt.handler))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := Dial(ctx, Config{URL: "ws://" + srv.Listener.Addr().String()}, testCreds())
			if d := time.Since(start); d > time.Second {
				t.Errorf("Dial() took %v, want it to give up with its 200 ms context", d)
			}
			if !errors.Is(err, homelab.ErrUnavailable) {
				t.Errorf("Dial() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

// TestDialUpgradeRefused: any non-101 answer to the upgrade leads to GET /api/versions.
func TestDialUpgradeRefused(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		versions   string
		wantIs     error
		wantMsg    string
		wantServed []string
	}{
		{
			name: "200 from a 25.04 server", status: http.StatusOK, versions: "api.versions.25.04",
			wantIs: homelab.ErrValidation, wantMsg: "none of which is v25.10.0",
			wantServed: []string{"v25.04.0", "v25.04.1", "v25.04.2"},
		},
		{
			name: "400 and no version list", status: http.StatusBadRequest,
			wantIs: homelab.ErrValidation, wantMsg: "pinned API v25.10.0 not served (HTTP 400)",
		},
		{
			name: "403 although the version is listed", status: http.StatusForbidden, versions: "api.versions",
			wantIs: homelab.ErrUnavailable, wantMsg: "lists API v25.10.0 but answered the websocket upgrade with HTTP 403",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeServer(t, withUpgradeStatus(tt.status, tt.versions))
			_, err := Dial(t.Context(), Config{URL: s.url}, testCreds())
			if !errors.Is(err, tt.wantIs) || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("Dial() error = %v, want %q wrapping %v", err, tt.wantMsg, tt.wantIs)
			}
			if tt.wantServed != nil {
				var ve *homelab.UnsupportedVersionError
				if !errors.As(err, &ve) {
					t.Fatalf("Dial() error = %T, want *homelab.UnsupportedVersionError", err)
				}
				if diff := cmp.Diff(tt.wantServed, ve.ServedAPIs); diff != "" {
					t.Errorf("ServedAPIs mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
