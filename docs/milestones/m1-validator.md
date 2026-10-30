# M1 — Read-only validator CLI (the MVP)

**Status:** not started · **Depends on:** M0 · **Issue:** _(unset)_

## Goal

Point `orchestrator validate [--json] <path>` at a homelab repo and get a correct, actionable report. It parses
every `compose.yaml`, resolves both environment scopes, runs every check in
[validator-checks.md](../validator-checks.md), and prints findings that name the file, the line and a
suggested fix.

Success is concrete: run it against the real `martin-homelab` and it reports exactly the defects listed
in the Context table of the plan — no more, no less.

## Out of scope

- Any HTTP route, server or auth (M2)
- Any frontend (M2)
- Any write to the homelab repo or the filesystem (M3)
- Running `setup.toml` (M3) — it is only *parsed* here, for `[env_validators]` and `SETUP00x`
- Any TrueNAS API call (M4)
- Deploy, manifests, overlays (M5)
- The catalog (M6)

## Tasks

### Domain — `internal/homelab`, no I/O

- [ ] Layout contract types: repo, app, env file, template/real pairing
- [ ] Deploy-env resolution: `.global.env` then `<app>/.env`, with orchestrator-injected `HOMELAB_STORAGE` beneath both; record provenance per key
- [ ] Container-scope resolution from the `env_file:` chain plus `environment:`
- [ ] Finding type: stable code, severity, file, line, message, suggested fix
- [ ] Stack status derivation: `Ready` / `NeedsSetup` / `Broken`
- [ ] Implement every check in `validator-checks.md`: `COMPOSE001-004`, `ENV001-010`, `PATH001-003`, `NET001-003`, `SETUP001-004`, `GIT001-003`
- [ ] `setup.toml` parsing for `[env_validators]` and schema validation only, via `pelletier/go-toml/v2` with `DisallowUnknownFields()` so an unknown key is `SETUP002` naming the key
- [ ] Value validators: `duration`, `port`, `identifier`, `absolute_path`, `non_empty`, `integer`, `bool`, `one_of`
- [ ] Secret-storage interface defined (not yet used for writes)
- [ ] `homelab.ComposeParser` interface and `homelab.ValidateRepo(ctx, repo, parser)` — the single validation pipeline that both the CLI and (in M2) the server call

### Compose — `internal/compose`

- [ ] Parse `compose.yaml` preserving line numbers
- [ ] Extract every variable reference **tagged with its scope** (interpolation vs container)
- [ ] Support `${VAR}`, `${VAR:-d}`, `${VAR-d}`, `${VAR:?e}`, `${VAR?e}`, `$VAR`; treat `$$` as a literal `$`
- [ ] A reference with a default resolves and is not reported missing
- [ ] Structural validation sufficient to catch `networks: databases`
- [ ] `COMPOSE001` decided by `compose-go/v2/schema.Validate` on the raw YAML map; the compose-go loader is never used

### Repo — `internal/gitrepo`

- [ ] Walk `compose/` into the app model; read env files
- [ ] Report branch, last fetch time, dirty state, ahead/behind
- [ ] Detect env files tracked by git (`GIT001`)

### CLI

- [ ] `orchestrator validate [--json] <path>` prints findings grouped by stack, each with code, file, line, message and fix
- [ ] Exits non-zero on any error-severity finding
- [ ] `--json` flag emits the same report as JSON
- [ ] Secret values are masked in the output by default

### Tests

- [ ] Golden-file snapshot tests over `fixtures/homelab-repo/` covering every implemented code, with `-update` to regenerate

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
4. `HOMELAB_STORAGE` is never reported as unresolved, and the resolved env provenance for it is orchestrator-injected.
5. The control stack in the fixtures reports `Ready` with zero findings.
6. Snapshot tests over `fixtures/homelab-repo/` cover every implemented code.
7. Nothing in this milestone writes to the homelab repo or the filesystem, verified by running the validator against a read-only mount.
8. M1 adds no HTTP routes; the `validate` subcommand does not construct the server, and `internal/homelab` and `internal/compose` pass the depguard rule.

## Verification

```
just test                            # includes the golden snapshot tests over fixtures — the inner loop
just validate ../martin-homelab      # must match the Context table exactly
just check && just test
```

Flags precede the path (`orchestrator validate [--json] <path>`) because stdlib `flag` stops at the first positional argument.

Manual checks:

- `chmod -R a-w` a copy of a homelab repo, run the validator against it, and confirm it completes with no error.
- `grep -ri` the CLI output for a known secret value and confirm no match (values are masked by default).

## Notes / decisions made during implementation

_(append as work happens)_
