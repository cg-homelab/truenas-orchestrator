# Decisions

Settled architectural decisions. Each entry records what was chosen, why, and what was rejected.
Do not relitigate these silently — if a decision needs revisiting, add a superseding entry rather than
editing the original.

Status: D1–D14 `accepted` as of 2026-09-28; D15–D28 `accepted` as of 2026-10-30. D1 is superseded (by
D15), and D2, D10 and D12 are amended (by D16, D17 and D18); read the later entry alongside the
original. D32–D36 are accepted 2026-10-31 by the tech lead (M4 design, 2026-10-30; Martin to confirm), as are
D37 and D38; D34 amends D5 and D35 amends D22.

---

## D1 — Backend is Rust + axum, in a Cargo workspace *(superseded by D15)*

Single static binary, no runtime to install on TrueNAS, and strong typing for the compose/env model that
is the core of the product. Workspace rather than one crate so the validation logic stays I/O-free and
unit-testable without a server, a database, or a TrueNAS.

---

## D2 — Frontend is a SvelteKit SPA, embedded in the backend binary *(amended by D16)*

`adapter-static` with `ssr = false`. Built assets are embedded via `rust-embed`, so the TrueNAS app is one
container with no separate web service.

The deciding constraint is the "external client" requirement: the frontend must be runnable on a laptop
against the backend running on the server. With no SSR, the same build artifact works both ways — `vite
dev` just proxies `/api` at a configurable origin.

**Rejected:** Next.js (two containers, SSR complicates the remote-backend case); Rust full-stack
(Leptos/Dioxus — smaller component ecosystem for an admin UI).

---

## D3 — Deploy via the TrueNAS middleware API, with a paste-able manifest always rendered

Primary path is the middleware websocket JSON-RPC API (`app.create` / `app.update`) so deploy is one
button. But the rendered manifest is **always** shown alongside, so a middleware version change or an API
failure degrades to the manual TrueNAS Custom App flow instead of blocking.

**Rejected:** paste-only (not orchestration); direct docker socket (apps become invisible to the TrueNAS
Apps UI, which defeats the point of running on TrueNAS).

---

## D4 — The backend owns authentication: argon2id + session cookie

Its own user table, argon2id password hashing, `HttpOnly` session cookie, plus long-lived bearer tokens
for CLI use.

The deciding constraint is disaster recovery: the orchestrator is plausibly the tool you reach for when
Traefik or Authelia is broken. Depending on Authelia to log in to the thing that repairs Authelia is a
lockout waiting to happen.

**Rejected:** Traefik forward-auth to Authelia (lockout risk; `Remote-User` is spoofable unless the
backend is provably unreachable directly); a single shared static token (no revocation, no audit
identity, ends up in shell history).

Forward-auth may be added later as an *additional* front door. The local account stays.

---

## D5 — Secrets are plain files, gitignored — behind a trait

`.global.env` and `<app>/.env` are plaintext on disk and excluded from git. `example.global.env` and
`<app>/example.env` are the committed templates.

This matches how the homelab already works and keeps the first milestones simple. The user has flagged
this may change. Therefore: all secret reads and writes go through one trait in `homelab-model`, so an
encrypted store (age/SOPS, or an encrypted SQLite column) can replace the implementation without touching
call sites. Do not scatter direct file I/O for secrets.

**Consequence:** the backend must never log secret values, and `.env` values are masked in the UI unless
explicitly revealed.

---

## D6 — App setup is declarative `setup.toml`, never `setup.sh` execution

Apps in the homelab repo currently ship a `setup.sh` that mkdirs into `$APPCONFIG_STORAGE`, generates
secrets with `head -c 32 /dev/urandom`, and `docker exec`s into other running stacks to create database
users. Executing those from the backend would mean arbitrary code from a git repo running as root with
the docker socket.

Instead the backend interprets a declarative `setup.toml`. Every primitive (`dirs`, `secrets`,
`config_files`, `prompts`, `requires`, `provision.*`) is a named backend capability. Adding a new one is
a deliberate, reviewed code change. See [architecture/setup-toml.md](architecture/setup-toml.md).

**Consequence:** existing `setup.sh` files must be ported. The validator flags any app that still has one
(`SETUP001`).

**Rejected:** executing `setup.sh` directly; an escape hatch that runs it after confirmation (the
confirmation becomes reflexive and the security property is lost).

---

## D7 — The app catalog is a read-only source; apps are vendored into the homelab repo

`truenas-orchestrator-apps` is a catalog of templates. "Add app" copies `compose.yaml`, `example.env`,
`config/` and `setup.toml` into the homelab repo and records the source and version in SQLite. The user
then owns the copy and may edit it freely; the orchestrator reports drift from upstream but never
auto-merges.

**Rejected:** a git submodule or overlay (the homelab repo alone would no longer describe a deployable
system); automatic three-way merge on upstream updates (merge conflicts on files the user has edited, for
little benefit at homelab scale).

---

## D8 — Git writes use a branch per change; env edits never touch git

