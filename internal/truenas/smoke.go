package truenas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// smokeAction is the advice on a failed shape check.
const smokeAction = "middleware API changed: re-record fixtures with --record and update decode.go; " +
	"if the version is new, see D36"

// smokeTrim caps the entries kept from a recorded list result.
const smokeTrim = 5

// SmokeRow is one check of a smoke run.
type SmokeRow struct {
	// Check names the check, e.g. "datasets".
	Check string
	// Method is the middleware method or endpoint checked.
	Method string
	// OK reports a pass.
	OK bool
	// Detail says what was seen; on failure, what was expected and what to do.
	Detail string
}

// SmokeReport is the outcome of Smoke.
type SmokeReport struct {
	Rows   []SmokeRow
	Failed int
}

// Smoke checks a live TrueNAS against the shapes the client decodes, with read-only calls only,
// and prints one row per check to w. A non-empty record is a directory (fixtures/truenas/wire):
// the replies are written, scrubbed, to record/<major.minor>/<method>[.<case>].json for review.
//
// The error is non-nil when the run could not continue (bad configuration, no connection,
// rejected login) or the recording failed; individual failed checks only count in Failed.
func Smoke(ctx context.Context, cfg Config, creds homelab.TrueNASCredentials, w io.Writer,
	record string,
) (SmokeReport, error) {
	s := &smoker{w: w, rec: map[string][]byte{}}
	err := s.run(ctx, cfg, creds)
	if record != "" && len(s.rec) > 0 {
		if werr := s.write(record); werr != nil {
			return s.report, errors.Join(err, werr)
		}
	}
	return s.report, err
}

type smoker struct {
	w       io.Writer
	report  SmokeReport
	conn    *conn
	version string
	rec     map[string][]byte // fixture name -> scrubbed envelope
}

func (s *smoker) row(check, method string, ok bool, detail string) {
	s.report.Rows = append(s.report.Rows, SmokeRow{Check: check, Method: method, OK: ok, Detail: detail})
	status := "PASS"
	if !ok {
		status = "FAIL"
		s.report.Failed++
	}
	_, _ = fmt.Fprintf(s.w, "%s  %-12s %-22s %s\n", status, check, method, detail)
}

func (s *smoker) shapeFail(check, method, detail string) {
	s.row(check, method, false, detail+"; "+smokeAction)
}

func (s *smoker) run(ctx context.Context, cfg Config, creds homelab.TrueNASCredentials) error {
	ep, err := parseEndpoint(cfg.URL)
	if err != nil {
		return err
	}
	hc, _ := httpClient(cfg)
	served, verr := servedAPIs(ctx, hc, ep)
	if verr == nil {
		if body, err := json.Marshal(served); err == nil {
			s.recordHTTP("api.versions", body)
		}
	}
	c, ep, err := dial(ctx, cfg, loggerOrDiscard(cfg.Logger), defaultTimings())
	if err != nil {
		s.row("endpoint", "/api/"+APIVersion, false, err.Error())
		return err
	}
	s.conn = c
	defer func() { _ = c.close() }() // read-only session; nothing to report on close
	switch {
	case verr != nil:
		s.row("endpoint", "/api/versions", false, "upgrade ok; GET /api/versions failed: "+verr.Error())
	case !slices.Contains(served, APIVersion):
		s.shapeFail("endpoint", "/api/versions", fmt.Sprintf("upgrade ok; %s missing from served %v", APIVersion, served))
	default:
		s.row("endpoint", "/api/"+APIVersion, true, "upgrade ok; served "+strings.Join(served, ", "))
	}

	if err := login(ctx, c, creds); err != nil {
		err = handshakeError(ep, err)
		s.row("auth", "auth.login_ex", false, err.Error())
		return err
	}
	s.row("auth", "auth.login_ex", true, "SUCCESS")

	s.checkVersion(ctx)
	s.checkJobs(ctx)
	s.checkDocker(ctx)
	s.checkDatasets(ctx)
	s.checkUsers(ctx)
	s.checkGroups(ctx)
	s.checkStat(ctx)
	s.checkList(ctx, "networks", "docker.network.query", networkQueryParams(), requiredNetworkKeys,
		func(raw json.RawMessage) error { _, err := decodeNetworks(raw); return err })
	s.checkList(ctx, "apps", "app.query", appQueryParams(), requiredAppKeys,
		func(raw json.RawMessage) error { _, err := decodeApps(raw); return err })
	return nil
}

