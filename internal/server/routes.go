package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// addRoutes registers everything the server exposes. Reading this function answers "what does
// this server expose". It must not call deps (see NewAPI).
func addRoutes(mux *http.ServeMux, api huma.API, deps Deps) {
	huma.Register(api, opGetHealth, handleGetHealth(readBuildInfo(deps.Version)))

	// Unknown API paths are a JSON 404, never the SPA.
	mux.Handle("/api/", handleAPINotFound(api))
	// Everything else is the SPA, with a fallback to index.html for client-side routes.
	mux.Handle("/", handleSPA(deps.SPA))
}
