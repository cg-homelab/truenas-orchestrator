# Components

## Repository layout

```
truenas-orchestrator/
├── justfile
├── go.mod                        # single module, go 1.27; sqlc + goose via the `tool` directive
├── .golangci.yml                 # strict lint set; depguard keeps the domain I/O-free (D26)
├── cmd/
│   └── orchestrator/             # main + run(ctx, args, environ, stdout): serve | validate | openapi
├── internal/
│   ├── homelab/                  # pure domain: layout, env scopes, validation, interfaces. No I/O
│   ├── compose/                  # parse compose.yaml, ${VAR} per scope, render manifests. No I/O
│   ├── gitrepo/                  # git binary via os/exec + layout walking; homelab.Repo
│   ├── truenas/                  # websocket JSON-RPC Client + Fake; homelab.TrueNAS
│   ├── sqlite/                   # modernc sqlite pool, goose migrations, sqlc-generated queries
│   └── server/                   # huma on http.ServeMux: routes, auth, job runner, embedded SPA
├── frontend/                     # SvelteKit SPA; a Go file here embeds the build output
├── fixtures/
│   ├── homelab-repo/             # fixture homelab repo, including deliberately broken stacks
│   └── truenas/                  # recorded middleware responses for the fake
├── deploy/
│   ├── compose.yaml              # the TrueNAS app definition for the orchestrator itself
│   └── bootstrap.sh              # first run: datasets, networks, git user, install app
├── Dockerfile                    # alpine + git + openssh-client; copies in the cross-compiled binary
└── docs/
```

Go conventions for everything below — naming, errors, testing, tooling — are in
[go-guidelines.md](go-guidelines.md). The decisions behind this layout are D15–D28 in
[decisions.md](../decisions.md).

## Package boundaries and why they sit there

The layout is Ben Johnson's "Standard Package Layout" (D15): the domain package owns the types and the
interfaces it consumes, and each dependency — git, TrueNAS, SQLite, HTTP — gets one package that
implements or uses them. These are the same six boundaries the design has always had; only the
enforcement mechanism changed.

### Dependency direction

```
cmd/orchestrator  ──> every internal package, plus frontend (it constructs and wires them)
internal/server   ──> homelab, compose, gitrepo, truenas, sqlite
internal/compose  ──> homelab
internal/gitrepo  ──> homelab
internal/truenas  ──> homelab
internal/sqlite   ──> homelab
internal/homelab  ──> nothing in this repo
```

Implementation packages do not import each other; `server` and `cmd/orchestrator` compose them. Only
`cmd/orchestrator` imports `frontend`: it passes the SPA's `fs.FS` into `server.Deps`, and
`internal/server` never imports `frontend`. The validation pipeline is `homelab.ValidateRepo`, called
by both `cmd/orchestrator validate` and `internal/server`, so it exists once. An import that points the
other way is a design error, not a convenience.

### `internal/homelab` — no I/O, ever

The layout contract, the env-scope model, the `setup.toml` schema, and the whole validation engine.
Takes already-read content in and returns findings out. Touches no filesystem, no network, no database.

This is the constraint that makes M1 testable: the entire validator can be exercised from in-memory
fixtures with plain unit tests, with no server, no database and no TrueNAS. Resist every temptation to
read a file from here. The boundary is enforced by a depguard rule (D26) that fails the build if this
package imports `os`, `io/fs`, `net`, `net/http`, `database/sql` or `os/exec`.

Also holds the interfaces the domain consumes — `TrueNAS`, `Repo`, `SecretStore` and `ComposeParser` —
declared here rather than beside their implementations. `SecretStore` is the secret-storage boundary
from D5, so an encrypted backend can replace plaintext files later without touching call sites. The
types that `compose` produces (variable references tagged with a scope and a source line) are defined
here too, so the checks consume them without `homelab` ever importing `compose`.

The validation pipeline — read through `Repo`, parse through `ComposeParser`, run every check — is one
function here, so the CLI and the UI cannot disagree:

```go
type ComposeParser interface {
	Parse(path string, src []byte) (ComposeFile, error)
}

func ValidateRepo(ctx context.Context, repo Repo, parse ComposeParser, opts Options) (Report, error)
```

`internal/compose` implements `ComposeParser`; `cmd/orchestrator validate` and `internal/server` both
call `ValidateRepo` and only format its `Report`.

### `internal/compose` — parse and render compose files, no I/O

Parses `compose.yaml` bytes into a structural model, extracts every `${VAR}` reference **tagged with the
scope of the position it appears in**, and renders deploy manifests and `compose.override.yaml` overlays
as bytes for the caller to write. Under the same depguard rule as `homelab`.

Two mechanisms, deliberately separate (D20). Positions come from our own walk over the `yaml.Node` tree
of `go.yaml.in/yaml/v3` (the maintained continuation of `yaml.v3`), which knows which key every scalar
sits under and its source line. Compose semantics come from `compose-spec/compose-go/v2`: its `template`
package parses the substitution syntax, and `schema.Validate` on the raw YAML map, before any
interpolation, decides `COMPOSE001`. compose-go's loader is never called from here — it rejects `${VAR}`
in typed fields such as ports and memory once interpolation is off, and it reads `env_file:` paths from
disk — and compose-go is never used for line numbers.

Must preserve source line numbers on every reference — findings point at lines. Must never rewrite
`compose.yaml` itself (D14): customization is emitted as a separate overlay file, and a test asserts the
original is byte-identical after any customization operation.

### `internal/gitrepo` — the only package that touches the repo on disk

