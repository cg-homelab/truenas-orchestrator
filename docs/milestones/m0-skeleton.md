# M0 — Skeleton

**Status:** not started · **Depends on:** — · **Issue:** _(unset)_

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

- [ ] `go.mod` with `go 1.27`; module path `github.com/cg-homelab/truenas-orchestrator`
- [ ] `cmd/orchestrator/main.go` with `run(ctx, args, environ []string, stdout) error` and the subcommands `serve`, `validate`, `openapi` stubbed; `Config` parsed from `environ` with `caarlos0/env/v11`
- [ ] `internal/homelab`, `internal/compose`, `internal/gitrepo`, `internal/truenas`, `internal/sqlite`, `internal/server` compiling as stubs, per [architecture/components.md](../architecture/components.md); `homelab.TrueNAS`, `homelab.Repo`, `homelab.SecretStore` interfaces declared; `truenas.Fake` stub. No real implementations
- [ ] [architecture/go-guidelines.md](../architecture/go-guidelines.md) and root `CLAUDE.md` in place; `.golangci.yml` from the guidelines committed and passing on the stubs, including the depguard rule for `internal/homelab` and `internal/compose`
- [ ] `justfile` with the full recipe list (`setup`, `check`, `test`, `dev`, `build`, `build-linux`, `build-image`, `check-api`, `generate`, `validate`, `smoke`); recipes that belong to later milestones may print "not yet implemented"
- [ ] `just setup` installs golangci-lint, gofumpt, govulncheck and frontend deps from a clean checkout; sqlc and goose run via `go tool`
- [ ] huma on `http.ServeMux`: `GET /api/health` returning version and build info
- [ ] `orchestrator openapi` dumps the OpenAPI document; `openapi-typescript` generates `frontend/src/lib/api.d.ts`; `just check-api` fails on drift
- [ ] `internal/sqlite`: modernc driver, goose migration harness from `embed.FS`, one trivial migration, sqlc config with one query, generated code committed
- [ ] SvelteKit app with `adapter-static`, `ssr = false`, TypeScript, Tailwind, Vite
- [ ] `vite dev` proxies `/api` to `$ORCHESTRATOR_API`, defaulting to localhost
- [ ] `embed` serves the built SPA from the binary, with SPA fallback routing
- [ ] The `//go:embed` directive lives in a Go file under `frontend/` (see [architecture/components.md](../architecture/components.md)); because `frontend/build/` is gitignored, `go build`, `go vet` and lint need the SPA built once on a fresh clone. `just setup` builds it first, CI runs `just setup` before `just check`, and the README says so.
- [ ] `fixtures/homelab-repo/` salvaged from branch `feat/milestone-0` (including `FIXTURE.md` and every deliberate defect listed in [validator-checks.md](../validator-checks.md), plus the clean control stack)
- [ ] `fixtures/homelab-repo/` has a real `.git` so repo operations can be tested (created by a fixture script or committed as a bundle)
- [ ] `Dockerfile`: `alpine` with `git` and `openssh-client`; binary built on the host with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` and copied in; `just build-image` runs in minutes with no emulation
- [ ] CI workflow running `just check && just test && just build-linux && just build-image`, with `go test -race -shuffle=on` and `govulncheck`
- [ ] `.gitignore` for `dist/`, `*.db`, `node_modules/`, `frontend/build/`, `.env.dev`

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
