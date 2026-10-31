package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// CredentialStore keeps the TrueNAS API key (D34). *sqlite.Store implements it.
type CredentialStore interface {
	// TrueNASCredentials returns the stored credentials, or an error wrapping
	// homelab.ErrNotFound when none are stored.
	TrueNASCredentials(ctx context.Context) (homelab.TrueNASCredentials, error)
	// PutTrueNASCredentials stores creds, replacing any stored before.
	PutTrueNASCredentials(ctx context.Context, creds homelab.TrueNASCredentials, now time.Time) error
	// DeleteTrueNASCredentials removes the stored credentials, or returns an error wrapping
	// homelab.ErrNotFound when none are stored.
	DeleteTrueNASCredentials(ctx context.Context) error
}

// TrueNASConn is one authenticated, version-checked TrueNAS session.
type TrueNASConn interface {
	homelab.TrueNAS
	// Close ends the session.
	Close() error
}

// TrueNASDialer opens a session as creds: it connects, logs in and checks the version. It returns
// an error wrapping homelab.ErrUnauthenticated for a rejected key, an *homelab.UnsupportedVersionError
// for a version outside homelab.SupportedTrueNAS, and one wrapping homelab.ErrUnavailable when
// TrueNAS cannot be reached. No error text holds the key. truenas.Dial and truenas.FakeDialer
// implement it, adapted by cmd/orchestrator.
type TrueNASDialer func(ctx context.Context, creds homelab.TrueNASCredentials) (TrueNASConn, error)

// Connection states reported by get-environment.
const (
	connOK                 = "ok"
	connNotConfigured      = "not_configured"
	connUnreachable        = "unreachable"
	connAuthFailed         = "auth_failed"
	connUnsupportedVersion = "unsupported_version"
	connError              = "error"
)

// Messages for a connection that cannot be attempted.
const (
	msgNoTrueNASURL = "TrueNAS is not configured: set ORCH_TRUENAS_URL"
	msgNotLoggedIn  = "no TrueNAS API key is saved: enter one under TrueNAS settings"
	msgDialFailed   = "could not connect to TrueNAS; see the server log"
)

// truenasDeps are what the TrueNAS operations need.
type truenasDeps struct {
	dial  TrueNASDialer
	creds CredentialStore
	url   string
	env   homelab.EnvironmentConfig
}

// ConnectionView is the state of the TrueNAS connection.
type ConnectionView struct {
	State           string `json:"state" enum:"ok,not_configured,unreachable,auth_failed,unsupported_version,error"`
	Message         string `json:"message" doc:"Why the state is not ok, or the TrueNAS version when it is; never the key"`
	DetectedVersion string `json:"detected_version" doc:"The TrueNAS version, e.g. 25.10.1; empty when unknown"`
	Supported       string `json:"supported" doc:"The supported range, e.g. TrueNAS 25.10 through 27.x"`
}

// classifyDial maps a dial error to a connection state and a message for the user. The messages
// of the TrueNAS client name the user, the URL and the versions, never the key (D34).
func classifyDial(ctx context.Context, logger *slog.Logger, err error) ConnectionView {
	v := ConnectionView{Supported: homelab.SupportedTrueNAS.String()}
	var uv *homelab.UnsupportedVersionError
	switch {
	case errors.As(err, &uv):
		v.State, v.Message, v.DetectedVersion = connUnsupportedVersion, uv.Error(), uv.Detected
	case errors.Is(err, homelab.ErrUnauthenticated):
		v.State, v.Message = connAuthFailed, err.Error()
	case errors.Is(err, homelab.ErrUnavailable):
		v.State, v.Message = connUnreachable, err.Error()
	case errors.Is(err, homelab.ErrValidation):
		v.State, v.Message = connError, err.Error()
	default:
		logger.ErrorContext(ctx, "truenas connection failed", "error", err)
		v.State, v.Message = connError, msgDialFailed
	}
	return v
}

// dialError maps a dial error to the HTTP error of an operation that needs the connection.
func dialError(ctx context.Context, logger *slog.Logger, err error) error {
	var uv *homelab.UnsupportedVersionError
	switch {
	case errors.As(err, &uv):
		return huma.Error422UnprocessableEntity(uv.Error())
	case errors.Is(err, homelab.ErrUnauthenticated), errors.Is(err, homelab.ErrValidation):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, homelab.ErrUnavailable):
		return huma.Error502BadGateway(err.Error())
	}
	return apiError(ctx, logger, err)
}

