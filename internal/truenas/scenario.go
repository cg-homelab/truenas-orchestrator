package truenas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// mntToken is replaced in a scenario file by FakeOptions.Mnt, so the same seed serves /mnt in
// tests and a local directory in development.
const mntToken = "@MNT@"

// FakeOptions adjusts a scenario as it is loaded.
type FakeOptions struct {
	// Mnt replaces every @MNT@ in the scenario; empty means "/mnt".
	Mnt string
	// SelfApp is the orchestrator's own app name, which CreateApp refuses, as Client does.
	SelfApp string
}

// scenarioFile is fixtures/truenas/scenarios/<name>.json. Results are raw middleware results,
// decoded by decode.go exactly as the client decodes the wire.
type scenarioFile struct {
	Description string                   `json:"description"`
	Version     string                   `json:"version"`
	Auth        string                   `json:"auth"`
	Results     scenarioResults          `json:"results"`
	Errors      map[string]scenarioError `json:"errors"`
}

type scenarioResults struct {
	DockerStatus json.RawMessage            `json:"docker.status"`
	Datasets     json.RawMessage            `json:"pool.dataset.query"`
	Users        json.RawMessage            `json:"user.query"`
	Groups       json.RawMessage            `json:"group.query"`
	Stat         map[string]json.RawMessage `json:"filesystem.stat"`
	Networks     json.RawMessage            `json:"docker.network.query"`
	Apps         json.RawMessage            `json:"app.query"`
}

// scenarioError is a JSON-RPC error object plus "once": fire on the first call only.
type scenarioError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Once    bool            `json:"once"`
}

// fakeMethods are the middleware methods the Fake answers, the keys a scenario's errors may use:
// the dial gate's three and the thirteen homelab.TrueNAS methods.
var fakeMethods = []string{
	"auth.login_ex", "system.version", "core.set_options",
	"docker.status", "pool.dataset.query", "pool.dataset.create", "user.query", "user.create",
	"group.query", "group.create", "filesystem.stat", "filesystem.setperm", "docker.network.query",
	"app.query", "app.create",
}

// loginResponse maps a scenario's auth field to the auth.login_ex response_type it simulates.
func loginResponse(auth string) (string, bool) {
	switch auth {
	case "", "ok":
		return "SUCCESS", true
	case "auth_err":
		return "AUTH_ERR", true
	case "expired":
		return "EXPIRED", true
	case "otp_required":
		return "OTP_REQUIRED", true
	}
	return "", false
}

// loadState reads scenarios/<name>.json from fsys.
func loadState(fsys fs.FS, name string, opts FakeOptions) (*fakeState, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || !fs.ValidPath(name) {
		return nil, fmt.Errorf("truenas fake: scenario name %q: %w", name, homelab.ErrValidation)
	}
	file := path.Join("scenarios", name+".json")
	data, err := fs.ReadFile(fsys, file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("truenas fake: scenario %s: %w", name, homelab.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("truenas fake: read scenario %s: %w", name, err)
	}
	mnt := opts.Mnt
	if mnt == "" {
		mnt = "/mnt"
	}
	quoted, err := json.Marshal(strings.TrimRight(mnt, "/"))
	if err != nil {
		return nil, fmt.Errorf("truenas fake: mnt %q: %w", mnt, err)
	}
	data = bytes.ReplaceAll(data, []byte(mntToken), quoted[1:len(quoted)-1]) // JSON-escaped, unquoted

	var sc scenarioFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("truenas fake: scenario %s: %w: %w", name, homelab.ErrValidation, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("truenas fake: scenario %s: data after the JSON document: %w", name, homelab.ErrValidation)
	}
	st, err := newFakeState(sc, opts.SelfApp)
	if err != nil {
		return nil, fmt.Errorf("truenas fake: scenario %s: %w: %w", name, homelab.ErrValidation, err)
	}
	st.scenario = name
	return st, nil
}

func newFakeState(sc scenarioFile, selfApp string) (*fakeState, error) {
	if sc.Version == "" {
		return nil, errors.New("version is required")
	}
	resp, ok := loginResponse(sc.Auth)
	if !ok {
		return nil, fmt.Errorf("auth %q: want ok, auth_err, expired or otp_required", sc.Auth)
	}
	st := &fakeState{
		version: sc.Version,
		login:   resp,
		selfApp: selfApp,
		stat:    map[string]homelab.PathStat{},
		errors:  map[string]*fakeError{},
	}
	var err error
	if len(sc.Results.DockerStatus) > 0 {
		if st.docker, err = decodeDockerStatus(sc.Results.DockerStatus); err != nil {
			return nil, err
		}
	}
	if st.datasets, err = decodeSeed(sc.Results.Datasets, decodeDatasets); err != nil {
		return nil, err
	}
	if st.users, err = decodeSeed(sc.Results.Users, decodeUsers); err != nil {
		return nil, err
	}
	if st.groups, err = decodeSeed(sc.Results.Groups, decodeGroups); err != nil {
		return nil, err
	}
	if st.networks, err = decodeSeed(sc.Results.Networks, decodeNetworks); err != nil {
		return nil, err
	}
	if st.apps, err = decodeSeed(sc.Results.Apps, decodeApps); err != nil {
		return nil, err
	}
	for p, raw := range sc.Results.Stat {
		s, err := decodeStat(raw)
		if err != nil {
			return nil, fmt.Errorf("filesystem.stat %s: %w", p, err)
		}
		st.stat[path.Clean(p)] = s
	}
	for _, u := range st.users {
		st.lastID = max(st.lastID, u.ID)
	}
	for _, g := range st.groups {
		st.lastID = max(st.lastID, g.ID)
	}
	for method, e := range sc.Errors {
		if !slices.Contains(fakeMethods, method) {
			return nil, fmt.Errorf("errors: %q is not a method the Fake answers (%s)", method, strings.Join(fakeMethods, ", "))
		}
		st.errors[method] = &fakeError{
			once: e.Once,
			wire: wireError{Code: e.Code, Message: e.Message, Data: e.Data},
		}
	}
	return st, nil
}

func decodeSeed[T any](raw json.RawMessage, dec func(json.RawMessage) ([]T, error)) ([]T, error) {
	if len(raw) == 0 {
		return []T{}, nil
	}
	return dec(raw)
}
