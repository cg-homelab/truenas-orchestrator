// Package truenas talks to the TrueNAS middleware over its JSON-RPC 2.0 websocket API (D22, D35).
// Client is the real session; Fake is a stateful in-process implementation seeded from
// fixtures/truenas/scenarios; Smoke checks a live server against the shapes both decode.
package truenas

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// APIVersion is the pinned middleware API endpoint (D35). Its shapes are what decode.go reads on
// every supported release (D36).
const APIVersion = "v25.10.0"

const (
	dialTimeout    = 15 * time.Second
	defaultJobPoll = 250 * time.Millisecond
	maxJobPoll     = 2 * time.Second
	// maxVersionsBody bounds the unauthenticated GET /api/versions reply.
	maxVersionsBody = 1 << 20
)

// Config locates a TrueNAS server.
type Config struct {
	// URL is wss://host[:port]. ws:// is accepted for a loopback host only: TrueNAS revokes an API
	// key used over a connection it does not consider secure.
	URL string
	// TLS verifies the server; nil uses the system roots.
	TLS *tls.Config
	// SelfApp is the orchestrator's own app name, which CreateApp refuses (D11).
	SelfApp string
	// Logger receives method names, durations and errnames, never params or results. Nil discards.
	Logger *slog.Logger
}

// Client is one authenticated, version-checked middleware session. It is safe for concurrent
// use; Close ends it.
type Client struct {
	conn    *conn
	selfApp string
	jobPoll time.Duration
}

var _ homelab.TrueNAS = (*Client)(nil)

// Dial connects to cfg.URL + "/api/" + APIVersion, logs in with the API key, checks the version
// against homelab.SupportedTrueNAS and switches jobs to non-legacy replies. Errors wrap
// homelab.ErrUnavailable (unreachable, TLS), homelab.ErrUnauthenticated (rejected or revoked key)
// or homelab.ErrValidation (configuration, two-factor, *homelab.UnsupportedVersionError).
func Dial(ctx context.Context, cfg Config, creds homelab.TrueNASCredentials) (*Client, error) {
	return dialClient(ctx, cfg, creds, defaultTimings())
}

func dialClient(ctx context.Context, cfg Config, creds homelab.TrueNASCredentials, t connTimings) (*Client, error) {
	logger := loggerOrDiscard(cfg.Logger)
	c, ep, err := dial(ctx, cfg, logger, t)
	if err != nil {
		return nil, err
	}
	cl := &Client{conn: c, selfApp: cfg.SelfApp, jobPoll: defaultJobPoll}
	if err := cl.handshake(ctx, creds); err != nil {
		// No close handshake: the server may be the reason the handshake failed.
		_ = c.closeNow() // the handshake error is the one worth reporting
		return nil, handshakeError(ep, err)
	}
	return cl, nil
}

// handshakeError makes a timeout or cancellation during the login handshake an outage
// (ErrUnavailable) rather than a bare context error; transport errors already wrap it.
func handshakeError(ep endpoint, err error) error {
	if errors.Is(err, homelab.ErrUnavailable) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("TrueNAS at %s did not complete the login handshake: %w: %w", ep.display, homelab.ErrUnavailable, err)
	}
	return err
}

// Close ends the session. The close handshake is bounded: an unresponsive server is dropped after
// about a second.
func (cl *Client) Close() error { return cl.conn.close() }

func (cl *Client) handshake(ctx context.Context, creds homelab.TrueNASCredentials) error {
	if err := login(ctx, cl.conn, creds); err != nil {
		return err
	}
	v, err := cl.SystemVersion(ctx)
	if err != nil {
		return err
	}
	if err := homelab.CheckTrueNASVersion(v); err != nil {
		return err
	}
	if _, err := cl.conn.call(ctx, "core.set_options", setOptionsParams()); err != nil {
		return err
	}
	return nil
}

func loggerOrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l
}

// endpoint is a validated server URL.
type endpoint struct {
	ws      string // websocket URL of the pinned API
	http    string // scheme and host for plain HTTP requests
	display string // scheme and host, for messages
}

