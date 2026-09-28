# M0 — Skeleton

**Status:** not started · **Depends on:** — · **Issue:** _(unset)_

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

- [ ] Cargo workspace with the six crates from [architecture/components.md](../architecture/components.md), each compiling as a stub
- [ ] `rust-toolchain.toml` pinning the toolchain
- [ ] `justfile` with the full recipe list (see the Verification section); recipes that belong to later milestones may print "not yet implemented"
- [ ] `just setup` installs cargo tools and frontend deps from a clean checkout
- [ ] SvelteKit app with `adapter-static`, `ssr = false`, TypeScript, Tailwind, Vite
- [ ] `vite dev` proxies `/api` to `$ORCHESTRATOR_API`, defaulting to localhost
- [ ] axum server with `GET /api/health` returning version and build info
- [ ] `rust-embed` serves the built SPA from the release binary, with SPA fallback routing
- [ ] `utoipa` wired up; OpenAPI served at `/api/openapi.json`; frontend client generated from it
- [ ] `orchestrator-db`: sqlx sqlite pool, migration harness, one trivial migration, offline query data checked in
- [ ] `fixtures/homelab-repo/` — copy `martin-homelab/compose/` **including every defect** listed in [validator-checks.md](../validator-checks.md), plus at least one fully correct stack as a control
- [ ] `fixtures/homelab-repo/` has a real `.git` so repo operations can be tested (committed as a bundle or created by a fixture script)
- [ ] `truenas-client`: `TrueNasApi` trait definition and a `FakeTrueNas` stub. No real implementation
- [ ] Dockerfile using cargo-chef; `just build-image` cross-compiles to `linux/amd64` via buildx
- [ ] CI workflow running `just check && just test && just build-image`
- [ ] `.gitignore` extended for `node_modules`, frontend build output, `*.db`, `.env.dev`

## Acceptance criteria

1. From a clean clone, `just setup && just dev` works with no manual steps beyond installing `just`, Rust and Node.
2. The SPA loads in a browser and displays data fetched from `GET /api/health`.
3. `just build` produces a single binary that serves the SPA with no `frontend/` directory present at runtime.
4. `just build-image` produces a `linux/amd64` image from the darwin/arm64 dev host.
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

_(append as work happens — this is the handoff surface for the next session)_
