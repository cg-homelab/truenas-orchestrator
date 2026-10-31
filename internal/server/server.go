// Package server is the HTTP layer: huma operations on a stdlib http.ServeMux, middleware, and
// the embedded SPA. It contains no domain logic; it wires packages together and speaks HTTP.
package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

// apiTitle is the OpenAPI document title.
const apiTitle = "truenas-orchestrator"

// Deps are the server's dependencies, constructed in run() and passed in. The zero value is
// enough to build the API description (see NewAPI).
type Deps struct {
	// Logger receives request and error logs. Nil discards them.
	Logger *slog.Logger
	// Version overrides the version reported by the health endpoint and the OpenAPI document.
	// Empty means "dev" in the document and, in the health endpoint, the module version from
	// the binary's build info, or "dev".
	Version string
	// SPA is the built frontend, served for every non-API path. Nil serves 404s.
	SPA fs.FS
	// Store is the database: accounts, sessions, API tokens and job records.
	Store *sqlite.Store
	// Repo is the homelab repo the stacks and repo endpoints validate and fetch.
	Repo Repo
	// Secrets reads and writes each app's env file for the env editor and setup runs. Nil makes
	// the env and setup endpoints fail with a 500, like a nil Repo.
	Secrets homelab.SecretStore
	// Host performs setup's writes under the storage roots. Nil makes the setup endpoints fail
	// with a 500.
	Host homelab.SetupHost
	// Validate configures homelab.ValidateRepo for every report the server builds.
	Validate homelab.Options
	// TrueNAS opens a session with TrueNAS. Nil reports the connection as not_configured and
	// refuses the operations that need one.
	TrueNAS TrueNASDialer
	// TrueNASURL is where TrueNAS is reached, shown in the settings; never used to connect.
	TrueNASURL string
	// Credentials stores the TrueNAS API key. Nil reports the connection as not_configured.
	Credentials CredentialStore
	// Environment configures the readiness check and the bootstrap. Global is ignored: the
	// handlers read compose/.global.env from the repo on every request.
	Environment homelab.EnvironmentConfig
	// SecureCookies sets the Secure flag on the session cookie. TLS terminates at the proxy, so
	// the server cannot detect it; false only for plain-HTTP local development.
	SecureCookies bool
	// SessionIdle ends a session after this long without a request. Zero means 24 h.
	SessionIdle time.Duration
	// SessionLifetime ends a session this long after login, however active. Zero means 7 days.
	SessionLifetime time.Duration
	// Now is the clock. Nil means time.Now; tests inject a fake one.
	Now func() time.Time
}

// fetcher updates the homelab repo from its remote. Only the server's fetch job needs it, so
// it is declared here rather than in homelab.Repo, which the validator consumes.
type fetcher interface {
	// Fetch runs `git fetch` followed by a fast-forward-only pull on the current branch. It
	// reports progress lines through report (may be nil) and returns the resulting status.
	Fetch(ctx context.Context, report func(line string)) (homelab.RepoStatus, error)
}

// Repo is the homelab repo as the server uses it: validated through homelab.Repo and updated
// through Fetch. *gitrepo.Repo implements it.
type Repo interface {
	homelab.Repo
	fetcher
}

// services is the server's own state shared by handlers. NewAPI builds one without starting
// anything; NewServer keeps it so Server.Run can execute jobs.
type services struct {
	logger  *slog.Logger
	now     func() time.Time
	auth    *authenticator
	limiter *loginLimiter
	hasher  *passwordHasher
	reports *reportCache
	jobs    *jobRunner
	audit   *auditor
	// envLocks serialises writes to each app's env file across requests and setup jobs.
	envLocks *envLocks
}