// parseEndpoint validates a wss://host[:port] URL. Errors never echo the raw URL, which might
// carry credentials or a key someone pasted into the wrong field.
func parseEndpoint(raw string) (endpoint, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return endpoint{}, fmt.Errorf("truenas URL is not a valid URL; want wss://host[:port]: %w", homelab.ErrValidation)
	}
	if u.User != nil {
		return endpoint{}, fmt.Errorf("truenas URL: credentials in the URL are not allowed: %w", homelab.ErrValidation)
	}
	display := u.Scheme + "://" + u.Host
	if u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return endpoint{}, fmt.Errorf("truenas URL %s: want wss://host[:port] with no path or query: %w",
			display, homelab.ErrValidation)
	}
	var httpScheme string
	switch u.Scheme {
	case "wss":
		httpScheme = "https"
	case "ws":
		if !isLoopback(u.Hostname()) {
			return endpoint{}, fmt.Errorf("truenas URL %s: ws:// is allowed for a loopback host only, "+
				"because TrueNAS revokes an API key used over plain HTTP; use wss://: %w", display, homelab.ErrValidation)
		}
		httpScheme = "http"
	default:
		return endpoint{}, fmt.Errorf("truenas URL %s: want wss://host[:port]: %w", display, homelab.ErrValidation)
	}
	return endpoint{
		ws:      display + "/api/" + APIVersion,
		http:    httpScheme + "://" + u.Host,
		display: display,
	}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// httpClient returns a client for cfg and the record of the connection it dials.
func httpClient(cfg Config) (*http.Client, *rawConn) {
	d := &net.Dialer{Timeout: dialTimeout}
	raw := &rawConn{}
	return &http.Client{Transport: &http.Transport{
		DialContext:           raw.dialContext(d),
		TLSClientConfig:       cfg.TLS.Clone(),
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: dialTimeout, // a server that accepts and then stalls the upgrade
	}}, raw
}

// dial opens the websocket and starts the session loops. An HTTP answer other than 101 to the
// upgrade means the server does not serve the pinned API: GET /api/versions names what it does.
func dial(ctx context.Context, cfg Config, logger *slog.Logger, t connTimings) (*conn, endpoint, error) {
	ep, err := parseEndpoint(cfg.URL)
	if err != nil {
		return nil, endpoint{}, err
	}
	if cfg.TLS != nil && cfg.TLS.InsecureSkipVerify {
		logger.WarnContext(ctx, "truenas tls verification disabled", "url", ep.display)
	}
	hc, raw := httpClient(cfg)
	ws, resp, err := websocket.Dial(ctx, ep.ws, &websocket.DialOptions{HTTPClient: hc})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, ep, notServedError(ctx, hc, ep, resp.StatusCode)
		}
		return nil, ep, fmt.Errorf("TrueNAS unreachable at %s: %w: %w", ep.display, homelab.ErrUnavailable, err)
	}
	return newConn(ctx, ws, raw, logger, t), ep, nil
}

func notServedError(ctx context.Context, hc *http.Client, ep endpoint, status int) error {
	served, err := servedAPIs(ctx, hc, ep)
	switch {
	case err != nil:
		// The listing's own error is text only: the outcome is a version problem, not an outage.
		return fmt.Errorf("TrueNAS at %s: pinned API %s not served (HTTP %d), and GET /api/versions failed (%s): "+
			"this orchestrator supports %s: %w",
			ep.display, APIVersion, status, err.Error(), homelab.SupportedTrueNAS, homelab.ErrValidation)
	case len(served) == 0:
		return fmt.Errorf("TrueNAS at %s: pinned API %s not served (HTTP %d), and GET /api/versions lists none: "+
			"this orchestrator supports %s: %w", ep.display, APIVersion, status, homelab.SupportedTrueNAS, homelab.ErrValidation)
	case slices.Contains(served, APIVersion):
		// Listed but refused: something between us and the middleware (a proxy, an allowlist).
		return fmt.Errorf("TrueNAS at %s lists API %s but answered the websocket upgrade with HTTP %d: %w",
			ep.display, APIVersion, status, homelab.ErrUnavailable)
	}
	return &homelab.UnsupportedVersionError{Supported: homelab.SupportedTrueNAS.String(), ServedAPIs: served}
}

