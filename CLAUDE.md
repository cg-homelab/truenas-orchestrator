# truenas-orchestrator

Canonical instruction source for AI coding agents in this repository.

Opinionated homelab orchestrator for TrueNAS SCALE: a Go backend (huma on `http.ServeMux`, SQLite via
sqlc + goose) with an embedded SvelteKit SPA, managing a git-backed repo of Docker Compose stacks.

## Scope

Applies to the whole repository. Nested `CLAUDE.md` files, if any, add directory-local rules.

## Required reading

Before the first change in a session:

1. [docs/README.md](docs/README.md) — current state and how to resume.
2. [docs/architecture/go-guidelines.md](docs/architecture/go-guidelines.md) — the normative Go guide.
   Everything not stated here is stated there.
3. [docs/architecture/env-scopes.md](docs/architecture/env-scopes.md) — before touching anything that
   resolves environment variables.
4. [docs/decisions.md](docs/decisions.md) — settled decisions. Do not relitigate them silently; propose a
   superseding entry instead.

## Rule Priority

1. Hard rules in this file.
2. Hard rules in nested `CLAUDE.md` files.
3. Soft rules in this file.
4. Soft rules in nested `CLAUDE.md` files.

If two rules conflict, prefer the rule that is safer for the user's storage, secrets and running stacks.

## Hard Rules

### Secrets

- No secret value ever reaches a log, an error string, a job output, an API response or the audit log.
- Every secret is a `homelab.Secret` from the moment it is read. `Reveal()` only at the point of use.
- The audit log records that a secret was written, never its value. `.env` values are redacted in API
  responses unless explicitly revealed.

### Architecture

- `internal/homelab` and `internal/compose` import no I/O packages (`os`, `io/fs`, `net`, `net/http`,
  `database/sql`, `os/exec`). depguard enforces it; do not work around it.
- Imports follow the dependency direction in
  [components.md](docs/architecture/components.md); implementation packages never import each other.
- Never execute scripts from the homelab repo (D6). Setup is interpreted from `setup.toml` only.
- Never rewrite a stack's `compose.yaml` (D14). Customization is a `compose.override.yaml` overlay, and a
  test asserts the original is byte-identical.
- Every write under a storage root (`$APPCONFIG_STORAGE`, `$DATABASE_STORAGE`, an app dir) goes through
  `os.Root`.
- Every destructive operation has a dry-run preview, an explicit confirm and an audit entry (D9).

### Contracts

- Finding codes in [docs/validator-checks.md](docs/validator-checks.md) are stable: never renumber or
  reuse one. A new check gets a new code, added to the registry in the same change.
- SQL changes go through a new goose migration in `internal/sqlite/migrations/` and sqlc queries in
  `internal/sqlite/queries/`; run `just generate` and commit the generated code. Never edit a shipped
  migration or a generated file.
- Every route change keeps `just check-api` green: run `just generate` and commit
  `frontend/src/lib/api.d.ts`.

### Validation

- `just check && just test` pass before you report work as done. Do not add `//nolint` without the
  linter name and a reason.
- Never commit, push or open pull requests. Suggest a commit message; the developer commits.

## Soft Rules

- Small interfaces declared by the consumer; no interface until there is a fake or a second
  implementation (the day-one exceptions are listed in go-guidelines section 3.3).
- Table-driven tests, `go-cmp` diffs, golden files for validator output, hand-written fakes over mocks.
- `slog` with static messages and `snake_case` keys; the logger is passed in, never global.
- Prefer the standard library. A new dependency needs a reason in the change description.
- Wrap errors with context and `%w`; map domain errors to HTTP status only in `internal/server`.
- Keep docs current in the same change when commands, paths or behaviour change.

## Where Things Go

| Change | Location |
| --- | --- |
| Domain types, env-scope logic, validator checks, `ValidateRepo` pipeline, consumed interfaces | `internal/homelab/` |
| Compose parsing, `${VAR}` scope tagging, overlay rendering | `internal/compose/` |
| Git operations, layout walking, plaintext `SecretStore` | `internal/gitrepo/` |
| TrueNAS JSON-RPC client and `truenas.Fake` | `internal/truenas/` |
| Schema change | new `internal/sqlite/migrations/NNNN_name.sql` |
| Query | `internal/sqlite/queries/*.sql`, then `just generate` |
| API endpoint | handler in `internal/server/`, registered in `internal/server/routes.go` |
| CLI subcommand or config variable | `cmd/orchestrator/` |
| Frontend page or component | `frontend/src/` |
| Golden-file helper (only) | `internal/testutil/golden/` |
| Test inputs and golden outputs | `testdata/` beside the test |
| Shared fixture homelab repo | `fixtures/homelab-repo/` |
| New validator code | `docs/validator-checks.md` and `internal/homelab/` |
| Architectural decision | new entry in `docs/decisions.md` |

## Commands

| Command | Use |
| --- | --- |
| `just check` | Lint, format check, sqlc and API drift (diffed against temp output), govulncheck. Never writes |
| `just test` | `go test -race -shuffle=on ./...` and frontend tests |
| `just generate` | sqlc and the TypeScript API client. The only recipe that writes generated files |
| `just validate <path>` | Run the validator CLI against a homelab repo |
| `just dev` | Backend and vite against fixtures and the fake TrueNAS |

## Workflow

Milestones are M0–M7 in [docs/roadmap.md](docs/roadmap.md) (M1 = validator engine + CLI, M2 = server +
auth + UI). As work lands: tick the task boxes in the milestone file under `docs/milestones/`, append to
its Notes section, and update the status column in `docs/roadmap.md`. That is the handoff surface.