// newServices fills defaults into deps and builds the shared state. It must not call deps.
func newServices(deps Deps) *services {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	idle := deps.SessionIdle
	if idle <= 0 {
		idle = defaultSessionIdle
	}
	lifetime := deps.SessionLifetime
	if lifetime <= 0 {
		lifetime = defaultSessionLifetime
	}
	var repo homelab.Repo
	if deps.Repo != nil {
		repo = deps.Repo
	}
	reports := newReportCache(repo, deps.Validate, now)
	return &services{
		logger: logger,
		now:    now,
		auth: &authenticator{
			store: deps.Store, logger: logger, now: now,
			idle: idle, lifetime: lifetime, secure: deps.SecureCookies,
		},
		limiter:  newLoginLimiter(now),
		hasher:   newPasswordHasher(hashSlots),
		reports:  reports,
		jobs:     newJobRunner(deps.Store, logger, now, reports.invalidate),
		audit:    &auditor{store: deps.Store, logger: logger, now: now},
		envLocks: newEnvLocks(),
	}
}

// NewAPI registers every operation on mux. It must not call deps, so `orchestrator openapi`
// can build it with zero-value Deps.
func NewAPI(mux *http.ServeMux, deps Deps) huma.API {
	return newAPI(mux, deps, newServices(deps))
}

func newAPI(mux *http.ServeMux, deps Deps, svc *services) huma.API {
	// The document's version is deps.Version or "dev", never build info, so the spec that
	// `orchestrator openapi` prints is identical however the binary was built.
	docVersion := deps.Version
	if docVersion == "" {
		docVersion = devVersion
	}
	cfg := huma.DefaultConfig(apiTitle, docVersion)
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", "" // spec comes from `orchestrator openapi`
	cfg.CreateHooks = nil                                       // no $schema links to the routes just removed
	cfg.Components.SecuritySchemes = securitySchemes()
	cfg.Transformers = append(cfg.Transformers, dropErrorValues)
	api := humago.New(mux, cfg)
	api.UseMiddleware(svc.auth.middleware(api)) // before any Register: huma binds middleware at registration
	addRoutes(mux, api, deps, svc)
	return api
}

// Server is the complete HTTP handler for `orchestrator serve`, plus the background job runner
// its operations queue work on.
type Server struct {
	handler http.Handler
	jobs    *jobRunner
}

// dropErrorValues removes the offending input value that huma puts in each problem+json error
// detail. A request body that fails validation may carry a password or token, and no secret
// may appear in a response. A JSON syntax error message quotes a character of the body
// ("invalid character 'h' ..."), so it is replaced with a fixed one.
func dropErrorValues(_ huma.Context, _ string, v any) (any, error) {
	if m, ok := v.(*huma.ErrorModel); ok {
		for _, d := range m.Errors {
			if d == nil {
				continue
			}
			d.Value = nil
			if d.Location == "body" && (m.Status == http.StatusBadRequest || isJSONSyntaxMessage(d.Message)) {
				d.Message = msgInvalidJSON
			}
		}
	}
	return v, nil
}

// msgInvalidJSON replaces encoding/json syntax errors, which can quote the body.
const msgInvalidJSON = "invalid JSON"

func isJSONSyntaxMessage(msg string) bool {
	return strings.HasPrefix(msg, "invalid character") || strings.HasPrefix(msg, "unexpected end of JSON input")
}

// NewServer returns the server: every API operation, the SPA, and the stdlib middleware around
// them. Jobs queued through the API execute only while Run is running.
func NewServer(deps Deps) *Server {
	svc := newServices(deps)
	mux := http.NewServeMux()
	newAPI(mux, deps, svc)
	var h http.Handler = mux
	// Rejects cross-origin and same-site-but-cross-origin browser requests that change state
	// (sibling subdomains behind the same proxy are same-site, so SameSite=Lax alone does not
	// stop them). Requests without Sec-Fetch-Site or Origin, such as a bearer-token CLI, pass.
	h = http.NewCrossOriginProtection().Handler(h)
	h = logRequests(svc.logger)(h)
	h = recoverPanics(svc.logger)(h)
	return &Server{handler: h, jobs: svc.jobs}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Run executes background jobs until ctx is done, then returns nil. It first marks jobs that a
// previous process left queued or running as failed (interrupted); on return, the job that was
// running is cancelled through ctx and recorded, and jobs still queued are marked interrupted.
// The caller that starts Run owns its lifetime, e.g. in the errgroup that serves HTTP.
func (s *Server) Run(ctx context.Context) error {
	s.jobs.run(ctx)
	return nil
}