// servedAPIs reads the unauthenticated GET /api/versions list.
func servedAPIs(ctx context.Context, hc *http.Client, ep endpoint) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.http+"/api/versions", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("api versions: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api versions: %w: %w", homelab.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api versions: HTTP %d: %w", resp.StatusCode, homelab.ErrUnavailable)
	}
	var served []string
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVersionsBody)).Decode(&served); err != nil {
		return nil, fmt.Errorf("api versions: decode: %w: %w", homelab.ErrUnavailable, err)
	}
	return served, nil
}

// login runs auth.login_ex with API_KEY_PLAIN. The key is revealed here and nowhere else.
func login(ctx context.Context, c *conn, creds homelab.TrueNASCredentials) error {
	key := creds.APIKey.Reveal()
	raw, err := c.call(ctx, "auth.login_ex", []any{map[string]string{
		"mechanism": "API_KEY_PLAIN",
		"username":  creds.Username,
		"api_key":   key,
	}})
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			scrubError(e, key)
		}
		return err
	}
	var r wireLogin
	if err := json.Unmarshal(raw, &r); err != nil {
		return driftError("auth.login_ex", err)
	}
	return loginResult(r.ResponseType, creds.Username)
}

// loginResult maps an auth.login_ex response_type to the session error. Dial and FakeDialer share
// it, so both report a rejected key the same way.
func loginResult(responseType, username string) error {
	switch responseType {
	case "SUCCESS":
		return nil
	case "AUTH_ERR":
		return fmt.Errorf("TrueNAS rejected the API key for user %s: %w", username, homelab.ErrUnauthenticated)
	case "EXPIRED":
		return fmt.Errorf("the API key for user %s is expired or revoked; TrueNAS revokes a key used over plain HTTP: %w",
			username, homelab.ErrUnauthenticated)
	case "OTP_REQUIRED", "REDIRECT":
		return fmt.Errorf("two-factor login is not supported for API keys: %w", homelab.ErrValidation)
	}
	return fmt.Errorf("truenas: auth.login_ex: unexpected response_type %q (middleware API drift; run just smoke): %w",
		responseType, homelab.ErrUnavailable)
}

// driftError reports a result that does not decode into the v25.10.0 shape.
func driftError(method string, err error) error {
	return fmt.Errorf("truenas: %s: unexpected result shape (middleware API drift; run just smoke): %w: %w",
		method, homelab.ErrUnavailable, err)
}

func setOptionsParams() []any { return []any{map[string]bool{"legacy_jobs": false}} }

func datasetQueryParams() []any {
	return []any{
		[]any{[]any{"type", "=", "FILESYSTEM"}},
		map[string]any{"extra": map[string]any{
			"retrieve_children":   false,
			"retrieve_user_props": false,
			// mountpoint, encryption and keystatus must be listed too: with a properties list the
			// middleware fills mountpoint, encrypted and locked only from fetched properties
			// (pool_/dataset.py __transform; get_instance_quick adds the same three).
			"properties": []string{
				"acltype", "aclmode", "atime", "compression", "recordsize",
				"mountpoint", "encryption", "keystatus",
			},
		}},
	}
}

func localFilter() []any { return []any{[]any{[]any{"local", "=", true}}} }

func networkQueryParams() []any {
	return []any{[]any{[]any{"name", "in", homelab.HomelabNetworks}}}
}

func appQueryParams() []any {
	return []any{[]any{}, map[string]any{"extra": map[string]any{"retrieve_config": false}}}
}

// query calls method and decodes the result with dec.
func query[T any](ctx context.Context, c *conn, method string, params []any,
	dec func(json.RawMessage) (T, error),
) (T, error) {
	var zero T
	raw, err := c.call(ctx, method, params)
	if err != nil {
		return zero, err
	}
	v, err := dec(raw)
	if err != nil {
		return zero, driftError(method, err)
	}
	return v, nil
}