Adding or removing an app writes to a branch and surfaces a diff for the user to merge. Because `.env`
files are gitignored (D5), routine environment edits produce no git activity at all — which is what makes
branch-per-change tolerable rather than tedious.

---

## D9 — The orchestrator manages datasets, users, groups and permissions in full

Not read-and-verify. It creates datasets, sets ACLs, and creates the git user and groups.

**Consequence:** the TrueNAS API key it holds is powerful enough to damage storage. This raises the stakes
on D4 (auth) and on never executing untrusted scripts (D6). Every destructive operation needs a dry-run
preview and an explicit confirm, and every one is written to the audit log.

---

## D10 — State lives in SQLite on a dedicated orchestrator dataset *(amended by D17)*

Job history, sessions, audit log, schedules, and catalog provenance. `sqlx` with checked-in migrations and
offline query data so CI builds without a database.

The dataset is separate from the homelab repo, so state survives container replacement and is trivially
backed up, and no state file ever risks being committed.

**Rejected:** git as the only state (no job history, no audit trail, no sessions); Postgres reusing the
`db-postgres` stack (the orchestrator could not start until a stack it manages is healthy — circular
exactly when you need it most).

---

## D11 — Installable both by hand and by bootstrap script

`deploy/bootstrap.sh` does the whole first run (datasets, the four networks, git user, install the app),
and a hand-install guide covers the same ground step by step for anyone who wants to see what is
happening. Both must produce an identical end state, verified by the same environment-readiness check.

The orchestrator does not manage its own TrueNAS app entry — nothing that can break the tool that fixes
things.

---

## D12 — Development is fully local; no TrueNAS in the inner loop or in CI *(amended by D18)*

`just dev` runs axum plus vite against a fixture homelab repo and a fake middleware server. All tests run
in CI with no TrueNAS and no network. A `just smoke <host>` recipe makes read-only calls against a real
TrueNAS to detect middleware API drift, but it is never part of the default loop.

Milestone 1 touches no TrueNAS API at all, which is part of why it was chosen as the MVP.

---

## D13 — Milestone 1 is a read-only validator

No writes, no TrueNAS API, no deploy. It is immediately useful against the half-converted homelab repo,
it is testable entirely from fixtures, and it forces the env-scope model (the hard part) to be correct
before anything depends on it.

**Rejected:** starting with deploy (would need auth, git, env resolution and the TrueNAS client all
half-built at once); starting with the bootstrap wizard (produces nothing visible until the milestone
after it).

---

## D14 — Compose customization is overlay files, never in-place YAML mutation

Traefik exposure, resource limits and GPU options are applied as a generated `compose.override.yaml`, not
by rewriting `compose.yaml`.

The homelab's compose files carry load-bearing human comments (`# Comment out: if not public access`,
`# set shared memory limit when using docker-compose`). Round-tripping YAML through a serializer destroys
them. The original file must stay byte-identical, and this is asserted in tests.

---

## D15 — Backend is Go, a single module with `internal/` packages

**Supersedes D1.**

Go iterates faster than Rust for a one-person project, and everything D1 wanted from Rust still holds: one
static binary with no runtime to install on TrueNAS, and typing that is strong enough for the compose/env
model.

The layout follows Ben Johnson's "Standard Package Layout". `internal/homelab` owns the domain types, the
pure logic and the interfaces it consumes (`TrueNAS`, `Repo`, `SecretStore`, `ComposeParser`).
`internal/compose`,
`internal/gitrepo`, `internal/truenas`, `internal/sqlite` and `internal/server` implement or use them, and
`cmd/orchestrator` is the entry point. These are the same six boundaries as the Rust crates; see
[architecture/components.md](architecture/components.md). `internal/homelab` and `internal/compose` stay
I/O-free, enforced by a depguard lint rule (D26) where Rust enforced it with crate boundaries. The
validation pipeline is one function, `homelab.ValidateRepo`, which takes a `Repo` and a `ComposeParser`
(implemented by `internal/compose`), so the CLI and the server run exactly the same code.

**Consequence:** where D1–D14 name a crate or a trait, read the Go package and an interface:
`homelab-model` → `internal/homelab`, `compose-model` → `internal/compose`, `homelab-repo` →
`internal/gitrepo`, `truenas-client` → `internal/truenas`, `orchestrator-db` → `internal/sqlite`,
`orchestrator-server` → `internal/server`.

**Rejected:** hexagonal / ports-and-adapters directories (over-layered for Go, puts interfaces before
any implementation exists, and produces generic package names); flat feature packages (the env-scope model
is shared by validation, setup and deploy, and would end up duplicated or in a `common` package); a
multi-module `go.work` (overkill for one binary).

---

## D16 — The SPA is embedded via stdlib `embed`

**Amends D2.**

Everything in D2 holds: SvelteKit, `adapter-static`, `ssr = false`, and one build artifact for both the
dev loop and the external-client case. Only the embedding mechanism changes: the built assets are an
`embed.FS`, served with `http.FileServerFS` and an SPA fallback to `index.html` for unknown non-`/api`
paths.

