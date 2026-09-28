# Components

## Repository layout

```
truenas-orchestrator/
├── justfile
├── Cargo.toml                    # workspace
├── rust-toolchain.toml
├── crates/
│   ├── homelab-model/            # pure domain: layout contract, env scopes, validation. No I/O
│   ├── compose-model/            # parse compose.yaml, extract ${VAR} per scope, render manifests
│   ├── homelab-repo/             # git operations + on-disk layout walking
│   ├── truenas-client/           # TrueNasApi trait + websocket JSON-RPC impl + FakeTrueNas
│   ├── orchestrator-db/          # sqlx sqlite pool + migrations
│   └── orchestrator-server/      # axum: routes, auth, job runner, rust-embed of the SPA
├── frontend/                     # SvelteKit SPA
├── fixtures/
│   ├── homelab-repo/             # fixture homelab repo, including deliberately broken stacks
│   └── truenas/                  # recorded middleware responses for the fake
├── deploy/
│   ├── compose.yaml              # the TrueNAS app definition for the orchestrator itself
│   └── bootstrap.sh              # first run: datasets, networks, git user, install app
└── docs/
```

## Crate boundaries and why they sit there

### `homelab-model` — no I/O, ever

The layout contract, the env-scope model, the `setup.toml` schema, and the whole validation engine.
Takes already-read content in and returns findings out. Touches no filesystem, no network, no database.

This is the constraint that makes M1 testable: the entire validator can be exercised from in-memory
fixtures with plain unit tests, with no server, no database and no TrueNAS. Resist every temptation to
read a file from here.

Also holds the secret storage **trait** (see [decisions.md](../decisions.md) D5), so an encrypted backend
can replace plaintext files later without touching call sites.

### `compose-model` — parse and render compose files

Parses `compose.yaml` into a structural model, extracts every `${VAR}` reference **tagged with the scope
of the position it appears in**, and renders deploy manifests and `compose.override.yaml` overlays.

Must preserve source line numbers on every reference — findings point at lines. Must never rewrite
`compose.yaml` itself (D14): customization is emitted as a separate overlay file, and a test asserts the
original is byte-identical after any customization operation.

### `homelab-repo` — the only crate that touches the repo on disk

Clone, fetch, pull, branch, commit, diff. Walks the layout and reads env and compose files, handing
content to `homelab-model`. Enforces that real env files stay gitignored.

### `truenas-client` — a trait from day one

`TrueNasApi` trait, a websocket JSON-RPC implementation against the TrueNAS middleware, and `FakeTrueNas`
backed by recorded fixtures. Nothing else in the workspace depends on a concrete implementation, so the
entire system runs and tests locally with no TrueNAS (D12).

### `orchestrator-db` — sqlx + sqlite

Pool, migrations, and query functions. Offline query data is checked in so CI builds without a database.
Tables: users, sessions, api_tokens, jobs, audit_log, schedules, app_provenance.

### `orchestrator-server` — HTTP and auth only

axum routes, argon2id auth and sessions, the async job runner with SSE progress, and `rust-embed` of the
built SPA. Contains no domain logic — it wires crates together and speaks HTTP.

OpenAPI via `utoipa`, from which the frontend's typed client is generated, so route drift is a build
error rather than a runtime 404.

## Frontend

SvelteKit with `adapter-static` and `ssr = false`, TypeScript, Tailwind, Vite. Built assets are embedded
into the backend binary.

`vite dev` proxies `/api` to a configurable origin. Pointed at localhost it is the normal dev loop;
pointed at the server it is the "external client" feature from the README. Same artifact both ways —
that is the reason for choosing an SPA over SSR (D2).

Screens arrive by milestone:

| Milestone | Screens |
|---|---|
| M1 | Stack list with status badges; stack detail with findings (code, file, line, fix); resolved-env table showing both scopes separately; repo status |
| M2 | `.env` editor with per-key validation and masked values; setup wizard driven by `setup.toml` |
| M3 | Bootstrap / environment-readiness view |
| M4 | Deploy view: rendered manifest, deploy button, job log, and always a copy-able paste fallback |
| M5 | Catalog browser, add-app flow, upstream-drift indicator |

## Cross-cutting rules

- **Secrets never reach a log, a job output, or the audit trail.** The audit records that a secret was
  written, never its value. `.env` values are masked in the UI unless explicitly revealed.
- **Every destructive operation has a dry-run preview and an explicit confirm.** The TrueNAS API key can
  damage storage (D9).
- **Findings carry stable codes.** See [../validator-checks.md](../validator-checks.md). Codes are part of
  the contract with the UI and with users; never renumber them.
