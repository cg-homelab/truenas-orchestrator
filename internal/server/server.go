// Package server is the HTTP layer: huma operations on a stdlib http.ServeMux, middleware, and
// the embedded SPA. It contains no domain logic; it wires packages together and speaks HTTP.
package server

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

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
	// Store is the database.
	Store *sqlite.Store
}

// NewAPI registers every operation on mux. It must not call deps, so `orchestrator openapi`
// can build it with zero-value Deps.
func NewAPI(mux *http.ServeMux, deps Deps) huma.API {
	// The document's version is deps.Version or "dev", never build info, so the spec that
	// `orchestrator openapi` prints is identical however the binary was built.
	docVersion := deps.Version
	if docVersion == "" {
		docVersion = devVersion
	}
	cfg := huma.DefaultConfig(apiTitle, docVersion)
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", "" // spec comes from `orchestrator openapi`
	cfg.CreateHooks = nil                                       // no $schema links to the routes just removed
	api := humago.New(mux, cfg)
	api.UseMiddleware(authenticate()) // before any Register: huma binds middleware at registration
	addRoutes(mux, api, deps)
	return api
}

// NewServer returns the complete HTTP handler for `orchestrator serve`: every API operation,
// the SPA, and the stdlib middleware around them.
func NewServer(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	NewAPI(mux, deps)
	var h http.Handler = mux
	h = logRequests(logger)(h)
	h = recoverPanics(logger)(h)
	return h
}