// call makes one read-only call and records its reply under name. A transport failure is
// reported as a failed row and returned as ok false.
func (s *smoker) call(ctx context.Context, check, method, name string, params []any) (json.RawMessage, *Error, bool) {
	msg, err := s.conn.roundTrip(ctx, method, params)
	if err != nil {
		s.row(check, method, false, err.Error())
		return nil, nil, false
	}
	s.recordReply(name, msg)
	if msg.Error != nil {
		return nil, newError(method, msg.Error), true
	}
	return msg.Result, nil, true
}

func (s *smoker) checkVersion(ctx context.Context) {
	raw, e, ok := s.call(ctx, "version", "system.version", "system.version", nil)
	if !ok {
		return
	}
	if e != nil {
		s.row("version", "system.version", false, e.Error())
		return
	}
	v, err := decodeString(raw)
	if err != nil {
		s.shapeFail("version", "system.version", "want a string: "+err.Error())
		return
	}
	s.version = v
	if err := homelab.CheckTrueNASVersion(v); err != nil {
		s.row("version", "system.version", false, err.Error())
		return
	}
	s.row("version", "system.version", true, v)
}

func (s *smoker) checkJobs(ctx context.Context) {
	raw, e, ok := s.call(ctx, "jobs", "core.set_options", "core.set_options", setOptionsParams())
	if !ok {
		return
	}
	if e != nil {
		s.row("jobs", "core.set_options", false, e.Error())
		return
	}
	var opts struct {
		LegacyJobs *bool `json:"legacy_jobs"`
	}
	if err := json.Unmarshal(raw, &opts); err != nil || opts.LegacyJobs == nil || *opts.LegacyJobs {
		s.shapeFail("jobs", "core.set_options", "want {legacy_jobs: false}")
		return
	}
	raw, e, ok = s.call(ctx, "jobs", "core.bulk", "core.bulk", []any{"core.ping", []any{[]any{}}})
	if !ok {
		return
	}
	if e != nil {
		s.row("jobs", "core.bulk", false, e.Error())
		return
	}
	if id, isID := jobID(raw); isID {
		s.row("jobs", "core.bulk", false, fmt.Sprintf("reply is job id %d, so legacy_jobs false is not honoured; "+
			"the client falls back to polling core.get_jobs, but report this (D35)", id))
		return
	}
	var results []json.RawMessage
	if err := json.Unmarshal(raw, &results); err != nil {
		s.shapeFail("jobs", "core.bulk", "want the bulk result list: "+err.Error())
		return
	}
	s.row("jobs", "core.bulk", true, "job reply is the final result (legacy_jobs false honoured)")
}

func (s *smoker) checkDocker(ctx context.Context) {
	raw, e, ok := s.call(ctx, "docker", "docker.status", "docker.status", nil)
	if !ok {
		return
	}
	if e != nil {
		s.row("docker", "docker.status", false, e.Error())
		return
	}
	d, err := decodeDockerStatus(raw)
	if err != nil || d.Status == "" {
		s.shapeFail("docker", "docker.status", "want {status, description}")
		return
	}
	s.row("docker", "docker.status", true, d.Status)
}

// entries splits a list result and reports the required keys missing from any entry; sub, when
// set, adds keys missing from nested objects.
func entries(raw json.RawMessage, keys []string, sub func(json.RawMessage) []string) (n int, missing []string, err error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, nil, fmt.Errorf("want a list: %w", err)
	}
	add := func(ks []string) {
		for _, k := range ks {
			if !slices.Contains(missing, k) {
				missing = append(missing, k)
			}
		}
	}
	for _, e := range list {
		add(missingKeys(e, keys))
		if sub != nil {
			add(sub(e))
		}
	}
	return len(list), missing, nil
}

func (s *smoker) checkList(ctx context.Context, check, method string, params []any, keys []string,
	dec func(json.RawMessage) error,
) {
	s.checkListDetail(ctx, check, method, params, keys, nil, dec, nil)
}

