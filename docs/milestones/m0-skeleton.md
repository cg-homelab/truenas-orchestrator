# M0 — Skeleton

**Status:** in progress · **Depends on:** — · **Issue:** _(unset)_

## Goal

The project builds, tests, lints and runs end to end with nothing in it. `just dev` starts the backend and
the frontend; the SPA loads in a browser and successfully calls a health endpoint on the backend. CI runs
the same checks. A fixture homelab repo exists to develop the validator against.

## Out of scope

- Any validation logic (M1)
- Auth (M1)
- Any database schema beyond the migration harness itself (M1)
- Any TrueNAS API call (M3)

## Tasks

- [x] Cargo workspace with the six crates from [architecture/components.md](../architecture/components.md), each compiling as a stub
- [x] `rust-toolchain.toml` pinning the toolchain
- [x] `justfile` with the full recipe list (see the Verification section); recipes that belong to later milestones may print "not yet implemented"
- [x] `just setup` installs cargo tools and frontend deps from a clean checkout
- [x] SvelteKit app with `adapter-static`, `ssr = false`, TypeScript, Tailwind, Vite
- [x] `vite dev` proxies `/api` to `$ORCHESTRATOR_API`, defaulting to localhost
- [x] axum server with `GET /api/health` returning version and build info
- [x] `rust-embed` serves the built SPA from the release binary, with SPA fallback routing
- [x] `utoipa` wired up; OpenAPI served at `/api/openapi.json`; frontend client generated from it
- [x] `orchestrator-db`: sqlx sqlite pool, migration harness, one trivial migration, offline query data checked in
- [x] `fixtures/homelab-repo/` — copy `martin-homelab/compose/` **including every defect** listed in [validator-checks.md](../validator-checks.md), plus at least one fully correct stack as a control
- [x] `fixtures/homelab-repo/` has a real `.git` so repo operations can be tested (committed as a bundle or created by a fixture script)
- [x] `truenas-client`: `TrueNasApi` trait definition and a `FakeTrueNas` stub. No real implementation
- [~] Dockerfile using cargo-chef; `just build-image` cross-compiles to `linux/amd64` via buildx — **written, not yet verified to completion** (see below)
- [x] CI workflow running `just check && just test && just build-image`
- [x] `.gitignore` extended for `node_modules`, frontend build output, `*.db`, `.env.dev`

## Acceptance criteria

1. From a clean clone, `just setup && just dev` works with no manual steps beyond installing `just`, Rust and Node.
2. The SPA loads in a browser and displays data fetched from `GET /api/health`.
3. `just build` produces a single binary that serves the SPA with no `frontend/` directory present at runtime.
4. `just build-image` produces a `linux/amd64` image from the darwin/arm64 dev host. — **NOT VERIFIED**, deliberately deferred (see notes).
5. `just check` fails on any clippy warning, any unformatted file, and any `svelte-check` error.
6. CI passes with no network access to TrueNAS and no database present at build time.
7. `fixtures/homelab-repo/` contains at least one instance of every defect in the Context table, so M1 has something to detect.

## Verification

```
just setup
just check
just test
just dev          # open the browser, confirm health data renders
just build        # then run the binary alone and confirm the SPA still serves
just build-image
```

Confirm the fixture repo: every defect code in `docs/validator-checks.md` that is marked with a real
example has a corresponding instance under `fixtures/homelab-repo/`.

## Notes / decisions made during implementation

### Toolchain versions actually resolved

Rust 1.96, edition 2024, resolver 3. axum 0.8, tokio 1.53, sqlx 0.9, utoipa 6, rust-embed 8, gix 0.88,
thiserror 2. Frontend: SvelteKit 2 / Svelte 5 (runes), Vite 8, Tailwind 4, vitest 5, bun as the package
manager (matching the other repos in this org).

### Things that bit, so they do not bite twice

- **`gix` needs explicit features.** `--no-default-features` makes `gix-hash` fail to compile with a
  non-exhaustive match. Use `features = ["max-performance-safe", "blocking-network-client"]`.
- **TypeScript must be pinned to 5.x.** TypeScript 7 (the native port) has no `ts.factory`, which
  `openapi-typescript` calls, so `just gen-api` dies. Pinned to `^5.9`. Revisit when
  `openapi-typescript` supports TS 7.
- **`defineConfig` must come from `vitest/config`, not `vite`,** or `svelte-check` rejects the `test`
  key in `vite.config.ts`.