// SystemVersion returns the system.version string, e.g. "TrueNAS-25.10.1".
func (cl *Client) SystemVersion(ctx context.Context) (string, error) {
	return query(ctx, cl.conn, "system.version", nil, decodeString)
}

// DockerStatus returns docker.status.
func (cl *Client) DockerStatus(ctx context.Context) (homelab.DockerStatus, error) {
	return query(ctx, cl.conn, "docker.status", nil, decodeDockerStatus)
}

// Datasets returns every filesystem dataset.
func (cl *Client) Datasets(ctx context.Context) ([]homelab.Dataset, error) {
	return query(ctx, cl.conn, "pool.dataset.query", datasetQueryParams(), decodeDatasets)
}

// CreateDataset creates one dataset with EncodeDatasetCreate's payload.
func (cl *Client) CreateDataset(ctx context.Context, spec homelab.DatasetSpec) error {
	_, err := cl.conn.call(ctx, "pool.dataset.create", []any{json.RawMessage(EncodeDatasetCreate(spec))})
	return err
}

// Users returns the local users.
func (cl *Client) Users(ctx context.Context) ([]homelab.User, error) {
	return query(ctx, cl.conn, "user.query", localFilter(), decodeUsers)
}

// CreateUser resolves spec.GID to the group's row id, then creates the user with
// EncodeUserCreate's payload. A gid no local group holds is an error wrapping ErrNotFound.
func (cl *Client) CreateUser(ctx context.Context, spec homelab.UserSpec) error {
	groups, err := cl.Groups(ctx)
	if err != nil {
		return err
	}
	id, err := groupRowID(groups, spec)
	if err != nil {
		return err
	}
	_, err = cl.conn.call(ctx, "user.create", []any{json.RawMessage(EncodeUserCreate(spec, id))})
	return err
}

func groupRowID(groups []homelab.Group, spec homelab.UserSpec) (int, error) {
	for _, g := range groups {
		if g.GID == spec.GID {
			return g.ID, nil
		}
	}
	return 0, fmt.Errorf("truenas: user.create %s: no local group has gid %d: %w", spec.Name, spec.GID, homelab.ErrNotFound)
}

// Groups returns the local groups.
func (cl *Client) Groups(ctx context.Context) ([]homelab.Group, error) {
	return query(ctx, cl.conn, "group.query", localFilter(), decodeGroups)
}

// CreateGroup creates a group with EncodeGroupCreate's payload.
func (cl *Client) CreateGroup(ctx context.Context, spec homelab.GroupSpec) error {
	_, err := cl.conn.call(ctx, "group.create", []any{json.RawMessage(EncodeGroupCreate(spec))})
	return err
}

// Stat returns filesystem.stat for path; an absent path (ENOENT) is PathStat{} and nil.
func (cl *Client) Stat(ctx context.Context, path string) (homelab.PathStat, error) {
	st, err := query(ctx, cl.conn, "filesystem.stat", []any{path}, decodeStat)
	var e *Error
	if errors.As(err, &e) && e.Errno == errnoNoEnt {
		return homelab.PathStat{}, nil
	}
	return st, err
}

// SetPermissions runs the filesystem.setperm job with EncodeSetPerm's payload.
func (cl *Client) SetPermissions(ctx context.Context, spec homelab.PermissionSpec) error {
	if err := checkPermissionSpec(spec); err != nil {
		return err
	}
	return cl.callJob(ctx, "filesystem.setperm", EncodeSetPerm(spec))
}

func checkPermissionSpec(spec homelab.PermissionSpec) error {
	if spec.Mode > 0o777 {
		return fmt.Errorf("truenas: filesystem.setperm %s: mode %s has bits above 0777: %w",
			spec.Path, spec.Mode, homelab.ErrValidation)
	}
	return nil
}