func (s *smoker) checkListDetail(ctx context.Context, check, method string, params []any, keys []string,
	sub func(json.RawMessage) []string, dec func(json.RawMessage) error, detail func(json.RawMessage) string,
) {
	raw, e, ok := s.call(ctx, check, method, method, params)
	if !ok {
		return
	}
	if e != nil {
		s.row(check, method, false, e.Error())
		return
	}
	n, missing, err := entries(raw, keys, sub)
	if err != nil {
		s.shapeFail(check, method, err.Error())
		return
	}
	if len(missing) > 0 {
		s.shapeFail(check, method, "missing keys: "+strings.Join(missing, ", "))
		return
	}
	if err := dec(raw); err != nil {
		s.shapeFail(check, method, err.Error())
		return
	}
	d := fmt.Sprintf("%d entries", n)
	if detail != nil {
		d += "; " + detail(raw)
	}
	s.row(check, method, true, d)
}

func (s *smoker) checkDatasets(ctx context.Context) {
	s.checkListDetail(ctx, "datasets", "pool.dataset.query", datasetQueryParams(), requiredDatasetKeys, nil,
		func(raw json.RawMessage) error { _, err := decodeDatasets(raw); return err },
		propertyCase)
}

// propertyCase reports the case of the property values as sent, before decode upper-cases them.
func propertyCase(raw json.RawMessage) string {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return "property case unknown"
	}
	var seen []string
	for _, d := range list {
		for _, k := range []string{"acltype", "aclmode", "atime", "compression"} {
			var p struct {
				Value *string `json:"value"`
			}
			if json.Unmarshal(d[k], &p) != nil || p.Value == nil || *p.Value == "" {
				continue
			}
			c := "upper"
			switch {
			case strings.ToUpper(*p.Value) == *p.Value:
			case strings.ToLower(*p.Value) == *p.Value:
				c = "lower"
			default:
				c = "mixed"
			}
			if !slices.Contains(seen, c) {
				seen = append(seen, c)
			}
		}
	}
	if len(seen) == 0 {
		return "property case unknown"
	}
	return "property values " + strings.Join(seen, "/") + "-case"
}

func (s *smoker) checkUsers(ctx context.Context) {
	groupKeys := func(e json.RawMessage) []string {
		var u struct {
			Group json.RawMessage `json:"group"`
		}
		if json.Unmarshal(e, &u) != nil || len(u.Group) == 0 || string(u.Group) == "null" {
			return nil // a missing group key is reported by the top-level check
		}
		var out []string
		for _, k := range missingKeys(u.Group, requiredUserGroupKeys) {
			out = append(out, "group."+k)
		}
		return out
	}
	s.checkListDetail(ctx, "users", "user.query", localFilter(), requiredUserKeys, groupKeys,
		func(raw json.RawMessage) error { _, err := decodeUsers(raw); return err }, nil)
}

func (s *smoker) checkGroups(ctx context.Context) {
	s.checkList(ctx, "groups", "group.query", localFilter(), requiredGroupKeys,
		func(raw json.RawMessage) error { _, err := decodeGroups(raw); return err })
}

func (s *smoker) checkStat(ctx context.Context) {
	const method = "filesystem.stat"
	if raw, e, ok := s.call(ctx, "stat", method, method, []any{"/mnt"}); ok {
		switch st, err := decodeStat(raw); {
		case e != nil:
			s.row("stat", method, false, e.Error())
		case len(missingKeys(raw, requiredStatKeys)) > 0:
			s.shapeFail("stat", method, "missing keys: "+strings.Join(missingKeys(raw, requiredStatKeys), ", "))
		case err != nil:
			s.shapeFail("stat", method, err.Error())
		case st.Type != homelab.PathDirectory:
			s.shapeFail("stat", method, fmt.Sprintf("/mnt: want type DIRECTORY, got %q", st.Type))
		default:
			s.row("stat", method, true, fmt.Sprintf("/mnt DIRECTORY %d:%d %s", st.UID, st.GID, st.Mode))
		}
	}

	if _, e, ok := s.call(ctx, "stat-absent", method, method+".enoent",
		[]any{"/mnt/.orchestrator-smoke-absent"}); ok {
		if e == nil || e.Errno != errnoNoEnt {
			s.shapeFail("stat-absent", method, fmt.Sprintf("want a CallError with errno ENOENT, got %v", errOrNil(e)))
		} else {
			s.row("stat-absent", method, true, "ENOENT decoded as absent")
		}
	}

	if _, e, ok := s.call(ctx, "validation", method, method+".einval", []any{""}); ok {
		if e == nil || e.Code != codeInvalidParams || e.Errname != "EINVAL" || len(e.Fields) == 0 {
			s.shapeFail("validation", method, fmt.Sprintf("want -32602 EINVAL with extra fields, got %v", errOrNil(e)))
		} else {
			s.row("validation", method, true, fmt.Sprintf("-32602 EINVAL, %d field(s)", len(e.Fields)))
		}
	}
}