- **`oven/bun` is not an official image,** so it is `docker.io/oven/bun`, not `docker.io/library/oven/bun`.
- **`clap` needs the `env` feature** for `#[arg(env = ...)]`.
- **A `.gitignore` inside `fixtures/` applies to this repository too.** The fixture repo's ignore
  rules (`compose/.global.env`, `compose/*/.env`) matched the fixture's own env files, so they would
  never have been committed and a fresh clone would have gotten a fixture silently missing its env
  files — changing what the validator finds with nothing failing. The file is therefore stored
  undotted as `fixtures/homelab-repo/gitignore`, and `scripts/init-fixture-repo.sh` restores the dot
  when it materialises the repo. Caught by `git check-ignore`; worth re-running that check if the
  fixture layout changes.

### Decisions taken during M0

- **Migrations are embedded and applied on connect** (`sqlx::migrate!`), not run by `sqlx-cli`. A
  deployed container migrates itself and `sqlx-cli` is not a runtime dependency. `just db-migrate`
  therefore just exercises the migration path; `just sqlx-prepare` stays, but is only needed once
  `query!` macros exist in M1.
- **`build.rs` declares `frontend/build` as a build input.** Without it cargo does not know the
  embedded SPA changed and would ship stale assets.
- **The SPA fallback deliberately excludes `/api`.** An unmatched `/api/...` path must 404 as an API
  call; returning `index.html` there turns a missing route into a confusing HTML body.
- **`just check-api` guards route drift.** It regenerates `openapi.json` and the typed client and fails
  if either differs from what is committed, so the frontend client cannot silently fall behind the
  backend's routes. Wired into CI.
- **The fixture repo is committed as plain files with no nested `.git`.** `scripts/init-fixture-repo.sh`
  materialises it as a real repository in a temp dir when a test needs git state. It also force-adds
  `compose/env-drift/.env` so `GIT001` has something to detect.
- **CORS is off unless `--cors-origin` is given.** Cross-origin is only needed for the external-client
  workflow; it should not be on by default.

### Verified by hand

- `just check` and `just test` pass clean (clippy `-D warnings`, `svelte-check` 0 errors, prettier clean).
- `just check-api` exits 0 against the committed client.
- The release binary was copied to an empty directory with no `frontend/` present and correctly served
  `index.html`, hashed `_app` assets, client-side routes, `/api/health`, and 404 for `/api/nope`.
  Binary size 2.7 MB.
- `scripts/init-fixture-repo.sh` produces a repo where `compose/env-drift/.env` is tracked and
  `.global.env` and the other `.env` files are correctly ignored.

### `just build-image` is UNVERIFIED — deliberately deferred

**The Dockerfile has never been built to completion. Do not assume it works.**

It compiles Rust for `linux/amd64` under QEMU emulation on an arm64 host. An attempt ran for roughly
50 minutes at ~650% CPU and was still compiling when it was killed on purpose — it was progressing,
not wedged. The cost is the dependency tree: `gix` alone pulls in about 60 subcrates, on top of sqlx,
axum and tokio, all of it emulated.

It was killed because nothing needs a container image until M3/M4, when something is actually
deployed, and it was monopolising the developer's machine for a deliverable no milestone yet depends
on.

**What is known:** the image path `docker.io/oven/bun` was wrong as `docker.io/library/oven/bun` and
is fixed; the build gets past image resolution and well into the `cargo chef cook --release` layer.
Nothing beyond that layer — the final `cargo build`, the frontend `COPY`, the runtime stage, the
unprivileged user, the entrypoint — has ever executed.

**Before relying on it (M3 at the latest):**

1. Run it once to completion and confirm the image starts and serves `/api/health`.
2. Confirm the frontend really was embedded, by requesting `/` and checking it is not the
   "frontend not built" placeholder that `build.rs` writes.
3. Decide whether to keep the emulated path. The alternative is `cargo-zigbuild` (or `cross`) on the
   host targeting `x86_64-unknown-linux-gnu`, with the finished binary `COPY`d into a slim runtime
   image — minutes instead of tens of minutes, at the cost of another tool in `just setup` and local
   and CI build paths no longer being identical.

CI builds this same Dockerfile natively on amd64 and pays none of the emulation cost, so the first
real end-to-end verification will most likely come from CI rather than from a laptop.

### Known gaps handed to M1

- `homelab-model` and `compose-model` are stubs: `Finding`, `Severity`, `StackStatus` and `Scope` types
  only. No validation logic.
- **The YAML parser is not chosen yet.** It must preserve line numbers, since every finding anchors to
  a line. `serde_yaml` is unmaintained and does not expose positions; look at `yaml-rust2` or `saphyr`
  for the position-preserving pass, possibly alongside a typed deserialization pass.
- `truenas-client` has three read-only trait methods and a fixed-state fake. The real websocket client
  is M3.
- No auth, no jobs, no SSE. All M1.
