# M2 — Validator server, auth and UI

**Status:** done · **Depends on:** M1 · **Issue:** _(unset)_

## Goal

The M1 report in the browser, behind login. The server exposes the validator over HTTP via huma, owns
authentication (D4), and the SPA renders stacks, findings, both resolved env scopes with provenance, and
repo status.

Auth ships here because the API is network-reachable from the moment it exists.

## Out of scope

- Any write to the homelab repo or the filesystem (M3)
- Any TrueNAS API call (M4)
- Deploy, manifests, overlays (M5)
- The catalog (M6)

## Tasks

### Server — `internal/server`, `internal/sqlite`

- [x] `GET /api/stacks` — list with status and finding counts
- [x] `GET /api/stacks/{name}` — findings plus both resolved env scopes with provenance
- [x] `GET /api/repo` — repo status
- [x] `internal/gitrepo`: clone and fetch/pull a configured homelab repo (the first git write path)
- [x] `POST /api/repo/fetch` — job; progress over SSE
- [x] Auth: user table, argon2id via `alexedwards/argon2id`, `HttpOnly` `SameSite=Lax` session cookie, `Secure` from `ORCH_SECURE_COOKIES` (default true; TLS terminates at Traefik so it cannot be detected)
- [x] First-run admin creation flow
- [x] Bearer API tokens for CLI, with revocation
- [x] Login rate limiting via `golang.org/x/time/rate`
- [x] All routes except health and login require auth
- [x] `.env` values redacted in every API response unless explicitly requested by an authenticated user
- [x] sqlc tables `users`, `sessions`, `api_tokens`, `jobs` with goose migrations
- [x] Every route registered through huma; `just check-api` kept green as routes are added

### Frontend

- [x] Login screen
- [x] Stack list with status badges and finding counts
- [x] Stack detail: findings grouped by severity, each showing code, file, line, message and suggested fix
- [x] Resolved-env view showing interpolation and container scopes as **separate** tables, with per-key provenance, values masked by default
- [x] Repo status header with a fetch button

## Acceptance criteria

1. Unauthenticated requests to any route other than health and login receive 401.
2. No secret value appears in any log line at any log level.
3. The UI shows exactly the findings `orchestrator validate` reports for the same repo.
4. The two env scopes are visibly distinct, with per-key provenance.
5. `.env` values are masked until revealed.
6. `just check-api` passes.

## Verification

```
just check && just test
just dev                             # log in, browse stacks, open a stack with findings
```

Manual checks:

- Open a stack detail and confirm the two env scopes are visibly distinct and each key shows where its value came from.
- Confirm `.env` values are masked until revealed.
- `grep -ri` the log output for a known secret value and confirm no match.

## Notes / decisions made during implementation

- 2026-10-30, server deviations:
  - The job runner is `Server.Run(ctx)`, started in the `serve` errgroup, not a separate package.
  - The sqlite store returns sqlite-package types for users, sessions, tokens and jobs.
  - `POST /api/auth/setup` returns 201 and does not log in; the SPA logs in with the same credentials right after. Minimum password length is 12 (422 with a problem+json `detail`, shown in the setup form).
  - No proxy headers are trusted, so behind Traefik login rate limiting is effectively global (all clients share one address).
  - The OpenAPI spec types arrays as `| null` (huma default) but they always encode as `[]` on the wire.
- 2026-10-30, new env vars: `ORCH_SESSION_IDLE` (default 24h), `ORCH_SESSION_LIFETIME` (default 168h), `ORCH_CHECK_HOST_PATHS` (default true; enables PATH001 host-path existence checks).
- 2026-10-30, justfile: `dev` and `dev-api` now depend on `_dev-homelab`, which creates a throwaway fixture repo at `dist/dev-homelab` (via `scripts/init-fixture-repo.sh`) so dev never touches the orchestrator's own checkout. `dev_env` sets `HOMELAB_STORAGE=dist/dev-homelab` and `ORCH_CHECK_HOST_PATHS=false` to match `orchestrator validate` defaults.
- 2026-10-30, e2e record (built binary, curl, fresh DB, dev fixture repo): auth status `needs_setup: true`; short password setup 422; setup 201; login 204 with cookie; `/api/stacks` 401 without cookie, 200 with it (ok-control Ready); `db-postgres` has ENV009 and COMPOSE001, values `[REDACTED]`, `?reveal=true` shows `REPLACE_THIS`; `POST /api/repo/fetch` 202, the job succeeds immediately with an empty log (no upstream) and the events stream sends only `done` for an already-finished job; `/` and `/stacks/db-postgres` return the SPA HTML. `just check && just test` pass.
- Still manual: walk the UI in a browser (`just dev`) to confirm acceptance criteria 3-5 visually; not done by the implementer.
- Security review fixes (2026-10-30):
  1. huma validation errors no longer echo the offending value (a transformer nils `errors[].value`), so passwords never appear in problem+json.
  2. `http.NewCrossOriginProtection` wraps the server, so same-site form posts cannot trigger fetch, logout or token creation (bearer CLIs are unaffected).
  3. The login limiter keys IPv6 by /64 and caps its map at 10k entries; argon2 work is bounded by a 4-slot semaphore.
  4. A non-Bearer `Authorization` header falls through to the cookie (Traefik basicAuth compatibility).
  5. Jobs dequeued during shutdown are recorded as "interrupted", and `fetch-repo` returns 503 while shutting down.
  6. Report validation runs under a non-cancellable 60 s context.
  7. The in-memory job log is capped at 2000 lines, with cursor-based SSE delivery.
  8. `Fetch` lives on a server-side `Repo` interface, not on `homelab.Repo`.
  9. `BaseContext` is non-cancellable so graceful shutdown works.
  10. `gitrepo.Fetch` refuses to run unless `HOMELAB_STORAGE` is a repository top level.
