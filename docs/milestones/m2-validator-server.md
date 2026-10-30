# M2 — Validator server, auth and UI

**Status:** not started · **Depends on:** M1 · **Issue:** _(unset)_

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

- [ ] `GET /api/stacks` — list with status and finding counts
- [ ] `GET /api/stacks/{name}` — findings plus both resolved env scopes with provenance
- [ ] `GET /api/repo` — repo status
- [ ] `internal/gitrepo`: clone and fetch/pull a configured homelab repo (the first git write path)
- [ ] `POST /api/repo/fetch` — job; progress over SSE
- [ ] Auth: user table, argon2id via `alexedwards/argon2id`, `HttpOnly` `SameSite=Lax` session cookie, `Secure` from `ORCH_SECURE_COOKIES` (default true; TLS terminates at Traefik so it cannot be detected)
- [ ] First-run admin creation flow
- [ ] Bearer API tokens for CLI, with revocation
- [ ] Login rate limiting via `golang.org/x/time/rate`
- [ ] All routes except health and login require auth
- [ ] `.env` values redacted in every API response unless explicitly requested by an authenticated user
- [ ] sqlc tables `users`, `sessions`, `api_tokens`, `jobs` with goose migrations
- [ ] Every route registered through huma; `just check-api` kept green as routes are added

### Frontend

- [ ] Login screen
- [ ] Stack list with status badges and finding counts
- [ ] Stack detail: findings grouped by severity, each showing code, file, line, message and suggested fix
- [ ] Resolved-env view showing interpolation and container scopes as **separate** tables, with per-key provenance, values masked by default
- [ ] Repo status header with a fetch button

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

_(append as work happens)_