// errNotLoggedIn is the stored-credentials lookup failing because none are stored.
var errNotLoggedIn = errors.New(msgNotLoggedIn)

// connect opens a session with the stored credentials. A nil dialer, or no stored key, is a
// ConnectionView with state not_configured and a nil conn; so is any dial failure, with its
// state. The error is for a failing store only.
func (d truenasDeps) connect(ctx context.Context, logger *slog.Logger) (TrueNASConn, ConnectionView, error) {
	notConfigured := func(msg string) ConnectionView {
		return ConnectionView{State: connNotConfigured, Message: msg, Supported: homelab.SupportedTrueNAS.String()}
	}
	if d.dial == nil {
		return nil, notConfigured(msgNoTrueNASURL), nil
	}
	if d.creds == nil {
		return nil, notConfigured(msgNotLoggedIn), nil
	}
	creds, err := d.creds.TrueNASCredentials(ctx)
	switch {
	case errors.Is(err, homelab.ErrNotFound):
		return nil, notConfigured(msgNotLoggedIn), nil
	case err != nil:
		return nil, ConnectionView{}, err
	}
	conn, err := d.dial(ctx, creds)
	if err != nil {
		return nil, classifyDial(ctx, logger, err), nil
	}
	return conn, ConnectionView{State: connOK, Supported: homelab.SupportedTrueNAS.String()}, nil
}

// dialStored opens a session with the stored credentials for work that cannot go on without
// one, such as a bootstrap job. Every failure is an error.
func (d truenasDeps) dialStored(ctx context.Context) (TrueNASConn, error) {
	if d.dial == nil {
		return nil, errors.New(msgNoTrueNASURL)
	}
	if d.creds == nil {
		return nil, errNotLoggedIn
	}
	creds, err := d.creds.TrueNASCredentials(ctx)
	if errors.Is(err, homelab.ErrNotFound) {
		return nil, errNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	return d.dial(ctx, creds)
}

// closeConn closes a session, logging a failure: the work it served is already done.
func closeConn(ctx context.Context, logger *slog.Logger, conn TrueNASConn) {
	if err := conn.Close(); err != nil {
		logger.WarnContext(ctx, "close truenas session", "error", err)
	}
}

// TrueNASSettings is how the orchestrator reaches TrueNAS. It never holds the key.
type TrueNASSettings struct {
	Configured bool   `json:"configured" doc:"Whether an API key is saved"`
	Username   string `json:"username" doc:"The TrueNAS user the key belongs to; empty when none is saved"`
	URL        string `json:"url" doc:"Where TrueNAS is reached (ORCH_TRUENAS_URL); empty when not configured"`
	SelfApp    string `json:"self_app" doc:"The orchestrator's own TrueNAS app, which it never modifies"`
}

// GetTrueNASSettingsOutput is the get-truenas-settings response.
type GetTrueNASSettingsOutput struct {
	Body TrueNASSettings
}

var opGetTrueNASSettings = huma.Operation{
	OperationID: "get-truenas-settings",
	Method:      http.MethodGet,
	Path:        "/api/truenas/settings",
	Summary:     "Get how the orchestrator reaches TrueNAS; never the API key",
	Tags:        []string{"truenas"},
	Security:    authenticated(),
}

func handleGetTrueNASSettings(
	logger *slog.Logger, d truenasDeps,
) func(context.Context, *struct{}) (*GetTrueNASSettingsOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetTrueNASSettingsOutput, error) {
		out := &GetTrueNASSettingsOutput{Body: TrueNASSettings{SelfApp: d.env.SelfApp}}
		if d.dial != nil {
			out.Body.URL = d.url
		}
		if d.creds == nil {
			return out, nil
		}
		creds, err := d.creds.TrueNASCredentials(ctx)
		switch {
		case errors.Is(err, homelab.ErrNotFound):
		case err != nil:
			return nil, apiError(ctx, logger, err)
		default:
			out.Body.Configured, out.Body.Username = true, creds.Username
		}
		return out, nil
	}
}

// TrueNASCredentialsInput is a TrueNAS API user and key.
type TrueNASCredentialsInput struct {
	Username string      `json:"username" minLength:"1" maxLength:"64" doc:"The TrueNAS user that owns the key, e.g. truenas_admin"`
	APIKey   secretField `json:"api_key" doc:"The API key; never echoed back, logged or returned"`
}