// Networks returns the homelab networks (homelab.HomelabNetworks) that exist.
func (cl *Client) Networks(ctx context.Context) ([]homelab.Network, error) {
	return query(ctx, cl.conn, "docker.network.query", networkQueryParams(), decodeNetworks)
}

// Apps returns the installed apps.
func (cl *Client) Apps(ctx context.Context) ([]homelab.App, error) {
	return query(ctx, cl.conn, "app.query", appQueryParams(), decodeApps)
}

// CreateApp runs the app.create job with EncodeAppCreate's payload. The orchestrator's own app is
// refused before anything is sent.
func (cl *Client) CreateApp(ctx context.Context, spec homelab.AppSpec) error {
	if err := homelab.CheckAppMutation(cl.selfApp, spec.Name); err != nil {
		return err
	}
	return cl.callJob(ctx, "app.create", EncodeAppCreate(spec))
}

// jobMethod reports the methods callJob may treat as returning a legacy job id.
func jobMethod(method string) bool {
	return method == "filesystem.setperm" || method == "app.create"
}

// callJob calls a job method. With legacy_jobs false the reply is the job's final result. If the
// reply is a bare number instead, the server answered in legacy mode with a job id, and the job
// is polled through core.get_jobs. core.job_wait cannot serve here: it is itself a job, so in
// legacy mode it only returns another id (core_service.py, method.py on stable/goldeye).
func (cl *Client) callJob(ctx context.Context, method string, arg []byte) error {
	raw, err := cl.conn.call(ctx, method, []any{json.RawMessage(arg)})
	if err != nil {
		return err
	}
	if !jobMethod(method) {
		return nil
	}
	if id, ok := jobID(raw); ok {
		return cl.waitJob(ctx, method, id)
	}
	return nil
}

func jobID(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return 0, false
	}
	var id int64
	if json.Unmarshal(raw, &id) != nil {
		return 0, false
	}
	return id, true
}

func (cl *Client) waitJob(ctx context.Context, method string, id int64) error {
	delay := cl.jobPoll
	for {
		jobs, err := query(ctx, cl.conn, "core.get_jobs", getJobsParams(id), decodeJobs)
		if err != nil {
			return fmt.Errorf("truenas: %s: wait for job %d: %w", method, id, err)
		}
		if len(jobs) == 0 {
			return fmt.Errorf("truenas: %s: job %d vanished: %w", method, id, homelab.ErrUnavailable)
		}
		switch jobs[0].State {
		case "SUCCESS":
			return nil
		case "FAILED", "ABORTED":
			return jobError(method, jobs[0])
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("truenas: %s: wait for job %d: %w", method, id, ctx.Err())
		case <-t.C:
		}
		delay = min(2*delay, maxJobPoll)
	}
}

// getJobsParams selects only the fields jobError reads, so a job's arguments and credentials
// never cross the wire.
func getJobsParams(id int64) []any {
	return []any{
		[]any{[]any{"id", "=", id}},
		map[string]any{"select": []string{"id", "state", "error", "exc_info"}},
	}
}

func decodeJobs(raw json.RawMessage) ([]wireJob, error) {
	var jobs []wireJob
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}
	return jobs, nil
}

func jobError(method string, j wireJob) *Error {
	e := &Error{Method: method, Reason: cleanReason(deref(j.Error))}
	if j.ExcInfo != nil {
		if j.ExcInfo.Errno != nil {
			e.Errno = *j.ExcInfo.Errno
		}
		// job.py Job.__encode__ reports ValidationError(s) as type "VALIDATION" (stable/goldeye);
		// the class names are accepted too.
		if t := deref(j.ExcInfo.Type); t == "VALIDATION" || t == "ValidationError" || t == "ValidationErrors" {
			e.Code = codeInvalidParams
			e.Fields = decodeFields(j.ExcInfo.Extra)
			e.Errno = errnoInval // as on the wire; a ValidationError's own errno is in its field
		}
	}
	e.Errname = errnoName(e.Errno)
	if e.Reason == "" {
		e.Reason = "job " + strings.ToLower(j.State)
	}
	return e
}