The typed TypeScript client is generated from the OpenAPI document (D19) with `openapi-typescript` and
called through `openapi-fetch`.

---

## D17 — State in SQLite via `modernc.org/sqlite`, `sqlc` and `goose`

**Amends D10**, replacing its `sqlx` clause. D10's dataset separation and its rejection of git-as-state and of
Postgres stand unchanged.

`modernc.org/sqlite` is a pure-Go driver, so `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` produces
the linux binary straight from the darwin/arm64 dev host: no C toolchain, no buildx emulation. The Rust
M0 recorded an emulated image build that ran for about 50 minutes and never completed; this removes that
problem at the root. `sqlc` compiles checked-in SQL into typed Go at generation time — the equivalent of
sqlx offline mode — and `goose` runs migrations from an embedded FS at startup. CI needs no database.

**Rejected:** `mattn/go-sqlite3` (cgo; cross-compiling needs a zig or musl cross toolchain, or
emulation; its 1.5–2× speed advantage is irrelevant at homelab scale); ent or GORM (a large runtime layer
for seven small tables, generated code that is hard to read, and a move away from writing SQL).

---

## D18 — Dev loop is `go run` plus vite; images are built from a cross-compiled binary

**Amends D12.** D12's principle holds in full: no TrueNAS in the inner loop or in CI, and
`just smoke <host>` stays the only thing that talks to a real TrueNAS.

`just dev` runs `go run ./cmd/orchestrator serve` plus vite against the fixture homelab repo and the fake
middleware. `just build-image` cross-compiles a static binary on the host (D17) and copies it into an
`alpine` image with `git` and `openssh-client` installed (the git binary is required by D21). Nothing is
compiled inside the image, so an image build takes minutes rather than emulated hours.

---

## D19 — HTTP via huma v2 on the stdlib `http.ServeMux`; the OpenAPI contract is checked in CI

**New.**

Code-first: each operation is a Go function with typed input and output structs, registered through
huma's `humago` adapter on a plain `http.ServeMux`. huma generates the OpenAPI document, validates input,
and provides an SSE helper for job progress. `orchestrator openapi` dumps the document, and
`just check-api` regenerates the TypeScript client (D16) and fails on any drift — the same flow utoipa
provided, so route drift stays a build error rather than a runtime 404.

Routing stays stdlib, so there is no lock-in on routing or middleware, and handlers are plain functions
that are testable without HTTP.

**Consequence:** a framework dependency with tag-based validation. Accepted, because the alternatives
either lose the checked contract or slow every endpoint change.

**Rejected:** spec-first `oapi-codegen` (the strongest contract, but every endpoint change starts in
hand-written YAML, the slowest edit loop, and SSE and auth are hand-rolled); chi plus `swag` doc
comments (OpenAPI generated from comments that nothing verifies, so route drift becomes a runtime 404
again); gin or echo (they replace `http.Handler` with their own context type, cutting off stdlib
middleware).

---

## D20 — Compose parsing: compose-go for spec semantics, a `yaml.Node` walk for positions

**New.**

`github.com/compose-spec/compose-go/v2` is the library Docker Compose itself uses, and two of its
packages carry the semantics. `schema.Validate`, run on the raw YAML map before any interpolation, is
what `COMPOSE001` means by "structurally valid per the Compose spec": it accepts `${VAR}` in typed fields
(ports, memory, cpus, healthcheck intervals) and still rejects `networks: databases`. Its `template`
package is the real substitution grammar: `${VAR:-d}`, `${VAR:?e}`, `$VAR`, `$$`, nested defaults.
Nothing about Compose's semantics is reimplemented.

compose-go's loader is never called. With interpolation disabled it fails on `${VAR}` in typed fields
(port mappings, sizes, floats, durations), so every real stack would get a spurious `COMPOSE001`; with
default options it reads `env_file:` paths from disk, which `internal/compose` must never do.

Positions come from our own walk over the `yaml.Node` tree of `go.yaml.in/yaml/v3`, the YAML
organisation's maintained continuation of `yaml.v3` (same `yaml.Node` API; the original `gopkg.in`
repository is archived). The walk tags every reference with the scope of the position it appears in
(interpolation vs container, per [architecture/env-scopes.md](architecture/env-scopes.md)) and its
source line. compose-go itself is on v4 of the same lineage (`go.yaml.in/yaml/v4`, a release candidate);
we move to v4 when it is final. Parser identity does not matter: positions come from our walk, semantics
from `template` and `schema`.

**Consequence:** a large module with Docker transitive dependencies in its graph, even though only
`schema` and `template` are imported, and an API that can move between minor versions — pin it and read
the changelog on upgrade.

**Rejected:** goccy/go-yaml's AST plus a hand-written grammar and checks (reimplements Compose's
grammar; every divergence becomes a validator false positive, and `ENV003` is only as trustworthy as
that reimplementation); a YAML parser alone without compose-go (the same problem).
---

## D21 — Git operations shell out to the `git` binary, behind `homelab.Repo`

**New.**