// PutTrueNASSettingsInput is the put-truenas-settings request.
type PutTrueNASSettingsInput struct {
	Body TrueNASCredentialsInput
}

// TrueNASSettingsSaved is the put-truenas-settings response body.
type TrueNASSettingsSaved struct {
	Configured bool   `json:"configured"`
	Version    string `json:"version" doc:"The TrueNAS version the key logged in to, e.g. TrueNAS-25.10.1"`
}

// PutTrueNASSettingsOutput is the put-truenas-settings response.
type PutTrueNASSettingsOutput struct {
	Body TrueNASSettingsSaved
}

var opPutTrueNASSettings = huma.Operation{
	OperationID: "put-truenas-settings",
	Method:      http.MethodPut,
	Path:        "/api/truenas/settings",
	Summary:     "Save the TrueNAS API key after logging in with it",
	Description: "Connects, logs in and checks the TrueNAS version with the submitted key, and saves it only " +
		"when all three succeed. A rejected key or an unsupported version is a 422 whose message says why " +
		"(for a version: the detected and the supported versions); an unreachable TrueNAS is a 502. The key " +
		"is never echoed back.",
	Tags:     []string{"truenas"},
	Security: authenticated(),
	Errors:   []int{http.StatusUnprocessableEntity, http.StatusBadGateway},
}

func handlePutTrueNASSettings(
	logger *slog.Logger, d truenasDeps, audit *auditor, now func() time.Time,
) func(context.Context, *PutTrueNASSettingsInput) (*PutTrueNASSettingsOutput, error) {
	return func(ctx context.Context, in *PutTrueNASSettingsInput) (*PutTrueNASSettingsOutput, error) {
		if d.dial == nil {
			return nil, huma.Error422UnprocessableEntity(msgNoTrueNASURL)
		}
		if d.creds == nil {
			return nil, apiError(ctx, logger, errors.New("no credential store configured"))
		}
		creds := homelab.TrueNASCredentials{Username: in.Body.Username, APIKey: in.Body.APIKey.Secret}
		conn, err := d.dial(ctx, creds)
		if err != nil {
			return nil, dialError(ctx, logger, err)
		}
		version, err := conn.SystemVersion(ctx)
		closeConn(ctx, logger, conn)
		if err != nil {
			return nil, dialError(ctx, logger, err)
		}
		if err := d.creds.PutTrueNASCredentials(ctx, creds, now()); err != nil {
			return nil, apiError(ctx, logger, err)
		}
		audit.record(ctx, auditTrueNASSettingsWrite, "truenas", map[string]string{"username": creds.Username})
		logger.InfoContext(ctx, "truenas credentials saved", "username", creds.Username)
		return &PutTrueNASSettingsOutput{Body: TrueNASSettingsSaved{Configured: true, Version: version}}, nil
	}
}

var opDeleteTrueNASSettings = huma.Operation{
	OperationID:   "delete-truenas-settings",
	Method:        http.MethodDelete,
	Path:          "/api/truenas/settings",
	Summary:       "Remove the saved TrueNAS API key",
	Tags:          []string{"truenas"},
	Security:      authenticated(),
	DefaultStatus: http.StatusNoContent,
}

func handleDeleteTrueNASSettings(
	logger *slog.Logger, d truenasDeps, audit *auditor,
) func(context.Context, *struct{}) (*struct{}, error) {
	return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if d.creds == nil {
			return nil, nil // huma: a nil output with a nil error is an empty 204
		}
		creds, err := d.creds.TrueNASCredentials(ctx)
		switch {
		case errors.Is(err, homelab.ErrNotFound):
			return nil, nil // nothing to remove: still a 204
		case err != nil:
			return nil, apiError(ctx, logger, err)
		}
		switch err := d.creds.DeleteTrueNASCredentials(ctx); {
		case errors.Is(err, homelab.ErrNotFound):
			return nil, nil // removed concurrently: still a 204
		case err != nil:
			return nil, apiError(ctx, logger, err)
		}
		audit.record(ctx, auditTrueNASSettingsDelete, "truenas", map[string]string{"username": creds.Username})
		return nil, nil // a 204 with no body
	}
}