// The encoders return the single argument each create call sends (params is a one-element array
// holding it). deploy/bootstrap.sh passes the same JSON to midclt, and a test compares the two.

type groupCreate struct {
	Name string `json:"name"`
	GID  int    `json:"gid"`
	SMB  bool   `json:"smb"`
}

// EncodeGroupCreate returns the group.create argument for spec.
func EncodeGroupCreate(spec homelab.GroupSpec) []byte {
	return encodeJSON(groupCreate{Name: spec.Name, GID: spec.GID})
}

type userCreate struct {
	Username         string `json:"username"`
	UID              int    `json:"uid"`
	FullName         string `json:"full_name"`
	Group            int    `json:"group"`
	GroupCreate      bool   `json:"group_create"`
	PasswordDisabled bool   `json:"password_disabled"`
	SMB              bool   `json:"smb"`
	Shell            string `json:"shell"`
	Home             string `json:"home"`
}

// EncodeUserCreate returns the user.create argument for spec, whose primary group is the group row
// groupID (the middleware's id, not the gid).
func EncodeUserCreate(spec homelab.UserSpec, groupID int) []byte {
	return encodeJSON(userCreate{
		Username:         spec.Name,
		UID:              spec.UID,
		FullName:         spec.FullName,
		Group:            groupID,
		PasswordDisabled: true,
		Shell:            "/usr/sbin/nologin",
		Home:             "/var/empty",
	})
}

type datasetCreate struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	ShareType   string `json:"share_type,omitempty"`
	ACLType     string `json:"acltype,omitempty"`
	ACLMode     string `json:"aclmode,omitempty"`
	Atime       string `json:"atime,omitempty"`
	Compression string `json:"compression,omitempty"`
	RecordSize  string `json:"recordsize,omitempty"`
}

// EncodeDatasetCreate returns the pool.dataset.create argument for spec: a storage root with
// share_type GENERIC and its properties, or, when spec.Props is nil, a plain ancestor that
// inherits everything.
func EncodeDatasetCreate(spec homelab.DatasetSpec) []byte {
	d := datasetCreate{Name: spec.Name, Type: "FILESYSTEM"}
	if p := spec.Props; p != nil {
		d.ShareType = "GENERIC"
		d.ACLType, d.ACLMode, d.Atime = p.ACLType, p.ACLMode, p.Atime
		d.Compression, d.RecordSize = p.Compression, p.RecordSize
	}
	return encodeJSON(d)
}

type setPerm struct {
	Path    string         `json:"path"`
	UID     int            `json:"uid"`
	GID     int            `json:"gid"`
	Mode    string         `json:"mode"`
	Options setPermOptions `json:"options"`
}

type setPermOptions struct {
	StripACL  bool `json:"stripacl"`
	Recursive bool `json:"recursive"`
	Traverse  bool `json:"traverse"`
}

// EncodeSetPerm returns the filesystem.setperm argument for spec: owner and an octal mode string
// on the one directory, stripping any ACL, never recursive.
func EncodeSetPerm(spec homelab.PermissionSpec) []byte {
	return encodeJSON(setPerm{
		Path:    spec.Path,
		UID:     spec.UID,
		GID:     spec.GID,
		Mode:    fmt.Sprintf("%03o", uint32(spec.Mode)&0o777),
		Options: setPermOptions{StripACL: true},
	})
}

type appCreate struct {
	AppName string `json:"app_name"`
	Custom  bool   `json:"custom_app"`
	Compose string `json:"custom_compose_config_string"`
}

// EncodeAppCreate returns the app.create argument for a custom compose app.
func EncodeAppCreate(spec homelab.AppSpec) []byte {
	return encodeJSON(appCreate{AppName: spec.Name, Custom: true, Compose: spec.Compose})
}

// encodeJSON marshals a payload struct of strings, ints and bools, without HTML escaping so that
// compose text stays readable. It cannot fail for those types.
func encodeJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("truenas: encode %T: %v", v, err)) // unreachable for the payload structs
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