`internal/gitrepo` runs `git` via `os/exec`, parsing porcelain v2 output, and uses
`git ls-files --error-unmatch` and `git check-ignore` for `GIT001`. The reason is exact git semantics:
`GIT001` and `GIT002` are findings about leaked secrets, and a wrong answer there is a false alarm the
user learns to ignore. SSH deploy keys work through normal ssh configuration.

**Consequence:** the container image is `alpine` plus `git` and `openssh-client` rather than
`FROM scratch` (D18), and `git` is required in CI (it is already present).

**Rejected:** go-git v5 (pure Go and `FROM scratch`, but `Status` is slow on large trees, its gitignore
handling has had correctness bugs, pull and SSH need manual handling, and every behavioural difference
from real git surfaces as a validator false positive); a hybrid of go-git for reads and the CLI for
writes (two implementations of one concern).

---

## D22 — TrueNAS transport is `coder/websocket` plus a hand-written JSON-RPC 2.0 layer

**New.**

TrueNAS deprecated its REST API in 25.04 and slated it for removal; JSON-RPC 2.0 over a websocket
(`/api/current`) is the supported API, so it is the only transport we build (D3).
`github.com/coder/websocket` is context-first, `net/http`-native, minimal and maintained. The RPC layer
on top is about 150 lines — request ids, a pending-call map, context cancellation, and event
subscriptions for job progress — and it is ours to test.

`homelab.TrueNAS` is satisfied by both `*truenas.Client` and `*truenas.Fake`, so nothing else depends on
a concrete implementation (D12).

**Rejected:** gorilla/websocket (no context on reads and writes, only deadlines; concurrent writers need
an external mutex); `golang.org/x/net/websocket` (the Go team recommends against it); heavier JSON-RPC
libraries such as creachadair/jrpc2 (more than the problem needs).

---

## D23 — Testing is stdlib `testing`, go-cmp and golden files; fakes, not mocks

**New.**

Table-driven tests; `cmp.Diff` from `github.com/google/go-cmp` for comparisons; validator snapshot tests
as golden files under `testdata/`, regenerated with an `-update` flag; hand-written fakes such as
`truenas.Fake`; `testing/synctest` for the scheduler. CI runs with `-race -shuffle=on`, and no test
touches the network.

This is the Google style guide's default, and golden files are exactly the "snapshot tests over
fixtures" that M1 requires.

**Rejected:** testify (discouraged by the Google guide, and its `mock` package pulls toward mocks where
this project wants fakes); gotest.tools/v3 (fine, but less widely recognised).

---

## D24 — CLI is stdlib `flag` with hand-rolled subcommands; configuration comes from the environment

**New.**