Clone, fetch, pull, branch, commit, diff, by running the `git` binary through `os/exec` and parsing
porcelain v2 output (D21). Walks the layout and reads env and compose files, handing content to
`homelab`. Enforces that real env files stay gitignored, using `git ls-files` and `git check-ignore` so
`GIT001` has exactly git's answer. Implements `homelab.Repo`, and the plaintext-file `SecretStore` lives
here with the rest of the on-disk code.

### `internal/truenas` — an interface from day one

`*truenas.Client` speaks JSON-RPC 2.0 over a websocket to the TrueNAS middleware: `coder/websocket` for
the transport and a small hand-written RPC layer for request ids, pending calls, context cancellation and
job-progress event subscriptions (D22). `*truenas.Fake` is backed by recorded fixtures. Both satisfy
`homelab.TrueNAS`, and nothing else in the module depends on a concrete implementation, so the entire
system runs and tests locally with no TrueNAS (D12).

### `internal/sqlite` — sqlc and goose on a pure-Go driver

Pool, migrations, and query functions (D17). `modernc.org/sqlite` needs no cgo, so the linux binary
cross-compiles from macOS. Queries are SQL files compiled into typed Go by `sqlc`, with the generated
code checked in so CI builds without a database. Migrations are `goose` SQL files embedded with `embed`
and applied at startup. Tables: users, sessions, api_tokens, jobs, audit_log, schedules, app_provenance.

### `internal/server` — HTTP and auth only

huma v2 operations registered on a stdlib `http.ServeMux` (D19), argon2id auth with session cookies,
bearer tokens and a per-IP login limiter (D25), the async job runner with SSE progress, and serving the
built SPA from an `embed.FS` with an SPA fallback (D16). Contains no domain logic — it wires packages
together and speaks HTTP.

Two constructors: `NewServer(deps) http.Handler` for `serve`, and `NewAPI(mux, deps) huma.API`, which
registers every operation and is what `NewServer` builds on, so `orchestrator openapi` can reach the
document without starting a listener. Auth middleware is registered before any operation, because huma
binds middleware to an operation when the operation is registered. huma's default docs, `openapi` and
schemas routes are disabled; the spec is published only through `orchestrator openapi`.

huma generates the OpenAPI document from the operation types; `orchestrator openapi` dumps it, the
frontend's typed client is generated from it, and `just check-api` fails CI on drift, so route drift is
a build error rather than a runtime 404.

### `cmd/orchestrator` — the entry point

`main` does nothing but call `run(ctx, args, environ []string, stdout) error` with `os.Environ()` (D24)
and exit non-zero on error, so the whole binary is testable in-process with fake arguments and
environment. `run` dispatches on the subcommand first. Each subcommand has its own `flag.FlagSet`,
parses only the configuration it needs from `environ` with `caarlos0/env/v11` (`env.ToMap`), and
constructs only its own dependencies, so `validate` and `openapi` run without a database:

- `serve` — the HTTP server; what the TrueNAS app runs. The only subcommand that requires the database
  path.
- `validate <path>` — runs the validator against a homelab repo on disk, prints findings, and exits
  non-zero on any error-severity finding. No HTTP involved.
- `openapi` — writes the OpenAPI document to stdout, for client generation and `just check-api`. It
  builds the API with `server.NewAPI` and serializes it; nothing listens.

## Frontend

SvelteKit with `adapter-static` and `ssr = false`, TypeScript, Tailwind, Vite. Built assets are embedded
into the backend binary with stdlib `embed` (D16). `//go:embed` cannot reach parent directories, so the
directive sits in a small Go file inside `frontend/` that exposes the build output as an `fs.FS`;
`cmd/orchestrator` hands it to `internal/server` to serve.

The typed API client is generated from the OpenAPI document with `openapi-typescript` and called through
`openapi-fetch`; `just check-api` regenerates it and fails if the checked-in copy differs.

`vite dev` proxies `/api` to a configurable origin. Pointed at localhost it is the normal dev loop;
pointed at the server it is the "external client" feature from the README. Same artifact both ways —
that is the reason for choosing an SPA over SSR (D2).

Screens arrive by milestone:

| Milestone | Screens |
|---|---|
| [M2](../milestones/m2-validator-server.md) | Login; stack list with status badges; stack detail with findings (code, file, line, fix); resolved-env table showing both scopes separately; repo status |
| [M3](../milestones/m3-app-setup.md) | `.env` editor with per-key validation and masked values; setup wizard driven by `setup.toml` |
| [M4](../milestones/m4-truenas-client-bootstrap.md) | Bootstrap / environment-readiness view |
| [M5](../milestones/m5-deploy.md) | Deploy view: rendered manifest, deploy button, job log, and always a copy-able paste fallback |
| [M6](../milestones/m6-catalog.md) | Catalog browser, add-app flow, upstream-drift indicator |
| [M7](../milestones/m7-lifecycle.md) | Updates view, schedule configuration, deploy history with diff and rollback |

M0 ships only a shell that calls the health endpoint, and M1 has no UI: it is the engine and the
`validate` CLI.

## Cross-cutting rules

- **Secrets never reach a log, a job output, or the audit trail.** The audit records that a secret was
  written, never its value. `.env` values are masked in the UI unless explicitly revealed.
- **Every destructive operation has a dry-run preview and an explicit confirm.** The TrueNAS API key can
  damage storage (D9).
- **Findings carry stable codes.** See [../validator-checks.md](../validator-checks.md). Codes are part of
  the contract with the UI and with users; never renumber them.
- **`internal/homelab` and `internal/compose` import no I/O packages; depguard fails the build if they
  do.**
