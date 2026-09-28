# M1 — Read-only validator (the MVP)

**Status:** not started · **Depends on:** M0 · **Issue:** _(unset)_

## Goal

Point the orchestrator at a homelab repo and get a correct, actionable report. It clones or pulls the
repo, parses every `compose.yaml`, resolves both environment scopes, runs every check in
[validator-checks.md](../validator-checks.md), and renders per-stack status in the UI with findings that
name the file, the line and a suggested fix.

Success is concrete: run it against the real `martin-homelab` and it reports exactly the defects listed
in the Context table of the plan — no more, no less.

Auth ships in this milestone because the API is network-reachable from the moment it exists.

## Out of scope

- Any write to the homelab repo or the filesystem (M2)
- Running `setup.toml` (M2) — it is only *parsed* here, for `[env_validators]` and `SETUP00x`
- Any TrueNAS API call (M3)
- Deploy, manifests, overlays (M4)
- The catalog (M5)

## Tasks

### Domain — `homelab-model`, no I/O

- [ ] Layout contract types: repo, app, env file, template/real pairing
- [ ] Deploy-env resolution: `.global.env` then `<app>/.env`, with orchestrator-injected `HOMELAB_STORAGE` beneath both; record provenance per key
- [ ] Container-scope resolution from the `env_file:` chain plus `environment:`
- [ ] Finding type: stable code, severity, file, line, message, suggested fix
- [ ] Stack status derivation: `Ready` / `NeedsSetup` / `Broken`
- [ ] Implement every check in `validator-checks.md`: `COMPOSE001-004`, `ENV001-010`, `PATH001-003`, `NET001-003`, `SETUP001-004`, `GIT001-003`
- [ ] `setup.toml` parsing for `[env_validators]` and schema validation only
- [ ] Value validators: `duration`, `port`, `identifier`, `absolute_path`, `non_empty`, `integer`, `bool`, `one_of`
- [ ] Secret-storage trait defined (not yet used for writes)

### Compose — `compose-model`

- [ ] Parse `compose.yaml` preserving line numbers
- [ ] Extract every variable reference **tagged with its scope** (interpolation vs container)
- [ ] Support `${VAR}`, `${VAR:-d}`, `${VAR-d}`, `${VAR:?e}`, `${VAR?e}`, `$VAR`; treat `$$` as a literal `$`
- [ ] A reference with a default resolves and is not reported missing
- [ ] Structural validation sufficient to catch `networks: databases`

### Repo — `homelab-repo`

- [ ] Clone and fetch/pull a configured homelab repo
- [ ] Walk `compose/` into the app model; read env files
- [ ] Report branch, last fetch time, dirty state, ahead/behind
- [ ] Detect env files tracked by git (`GIT001`)

### Server — `orchestrator-server`

- [ ] `GET /api/stacks` — list with status and finding counts
- [ ] `GET /api/stacks/{name}` — findings plus both resolved env scopes with provenance
- [ ] `GET /api/repo` — repo status
- [ ] `POST /api/repo/fetch` — job; progress over SSE
- [ ] Auth: user table, argon2id, `HttpOnly` `SameSite=Lax` session cookie, `Secure` behind TLS
- [ ] First-run admin creation flow
- [ ] Bearer API tokens for CLI, with revocation
- [ ] Login rate limiting
- [ ] All routes except health and login require auth
- [ ] `.env` values redacted in every API response unless explicitly requested by an authenticated user

### CLI

- [ ] `orchestrator validate <path>` prints findings to a terminal and exits non-zero on any error-severity finding

### Frontend

- [ ] Login screen
- [ ] Stack list with status badges and finding counts
- [ ] Stack detail: findings grouped by severity, each showing code, file, line, message and suggested fix
- [ ] Resolved-env view showing interpolation and container scopes as **separate** tables, with per-key provenance, values masked by default
- [ ] Repo status header with a fetch button

## Acceptance criteria

1. `just validate ../martin-homelab` reports every defect in the plan's Context table with the correct code, file and line, and reports nothing spurious.
2. Specifically detected, each with the right code:
   - `networks: databases` bare string → `COMPOSE001`
   - `${APPDATA_STORAGE}` undefined → `ENV001`
   - `${HTTP_PORT}` referenced while `PORT` is defined → `ENV001` plus `ENV008`
   - hardcoded `/mnt/fast/appdata/...` in authelia → `PATH002`
   - `METRICS_RETENTION_TIME=60` → `ENV007`
   - `authelia/setup.sh` present → `SETUP001`
3. A variable supplied only through `env_file:` but used in a `volumes:` entry is reported as `ENV003`. This is the check the whole design exists for.
4. `HOMELAB_STORAGE` is never reported as unresolved, and the resolved-env view shows its provenance as orchestrator-injected.
5. The control stack in the fixtures reports `Ready` with zero findings.
6. Snapshot tests over `fixtures/homelab-repo/` cover every implemented code.
7. Nothing in this milestone writes to the homelab repo or the filesystem, verified by running the validator against a read-only mount.
8. Unauthenticated requests to any route other than health and login receive 401.
9. No secret value appears in any log line at any log level.

## Verification

```
just test-validator                  # snapshot tests over fixtures — the inner loop
just validate ../martin-homelab      # must match the Context table exactly
just check && just test
just dev                             # log in, browse stacks, open a stack with findings
```

Manual checks:

- Open a stack detail and confirm the two env scopes are visibly distinct and each key shows where its value came from.
- Confirm `.env` values are masked until revealed.
- `chmod -R a-w` a copy of a homelab repo, run the validator against it, and confirm it completes with no error.
- `grep -ri` the log output for a known secret value and confirm no match.

## Notes / decisions made during implementation

_(append as work happens)_