`orchestrator serve | validate <path> | openapi`, each with its own `flag.FlagSet`. `main` only calls
`run(ctx, args, environ []string, stdout) error` (Mat Ryer's pattern, with `os.Environ()` passed in), so
the whole binary is testable in-process. `run` dispatches on the subcommand first; each subcommand then
parses only the configuration it needs from that environment with `github.com/caarlos0/env/v11`, via
`env.ToMap(environ)`, and constructs only its own dependencies. `serve` requires the database path;
`validate` and `openapi` need no database. Env-var configuration is what a TrueNAS app definition
supplies anyway.

**Rejected:** cobra (its `init()`-driven global command tree fights the testable `run()`, it costs about
30 lines of boilerplate per verb for three verbs, and viper is a standing temptation); kong (a third
struct-tag DSL beside huma's and sqlc's).

---

## D25 — Auth building blocks: `alexedwards/argon2id`, own session and token tables, `x/time/rate`

**New.** D4 stands in full; this records how it is built.

`github.com/alexedwards/argon2id` for PHC-format password hashing. `sessions` and `api_tokens` are sqlc
tables in the same goose migration set as everything else (D17). A per-IP login limiter uses
`golang.org/x/time/rate`. About 200 lines, one schema.

**Consequence:** session expiry, rotation on login, and cookie flags (`HttpOnly`, `SameSite=Lax`,
`Secure`) are ours to get right. TLS terminates at Traefik, so the server cannot detect it: `Secure`
comes from serve config (`ORCH_SECURE_COOKIES`, default true, false only for plain-HTTP local dev).
They are specified in [architecture/go-guidelines.md](architecture/go-guidelines.md) and asserted in
tests.

**Rejected:** alexedwards/scs (a battle-tested session lifecycle, but it manages its own table outside
sqlc and goose, and bearer tokens would still need a hand-rolled path, so both would exist).

---

## D26 — Lint and format: strict golangci-lint v2 set, fail on any warning; depguard enforces the I/O-free domain

**New.**

Linters: errcheck, govet, staticcheck, revive, gocritic, errorlint, exhaustive, sloglint, unparam,
gosec, noctx, bodyclose, misspell, copyloopvar, depguard. Formatting with gofumpt and goimports.
`govulncheck` runs in CI. depguard forbids `os`, `io/fs`, `net`, `net/http`, `database/sql` and `os/exec`
inside `internal/homelab` and `internal/compose`.

This makes the architecture rule mechanical rather than a review comment: the I/O-free domain (D15) is a
build failure to violate. `exhaustive` protects the finding-code enum, and `sloglint` backs the
no-secrets-in-logs rule (D5).

**Rejected:** a minimal set of vet, staticcheck, errcheck and gofmt (near-zero friction, but the domain
rule stays a review comment).

---

## D27 — The style baseline is the Google Go Style Guide

**New.**

The Google Go Style Guide (Guide, Decisions, Best Practices) is the baseline. Effective Go and Go Code
Review Comments are required reading; the Uber Go Style Guide is supplementary. Project-specific rules
and the Rust-to-Go pitfalls live in [architecture/go-guidelines.md](architecture/go-guidelines.md),
which wins on conflict and must say why when it does.

**Rejected:** Uber as the baseline (shorter and concrete, but Uber-specific in places — zap, their error
packages — and it predates `log/slog` and range-over-func); Effective Go plus project rules only (silent
on modules, generics, `slog` and layout).

---

## D28 — Milestones re-cut: old M1 splits into M1 and M2; old M2–M6 become M3–M7

**New.**

Old M1 was by far the largest milestone — every check, the compose parser, git, the routes, full auth,
the CLI and five screens — with nothing shippable in the middle. It is split:

- **M1** — validator engine and `orchestrator validate` CLI, no HTTP. Pure Go, fixture-driven, useful
  against `martin-homelab` the day it lands, and it forces the env-scope model to be right before any UI
  exists.
- **M2** — server, auth and the validator UI.

Old M2–M6 shift to M3–M7 with their content unchanged. No GitHub issues exist yet, so renumbering is
free. M0 is kept, so the guidelines, lint and CI are proven on an empty skeleton before real code lands.
See [roadmap.md](roadmap.md).

**Consequence:** milestone numbers in D1–D14 use the old numbering. D13's "Milestone 1" validator is now
M1 plus M2 — the read-only, no-TrueNAS properties hold for both — and D12's remark that Milestone 1
touches no TrueNAS API applies to both as well.

**Rejected:** keeping M0–M6 and only swapping the technology (leaves the oversized milestone in place);
merging M0 into M1 (the first real code would land without guardrails).

---

## D29 — Compose substitution grammar is scanned by our own code, differentially tested against compose-go

**Amends D20.** `schema.Validate` still decides `COMPOSE001`. compose-go's `template` package is no
longer called at runtime: `internal/homelab/interpolate.go` (`ScanTemplate`, `Interpolate`) implements
the grammar — `$VAR`, `${VAR}`, `${VAR:-d}`, `${VAR-d}`, `${VAR:+a}`, `${VAR+a}`, `${VAR:?e}`, `${VAR?e}`,
nesting in the operand, and `$$` as a literal `$` — mirroring compose-go's pattern and brace matching.

Why: the validator needs every reference with its exact form and line, and must not write to a global
logger. `template.ExtractVariables` merges occurrences per name (and cannot tell `${A:-}` from `${A}`),
so per-reference line tagging is impossible through it. `template.SubstituteWithOptions(...,
template.WithoutLogging)` still logs through the global logrus logger for an unset variable nested in a
default (`${A:-${B}}`) or following an operator in the same string (`${A:-x} ${B}`): those parts go
through the plain `Substitute`, which ignores the options. Verified against compose-go v2.16.2.

**Consequence:** every grammar case in `internal/homelab/interpolate_test.go` is also run through
compose-go's `template` package (`TestGrammarMatchesComposeGo`): the names found must equal
`ExtractVariables`', a template compose-go rejects must yield no reference, and `Interpolate` must equal
`SubstituteWithOptions` with every name set non-empty and with every name set empty. The cases include
`${A}`, `${A:-d}`, `${A-d}`, `${A:?e}`, `${A?e}`, `${A:+x}`, `${A+x}`, `$A`, `$$` and `${A:-${B}}`, so a
divergence after a compose-go upgrade fails CI. compose-go stays a dependency for `schema` and as the
test oracle.

**Rejected:** calling `template` at runtime and discarding its logger (silencing logrus means mutating
its global state, which the guidelines forbid in library code, and still leaves `ExtractVariables`
unable to tag lines).

---

## D30 — Host filesystem writes go through internal/hostfs behind homelab.SetupHost

A setup run writes directories, secret files and config files under `$APPCONFIG_STORAGE` and
`$DATABASE_STORAGE`. Those writes live in `internal/hostfs`, an `os.Root` adapter that implements
`homelab.SetupHost` (stat, read, mkdir, exclusive create, atomic write), declared in `internal/homelab`
where the planner and executor consume it. `*hostfs.Host` is wired in by `cmd/orchestrator` and passed
through `server.Deps.Host`.

Why: `homelab` must stay I/O-free (D26), so the executor needs an interface, and a fake of it makes the
whole plan and apply pipeline testable in memory, including the failure cases that are hard to
produce on a real disk. A package of its own keeps the one place that writes outside the repo small and
easy to audit, and keeps `os.Root` confinement as the only way to reach a storage root.

**Rejected:** writing from `gitrepo` (it is the only package that touches the repo, and storage roots are a
different trust boundary; mixing them would let a bug in repo code write to appdata and the reverse);
writing from `server` (domain logic and I/O in the HTTP layer, untestable without a server); a generic fs
interface (it would be wide enough to write anywhere, where `SetupHost` takes a root and a relative path
and nothing else).

---

## D31 — setup.toml paths resolve only from orchestrator-injected and .global.env variables

`${...}` in `setup.toml` (`path`, `file`, `to`, `from`) may reference only a variable the orchestrator
injects (`HOMELAB_STORAGE`) or one defined in `compose/.global.env`. A variable that the deploy env
takes from `compose/<app>/.env` is a plan problem, and nothing is written.

Why: resolved paths are shown in plans, job output and the audit log. A value from `<app>/.env` may be a
secret, so allowing it into a path would put a secret into all three. The rule also removes a
stale-env problem: the plan's deploy env is the one from before the run, so a key that the run itself sets
in `.env` would resolve to its old or missing value in a path. Variables from `.global.env` and the
injected ones do not change during a run.

**Rejected:** allowing any deploy-env variable and redacting after resolution (redaction cannot know which
fragment of a path is secret, and the stale-value problem remains); resolving against the post-run env (the
plan would no longer be computable before writing, which breaks the dry run).

---

## D32 — Bootstrap end state: one homelab identity, POSIX-mode storage roots *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**New.** Defines what `deploy/bootstrap.sh`, the hand-install guide and the orchestrator's own plan all
converge on, and what the environment-readiness check accepts (D11).

- **Identity.** One group `homelab` with gid `PGID` and one user `homelab` with uid `PUID`, both from
  `compose/.global.env` (1000/1000 in the fixture). The user has `homelab` as its primary group,
  `password_disabled`, `smb: false`, shell `/usr/sbin/nologin`, home `/var/empty`. A pre-existing group or
  user that already holds the gid or uid, under any name, is accepted as-is; a `homelab` name that holds a
  different id is a blocking problem. TrueNAS keeps 1000–2999 free for this (`MIN_AUTO_XID = 3000`, "to
  avoid collision with apps uids/gids"), and its builtin `apps` user (568) is not used.
- **The git user is the same identity.** The orchestrator container runs as `PUID:PGID`, owns
  `$HOMELAB_STORAGE` and fetches with the deploy key. There is no separate git user.
- **Datasets.** One dataset per writable storage root: `$HOMELAB_STORAGE`, `$APPCONFIG_STORAGE`,
  `$DATABASE_STORAGE`, plus the orchestrator state dataset `$ORCH_STATE_STORAGE` (D10). Created with
  `share_type: GENERIC`, `acltype: POSIX`, `aclmode: DISCARD`, `atime: OFF`, `compression: LZ4` and
  `recordsize: 128K`, except `$DATABASE_STORAGE` at `16K`. Missing parents are created as plain
  datasets that inherit everything. A dataset's name comes from the deepest existing dataset whose
  mountpoint contains the path, not from string surgery on `/mnt/`. An existing root keeps its
  properties: a property that differs from the list above is reported, never changed. The state dataset
  is created only by bootstrap and the guide. The orchestrator checks it and never plans a change to it.
  `$MEDIA_STORAGE` holds user data and is only checked for existence.
- **Permissions.** Each writable root directory is owned by `PUID:PGID` with mode `0750` (state:
  `0700`) and no extended ACL. This is set with `filesystem.setperm` (`stripacl: true`,
  `recursive: false`), which touches only the root directory, never its contents.
- **Networks.** `home`, `proxy`, `isolated` (`internal: true`) and `databases`, created by D33.
- **Deploy key.** An ed25519 key at `$ORCH_STATE_STORAGE/ssh/id_ed25519` (mode `0600`, `PUID:PGID`)
  beside `id_ed25519.pub` (`0644`) and `known_hosts`. The private key is never shown (D34).

Why: M3 writes secret files at `0600` with no group bits. Those files are readable by the containers
only if the orchestrator writes them as the same uid the containers run as, and git refuses a checkout
owned by another uid ("dubious ownership"). One identity satisfies both. A dedicated git user would
break one or the other. POSIX mode bits rather than NFSv4 ACLs: database images `chmod` their data
directories, and that fails under NFSv4 `aclmode=restricted`. `ls -l` also shows the whole permission
model. Datasets per root give separate snapshot, quota and recordsize policy.

**Rejected:** a dedicated git user owning `$HOMELAB_STORAGE` (M3's `0600` secrets become unreadable to
the containers, or the orchestrator needs root and a chown after every write); `share_type: APPS`
(NFSv4 plus a grant to uid 568, but our containers run as `PUID`); recursive chown of existing roots
(destructive on a server that already holds data); creating or chowning `$MEDIA_STORAGE` (user data,
often shared over SMB with its own ACLs).

---

## D33 — The four docker networks come from a `homelab-networks` custom app *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**New.** The TrueNAS middleware has no docker network create method. In 25.10, 27 and 28 the
`docker.network` namespace holds only `query` and `get_instance`, and its entries carry no `internal`
flag (`api/v25_10_0/docker_network.py` on `stable/goldeye`, `v27_0_0` on `stable/27`, `v28_0_0` and
`plugins/docker/docker_network_crud.py` on `master`).

The networks are therefore declared by a dedicated custom app, created with `app.create`
(`custom_app: true`, `custom_compose_config_string`). Its compose file declares `home`, `proxy`,
`isolated` (`internal: true`) and `databases` with explicit `name:`, so no `ix-` prefix applies, plus one
placeholder service that attaches to all four. The placeholder is `busybox` running `sleep infinity`, read-only, non-root, all
capabilities dropped. The compose text is one Go constant. `deploy/homelab-networks.yaml` is a
byte-identical copy that bootstrap and the guide use, and a test asserts the two match.

Readiness accepts a network that exists by name, however it was created, so a server whose networks
were made by hand with `docker network create` is ready without the app. The app is planned only when
all four are missing, or when every one that exists already belongs to compose project
`ix-homelab-networks`. A mix of hand-made and missing networks is a blocking problem that names the
`docker network create` commands to run, because compose cannot adopt an existing network under an
explicit `name:`.

Why: it keeps the orchestrator on the middleware API (D3: no docker socket) while letting it create the
networks itself (D9). It gives the first-run wizard a complete path with no shell step. The networks
also become visible and named in the TrueNAS Apps UI, and their definition, including `internal`, is
reviewable text rather than a remembered command.

**Consequence:** stopping or deleting the app in the TrueNAS UI runs `compose down`, which removes any
of the four networks no running container is using (`plugins/apps/app_scale.py` `stop`). Readiness
reports a missing network or a stopped app, and the app's compose file says not to stop it.

**Rejected:** `docker network create` from `bootstrap.sh` only (the orchestrator could not create or
repair networks, and the wizard would need a shell step; it stays the documented manual fallback);
declaring the networks in the orchestrator's own app (D11: never manage its own entry, and reinstalling
the orchestrator would try to remove networks every stack depends on); the docker socket (D3).

---

## D34 — The TrueNAS API key lives in SQLite behind a server-declared store; the deploy key is a file *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**Amends D5** for secrets that are not `.env` files.

- **API key.** A new `credentials` table (`name` primary key, `username`, `secret`, `updated_at`), with
  one row `name = 'truenas'`. `internal/server` declares the interface it consumes:
  `CredentialStore` with get, put and delete of `homelab.TrueNASCredentials{Username, APIKey
  homelab.Secret}`. `*sqlite.Store` implements it. The key is a `homelab.Secret` from the row scan
  onwards, and `Reveal()` happens only inside `truenas.Dial`, in the `auth.login_ex` payload. The API
  reports only whether a key is configured and the username, never the key or any prefix of it. The audit
  log records `truenas.credentials.write` and `truenas.credentials.delete` with the username only. A key is stored only
  after a successful login and version check.
- **Deploy key.** Generated in process (ed25519, OpenSSH format via `golang.org/x/crypto/ssh`) when
  absent, and written through `homelab.SetupHost` under `$ORCH_STATE_STORAGE` (`ssh/` at `0700`, private
  key `0600` by exclusive create, public key `0644`). It is never overwritten. `ssh` reads it from disk
  for `git fetch`, so it must be a file. The public half and its SHA256 fingerprint are shown in the UI.
  The private half never leaves the file.

Why: `homelab.SecretStore` is shaped around whole `.env` files per app (comments and key order survive).
An API key is a single credential with a username, so forcing it into that shape would mean a fake env
file. SQLite is already the orchestrator's state on its own dataset (D10, D17), and a small interface
keeps the D5 promise that an encrypted backend can replace the plaintext one without touching call
sites. The interface is declared by its consumer, `server`, per go-guidelines section 3.3.

**Consequence:** the key is plaintext in `orchestrator.db` (mode `0600`, on the state dataset), and every
backup of that dataset contains it. This is the same posture D5 accepts for `.env`.

**Rejected:** an env-file-shaped entry in `SecretStore` (a fake file and a fake app name); an
environment variable in the app definition (visible to anyone who can read the TrueNAS app
configuration, and changing it means redeploying the orchestrator's own app, which D11 forbids); storing
the deploy key in SQLite (`ssh` needs a file, so it would be written out anyway).

---

## D35 — The TrueNAS client pins API `v25.10.0` over `wss://` through the UI port *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**Amends D22**, which named `/api/current`. D22's transport and library choices stand.

- **Endpoint.** The client connects to `wss://<host>/api/v25.10.0`. The middleware registers one route
  per API version it ships (`main.py` `_add_api_route`) and adapts the calls on each one to that
  version's schema. `v25.10.0` is served by every 25.10.x release, by 27 (`stable/27`) and by 28
  (`master`), so the request and result shapes we decode stay fixed across that range.
  `/api/current` changes shape with every release.
- **TLS is mandatory.** A plain-API-key login over a connection the middleware does not consider secure
  revokes the key (`plugins/auth.py`: "Attempt to use over an insecure transport"). A connection counts
  as secure only if it is TLS, loopback or AF_UNIX (`utils/origin.py` `secure_transport`). The
  orchestrator container is neither loopback nor AF_UNIX, so it goes through nginx on the HTTPS UI port.
  `ws://` is refused by configuration except for a loopback host.
- **Configuration.** `ORCH_TRUENAS_URL` (`wss://host[:port]`, or `fake[:scenario]` for development),
  `ORCH_TRUENAS_CA_FILE` (PEM roots, e.g. the UI certificate exported by bootstrap),
  `ORCH_TRUENAS_TLS_SERVER_NAME` (`localhost` for TrueNAS's default self-signed certificate, whose only
  SAN is `localhost`), and `ORCH_TRUENAS_INSECURE_TLS` (default false; logs a warning on every connect).
  The deployed app reaches the host as `host.docker.internal` through
  `extra_hosts: host.docker.internal:host-gateway`.
- **Session.** One connection per operation: dial, `auth.login_ex` (`API_KEY_PLAIN`), `system.version`
  and the D36 range check, then `core.set_options {"legacy_jobs": false}` so that a job method's reply is
  its final result. Domain calls follow, and the connection closes. The client pings every 20 s, because
  nginx proxies `/api` with its default 60 s `proxy_read_timeout`. The read limit is raised to 16 MiB.

Why: a pinned version is the middleware's own compatibility contract. Combined with D36 and
`just smoke`, a TrueNAS upgrade becomes a range decision rather than a silent shape change.

**Rejected:** `/api/current` (the shape follows the server); mounting `/var/run/middleware/middlewared.sock`
into the container (an AF_UNIX connection from uid 0 is authenticated as root with no key, so the
container's root becomes full TrueNAS admin and the API key, its audit identity and revocation stop
meaning anything); `ws://` on port 80 (revokes the key).

---

## D36 — Supported TrueNAS versions: 25.10 up to, not including, 28 *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**New.** `homelab.SupportedTrueNAS` accepts a version when its major.minor is at least 25.10 and its major is
below 28. That covers 25.10.x and 27.x, including release candidates such as `27.0.0-RC.1` (the release
first developed as 26 was renamed 27 at RC.1; GA is planned for December 2026). The check runs on the
`system.version` string (`TrueNAS-25.10.1`; 24.10 and earlier said `TrueNAS-SCALE-24.10.2`) immediately
after login and before any domain call. Out of range is a `*homelab.UnsupportedVersionError` that
wraps `ErrValidation` and names both versions: `TrueNAS 25.04.2.6 is not supported: this orchestrator
supports TrueNAS 25.10 through 27.x`. If the server does not serve `/api/v25.10.0` at all, the client
reads the unauthenticated `GET /api/versions` and reports the API versions it does serve, with the same
wording.

Why: 25.10 is the floor for `legacy_jobs` and for the `v25.10.0` endpoint (D35). 28 is an unreleased
preview whose `v28` API already drops `auth.login_with_api_key`, so the ceiling is the newest release we
can smoke. Raising the ceiling is deliberate: run `just smoke` against the new release, update the
constant, and add a note to this entry.

**Rejected:** supporting 25.04 (no `v25.10.0` endpoint, no `legacy_jobs` option, so job polling over
`core.get_jobs` would be needed for one release line); no ceiling (a future schema change would surface
as a decode error, which acceptance criterion 7 forbids).

---

## D37 — M4 limitation: the orchestrator app runs as PUID 1000 *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**New.** The M4 image creates one user (uid 1000), and ssh refuses to run for a uid without a passwd
entry, so the orchestrator app must run as `PUID` 1000. Bootstrap step B0 blocks any other value. The
readiness `identity` item (`CheckIdentity`, process uid/gid against `PUID`/`PGID`) reports a mismatch; it
is enabled only for a real TrueNAS URL, so the fake and dev runs skip it.

Why: git over ssh with the deploy key is the only way the orchestrator reaches the homelab repo, and it
fails at runtime with an unhelpful message when the uid has no passwd entry. A blocked bootstrap is a
better place to say so than a failed fetch.

**Rejected:** an entrypoint or nss_wrapper that synthesises a passwd entry for any uid (the right
follow-up, not needed yet); running the app as root (breaks the single-identity model of D32).

---

## D38 — Legacy-job fallback polls `core.get_jobs` *(accepted 2026-10-31 by the tech lead; Martin to confirm)*

**New.** When a job method replies with a bare number (the legacy-job reply), the client polls it through
`core.get_jobs` (select `id`, `state`, `error`, `exc_info`) until it reaches a terminal state.
`core.job_wait` cannot be used: in legacy mode it is itself a job, so waiting on it needs the very wait it
would provide.

Why: the docs and the middleware source disagree about `legacy_jobs: false` (design section 13, item 3), so
the client handles both reply shapes and the smoke `jobs` check settles which one the server uses.

**Rejected:** `core.job_wait` (recursion described above); failing on a bare-number reply (it would make
the client depend on an unverified server option).
