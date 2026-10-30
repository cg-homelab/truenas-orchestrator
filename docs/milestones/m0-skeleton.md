# M0 — Skeleton

**Status:** done · **Depends on:** — · **Issue:** _(unset)_

## Goal

The project builds, tests, lints and runs end to end with nothing in it. `just dev` starts the backend and
the frontend; the SPA loads in a browser and successfully calls a health endpoint on the backend. CI runs
the same checks. A fixture homelab repo exists to develop the validator against.

## Out of scope

- Any validation logic (M1)
- Auth (M2)
- Any database schema beyond the migration harness itself (M2)
- Any TrueNAS API call (M4)

## Tasks

- [x] `go.mod` with `go 1.27`; module path `github.com/cg-homelab/truenas-orchestrator`
- [x] `cmd/orchestrator/main.go` with `run(ctx, args, environ []string, stdout) error` and the subcommands `serve`, `validate`, `openapi` stubbed; `Config` parsed from `environ` with `caarlos0/env/v11`
- [x] `internal/homelab`, `internal/compose`, `internal/gitrepo`, `internal/truenas`, `internal/sqlite`, `internal/server` compiling as stubs, per [architecture/components.md](../architecture/components.md); `homelab.TrueNAS`, `homelab.Repo`, `homelab.SecretStore` interfaces declared; `truenas.Fake` stub. No real implementations
- [x] [architecture/go-guidelines.md](../architecture/go-guidelines.md) and root `CLAUDE.md` in place; `.golangci.yml` from the guidelines committed and passing on the stubs, including the depguard rule for `internal/homelab` and `internal/compose`
- [x] `justfile` with the full recipe list (`setup`, `check`, `test`, `dev`, `build`, `build-linux`, `build-image`, `check-api`, `generate`, `validate`, `smoke`); recipes that belong to later milestones may print "not yet implemented"
- [x] `just setup` installs golangci-lint, gofumpt, govulncheck and frontend deps from a clean checkout; sqlc and goose run via `go tool`
- [x] huma on `http.ServeMux`: `GET /api/health` returning version and build info
- [x] `orchestrator openapi` dumps the OpenAPI document; `openapi-typescript` generates `frontend/src/lib/api.d.ts`; `just check-api` fails on drift
- [x] `internal/sqlite`: modernc driver, goose migration harness from `embed.FS`, one trivial migration, sqlc config with one query, generated code committed
- [x] SvelteKit app with `adapter-static`, `ssr = false`, TypeScript, Tailwind, Vite
- [x] `vite dev` proxies `/api` to `$ORCHESTRATOR_API`, defaulting to localhost
- [x] `embed` serves the built SPA from the binary, with SPA fallback routing
- [x] The `//go:embed` directive lives in a Go file under `frontend/` (see [architecture/components.md](../architecture/components.md)); because `frontend/build/` is gitignored, `go build`, `go vet` and lint need the SPA built once on a fresh clone. `just setup` builds it first, CI runs `just setup` before `just check`, and the README says so.
- [x] `fixtures/homelab-repo/` salvaged from branch `feat/milestone-0` (including `FIXTURE.md` and every deliberate defect listed in [validator-checks.md](../validator-checks.md), plus the clean control stack)
- [x] `fixtures/homelab-repo/` has a real `.git` so repo operations can be tested (created by a fixture script or committed as a bundle)
- [x] `Dockerfile`: `alpine` with `git` and `openssh-client`; binary built on the host with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` and copied in; `just build-image` runs in minutes with no emulation
- [x] CI workflow running `just check && just test && just build-linux && just build-image`, with `go test -race -shuffle=on` and `govulncheck`
- [x] `.gitignore` for `dist/`, `*.db`, `node_modules/`, `frontend/build/`, `.env.dev`

## Acceptance criteria

1. From a clean clone, `just setup && just dev` works with no manual steps beyond installing `just`, Go 1.27 and Node.
2. The SPA loads in a browser and displays data fetched from `GET /api/health`.
3. `just build` produces a single binary that serves the SPA with no `frontend/` directory present at runtime.
4. `just build-image` completes on the darwin/arm64 dev host in minutes and produces a `linux/amd64` image.
5. `just check` fails on any golangci-lint finding, any unformatted file, and any `svelte-check` error; the depguard rule is proven by a deliberate `import "os"` in `internal/homelab` failing the check.
6. CI passes with no network access to TrueNAS and no database present at build time.
7. `fixtures/homelab-repo/` contains at least one instance of every defect in [validator-checks.md](../validator-checks.md) marked with a real example, so M1 has something to detect.
8. On a fresh clone, `just setup` followed by `go build ./...` succeeds; `go build ./...` before `just setup` fails only with the embed error, nothing else.

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

### 2026-10-30 integration pass

Verified from the working tree: `just setup`, `just check`, `just test`, `just build`, `just build-linux`, `just build-image` all pass; `just generate` replaced the provisional `api.d.ts` with the generated one.

- Go-worker deviations from the guide:
  - `go:generate` for sqlc uses `-f ../../sqlc.yaml`.
  - OpenAPI `info.version` is `deps.Version` or `"dev"`, not build info, so the spec is identical between `go run` and a built binary (needed for `just check-api`).
  - `addRoutes` also registers the method-less `/api/` 404 handler and the `/` SPA handler, because registering `GET /` beside `/api/` makes `http.ServeMux` panic on the pattern conflict.
- `sqlc.yaml` uses `inflection_exclude_table_names: [schema_meta]` so sqlc does not singularise the table name.
- The justfile exports `PATH` with `$(go env GOPATH)/bin` prepended, so `go install`ed tools (golangci-lint, gofumpt, govulncheck) are found even when GOPATH/bin is not on PATH.
- Image build: `just build-image` was verified (OrbStack running); image `truenas-orchestrator:dev` is `linux/amd64`.
- Fresh-clone embed: with `frontend/build` removed, `go build ./...` fails only with `frontend/embed.go:14:12: pattern all:build: no matching files found`; after `npm run build` (part of `just setup`) it succeeds.
- govulncheck: `No vulnerabilities found.`
- Exit-code assumptions proven: `golangci-lint fmt --diff` exits 1 on unformatted Go; `just check-api` exits 1 on a modified `api.d.ts`; `go tool sqlc diff` exits 1 when a query is renamed. All restored afterwards.
- Binary serves the SPA (`/`, SPA fallback route) and `/api/health` JSON with `frontend/` moved away; `/api/nope` returns 404. `dist/linux-amd64/orchestrator` is ELF x86-64 statically linked.
- depguard proof (acceptance 5) verified by the Go worker: a probe file importing `os` in `internal/homelab` failed lint with `import 'os' is not allowed from list 'domain-no-io': the domain is I/O-free; take bytes or values as arguments (depguard)`, exit 1; likewise `net/http` and `os/exec` in `internal/compose`.
- Fixture `.git`: no nested `.git` is committed (see `fixtures/homelab-repo/FIXTURE.md`). `scripts/init-fixture-repo.sh <dest>` materialises the fixture as a real git repo with two commits, the second force-adding `compose/env-drift/.env` for GIT001 (35 tracked files). Tests call the script into `t.TempDir()`.
- Manual check for Martin (acceptance 2): run `just dev`, open http://localhost:5173, confirm version/commit/go_version render.
