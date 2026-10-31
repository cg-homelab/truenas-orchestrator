package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// addRoutes registers everything the server exposes. Reading this function answers "what does
// this server expose". It must not call deps (see NewAPI). Operations whose Security is
// non-empty require a session cookie or a bearer token (authenticator.middleware).
func addRoutes(mux *http.ServeMux, api huma.API, deps Deps, svc *services) {
	logger, store := svc.logger, deps.Store
	var repo homelab.Repo // a nil Repo stays a nil interface
	if deps.Repo != nil {
		repo = deps.Repo
	}
	setup := setupDeps{repo: repo, secrets: deps.Secrets, host: deps.Host}
	tn := truenasDeps{dial: deps.TrueNAS, creds: deps.Credentials, url: deps.TrueNASURL, env: deps.Environment}
	environment := environmentDeps{truenas: tn, repo: repo, host: deps.Host}
	deployKey := deployKeyDeps{host: deps.Host, stateRoot: deps.Environment.StateStorage}

	// Public.
	huma.Register(api, opGetHealth, handleGetHealth(readBuildInfo(deps.Version)))
	huma.Register(api, opGetAuthStatus, handleGetAuthStatus(logger, store))
	huma.Register(api, opAuthSetup, handleAuthSetup(logger, store, svc.hasher, svc.now))
	huma.Register(api, opAuthLogin, handleAuthLogin(logger, store, svc.auth, svc.limiter, svc.hasher))

	// Authenticated.
	huma.Register(api, opAuthLogout, handleAuthLogout(logger, store, svc.auth))
	huma.Register(api, opGetMe, handleGetMe())
	huma.Register(api, opListTokens, handleListTokens(logger, store))
	huma.Register(api, opCreateToken, handleCreateToken(logger, store, svc.now))
	huma.Register(api, opDeleteToken, handleDeleteToken(logger, store))
	huma.Register(api, opGetRepo, handleGetRepo(logger, svc.reports))
	huma.Register(api, opFetchRepo, handleFetchRepo(logger, deps.Repo, svc.jobs))
	huma.Register(api, opGetJob, handleGetJob(logger, svc.jobs))
	sse.Register(api, opJobEvents, jobEventTypes, handleJobEvents(logger, svc.jobs))
	huma.Register(api, opListStacks, handleListStacks(logger, svc.reports))
	huma.Register(api, opGetStack, handleGetStack(logger, svc.reports))
	huma.Register(api, opGetStackEnv, handleGetStackEnv(logger, svc.reports, repo, deps.Secrets, svc.audit))
	huma.Register(api, opUpdateStackEnv, handleUpdateStackEnv(logger, svc.reports, repo, deps.Secrets, svc.audit, svc.envLocks))
	huma.Register(api, opPlanStackSetup, handlePlanStackSetup(logger, svc.reports, setup))
	huma.Register(api, opRunStackSetup, handleRunStackSetup(logger, svc.reports, setup, svc.jobs, svc.audit, svc.envLocks))
	huma.Register(api, opListAudit, handleListAudit(logger, store))
	huma.Register(api, opGetTrueNASSettings, handleGetTrueNASSettings(logger, tn))
	huma.Register(api, opPutTrueNASSettings, handlePutTrueNASSettings(logger, tn, svc.audit, svc.now))
	huma.Register(api, opDeleteTrueNASSettings, handleDeleteTrueNASSettings(logger, tn, svc.audit))
	huma.Register(api, opGetEnvironment, handleGetEnvironment(logger, environment))
	huma.Register(api, opRunBootstrap, handleRunBootstrap(logger, environment, svc.jobs, svc.audit))
	huma.Register(api, opGetDeployKey, handleGetDeployKey(logger, deployKey))
	huma.Register(api, opCreateDeployKey, handleCreateDeployKey(logger, deployKey, svc.audit))

	// Unknown API paths are a JSON 404, never the SPA.
	mux.Handle("/api/", handleAPINotFound(api))
	// Everything else is the SPA, with a fallback to index.html for client-side routes.
	mux.Handle("/", handleSPA(deps.SPA))
}
