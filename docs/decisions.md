# Decisions

Settled architectural decisions. Each entry records what was chosen, why, and what was rejected.
Do not relitigate these silently — if a decision needs revisiting, add a superseding entry rather than
editing the original.

Status: all entries `accepted` as of 2026-09-28 unless noted.

---

## D1 — Backend is Rust + axum, in a Cargo workspace

Single static binary, no runtime to install on TrueNAS, and strong typing for the compose/env model that
is the core of the product. Workspace rather than one crate so the validation logic stays I/O-free and
unit-testable without a server, a database, or a TrueNAS.

---

## D2 — Frontend is a SvelteKit SPA, embedded in the backend binary

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

## D10 — State lives in SQLite on a dedicated orchestrator dataset

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

## D12 — Development is fully local; no TrueNAS in the inner loop or in CI

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