func errOrNil(e *Error) any {
	if e == nil {
		return "a result"
	}
	return fmt.Sprintf("code %d errno %d (%s)", e.Code, e.Errno, e.Error())
}

// recordedEnvelope is the fixture format: a JSON-RPC reply without its id, plus provenance.
type recordedEnvelope struct {
	Provenance string          `json:"_provenance"`
	JSONRPC    string          `json:"jsonrpc,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	HTTPBody   json.RawMessage `json:"http_body,omitempty"`
}

func (s *smoker) recordReply(name string, msg inbound) {
	env := recordedEnvelope{JSONRPC: "2.0"}
	if msg.Error != nil {
		b, err := json.Marshal(msg.Error)
		if err != nil {
			return
		}
		env.Error = scrubJSON(b, false)
	} else {
		env.Result = scrubJSON(msg.Result, true)
	}
	s.rec[name] = mustEnvelope(env)
}

func (s *smoker) recordHTTP(name string, body []byte) {
	s.rec[name] = mustEnvelope(recordedEnvelope{HTTPBody: scrubJSON(body, false)})
}

func mustEnvelope(env recordedEnvelope) []byte {
	b, err := json.Marshal(env)
	if err != nil {
		return nil
	}
	return b
}

// scrubbedKeys are removed from recorded replies at any depth: password material, keys, and the
// error trace and py_exception, which hold stack frames with local variables.
var scrubbedKeys = []string{"unixhash", "smbhash", "password_history", "sshpubkey", "trace", "py_exception"}

// scrubJSON removes scrubbedKeys and, when trim is set and raw is a list, keeps its first
// smokeTrim entries.
func scrubJSON(raw json.RawMessage, trim bool) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	if list, ok := v.([]any); ok && trim && len(list) > smokeTrim {
		v = list[:smokeTrim]
	}
	b, err := json.Marshal(scrubValue(v))
	if err != nil {
		return nil
	}
	return b
}

func scrubValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range scrubbedKeys {
			delete(t, k)
		}
		for k, x := range t {
			t[k] = scrubValue(x)
		}
	case []any:
		for i, x := range t {
			t[i] = scrubValue(x)
		}
	}
	return v
}

// write saves the recorded replies under record/<major.minor>/ through os.Root.
func (s *smoker) write(record string) error {
	dir := "unknown"
	if v, err := homelab.ParseTrueNASVersion(s.version); err == nil {
		dir = fmt.Sprintf("%d.%d", v.Major, v.Minor)
	}
	provenance := fmt.Sprintf("recorded from %s by orchestrator smoke on %s; review before commit",
		s.version, time.Now().UTC().Format(time.DateOnly))
	if err := os.MkdirAll(filepath.Join(record, dir), 0o750); err != nil {
		return fmt.Errorf("smoke record: %w", err)
	}
	root, err := os.OpenRoot(filepath.Join(record, dir))
	if err != nil {
		return fmt.Errorf("smoke record: %w", err)
	}
	defer func() { _ = root.Close() }()
	names := make([]string, 0, len(s.rec))
	for name := range s.rec {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		var env recordedEnvelope
		if err := json.Unmarshal(s.rec[name], &env); err != nil {
			return fmt.Errorf("smoke record %s: %w", name, err)
		}
		env.Provenance = provenance
		b, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return fmt.Errorf("smoke record %s: %w", name, err)
		}
		if err := root.WriteFile(name+".json", append(b, '\n'), 0o644); err != nil {
			return fmt.Errorf("smoke record %s: %w", name, err)
		}
	}
	return nil
}
