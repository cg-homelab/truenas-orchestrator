# M1 — Read-only validator CLI (the MVP)

**Status:** done · **Depends on:** M0 · **Issue:** _(unset)_

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

- [x] Layout contract types: repo, app, env file, template/real pairing
- [x] Deploy-env resolution: `.global.env` then `<app>/.env`, with orchestrator-injected `HOMELAB_STORAGE` beneath both; record provenance per key
- [x] Container-scope resolution from the `env_file:` chain plus `environment:`
- [x] Finding type: stable code, severity, file, line, message, suggested fix
- [x] Stack status derivation: `Ready` / `NeedsSetup` / `Broken`
- [x] Implement every check in `validator-checks.md`: `COMPOSE001-004`, `ENV001-010`, `PATH001-003`, `NET001-003`, `SETUP001-004`, `GIT001-003` (`SETUP004` is not emitted in M1: needs M3 run state)
- [x] `setup.toml` parsing for `[env_validators]` and schema validation only, via `pelletier/go-toml/v2` with `DisallowUnknownFields()` so an unknown key is `SETUP002` naming the key
- [x] Value validators: `duration`, `port`, `identifier`, `absolute_path`, `non_empty`, `integer`, `bool`, `one_of`
- [x] Secret-storage interface defined (not yet used for writes)
- [x] `homelab.ComposeParser` interface and `homelab.ValidateRepo(ctx, repo, parser)` — the single validation pipeline that both the CLI and (in M2) the server call

### Compose — `internal/compose`

- [x] Parse `compose.yaml` preserving line numbers
- [x] Extract every variable reference **tagged with its scope** (interpolation vs container)
- [x] Support `${VAR}`, `${VAR:-d}`, `${VAR-d}`, `${VAR:?e}`, `${VAR?e}`, `$VAR`; treat `$$` as a literal `$`
- [x] A reference with a default resolves and is not reported missing
- [x] Structural validation sufficient to catch `networks: databases`
- [x] `COMPOSE001` decided by `compose-go/v2/schema.Validate` on the raw YAML map; the compose-go loader is never used

### Repo — `internal/gitrepo`

- [x] Walk `compose/` into the app model; read env files
- [x] Report branch, last fetch time, dirty state, ahead/behind
- [x] Detect env files tracked by git (`GIT001`)

### CLI

- [x] `orchestrator validate [--json] <path>` prints findings grouped by stack, each with code, file, line, message and fix
- [x] Exits non-zero on any error-severity finding
- [x] `--json` flag emits the same report as JSON
- [x] Secret values are masked in the output by default

### Tests

- [x] Golden-file snapshot tests over `fixtures/homelab-repo/` covering every implemented code, with `-update` to regenerate

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

### Verification record (2026-10-30)

- Goldens regenerated with `go test ./cmd/orchestrator -update` and re-run clean. Every fixture stack is
  present with the codes in `FIXTURE.md`; no temp paths and no env values appear in the text or JSON
  output; all 269 JSON env vars carry `"value": "[REDACTED]"`.
- `just check && just test` pass.
- Read-only run: fixture copy made with `chmod -R a-w`, `orchestrator validate --homelab-storage
  /srv/homelab` printed the full report and exited 1 (error findings) with no I/O error; `--json` also ok.
- `--check-host-paths` emits `PATH001` for the `/mnt/...` paths, which do not exist on the dev machine.
- `validate.go` does not reference the server package.
- **Not run here:** acceptance criterion 1 (`just validate ../martin-homelab`), because that repo is not
  on this machine. This is the one manual check for Martin.
- 2026-10-30, after independent review: `gitrepo` runs git with `GIT_OPTIONAL_LOCKS=0` and
  `--no-optional-locks` (a test asserts `.git` is untouched by `Status`/`Tracked`, and `validate` is tested
  against a read-only copy); `ReadFile`/`Stat` return `ErrUnreadable` for root escapes and non-regular
  files; `Apps` lists every directory (COMPOSE006 reports bad names); `--homelab-storage` is made
  absolute. Goldens regenerated; `just check && just test` pass.

### Domain and compose semantics (internal/homelab, internal/compose)

These resolve contradictions between the registry, env-scopes.md and the fixture notes. The fixture
table in `fixtures/homelab-repo/FIXTURE.md` is the executable form of them.

- **API.** `ValidateRepo(ctx, repo, parse, opts Options)`. `Options.HomelabStorage` is injected as
  `HOMELAB_STORAGE` and always wins over `.global.env` (the CLI passes the repo root; empty means
  nothing is injected and the files' value is used). `Options.HostPathExists == nil` disables
  `PATH001`; a non-nil error from it aborts validation.
- **Every `${VAR}` / `$VAR` in compose.yaml is an interpolation reference**, wherever it sits,
  including `environment:` values (Docker Compose semantics). Keys are never interpolated. `$$` is a
  literal `$`. References inside YAML comments do not exist (db-prometheus `${HTTP_PORT}`).
- **Deploy env** = injected `HOMELAB_STORAGE` + `compose/.global.env` + `compose/<app>/.env`, later
  wins, injected `HOMELAB_STORAGE` wins over both. Env files: `KEY=VALUE`, `#` comments, blank
  lines, `export ` prefix, matching quotes stripped, ` # comment` stripped from unquoted values; no
  value interpolation inside env files; malformed lines ignored.
- **Container scope** per service = every `env_file:` entry in list order (path interpolated with the
  deploy env; relative paths are relative to the app dir; absolute paths under `HOMELAB_STORAGE` or
  the repo root map into the repo; anything else is unreadable and makes that service's key set
  *unknown*), then `environment:` overrides. A bare `- KEY` takes the deploy-env value.
  `StackReport.Container` merges services; with several services the alphabetically first one that
  defines a key wins.
- **ENV001 / ENV003**: a reference with no default, not in the deploy env. ENV003 when the service's
  container scope has the name (top-level references use the union of services), else ENV001. An
  `environment:` entry that references its own name (`GREETING: ${GREETING}`) does not count as
  container-only availability, so env-drift's `${GREETING}` is ENV001. A reference nested in a
  default (`${A:-${B}}`) is checked only when the enclosing name is unset; references in `:+`/`+`
  alternatives and `:?` messages are optional. `${VAR:?e}` is checked like a plain reference.
- **ENV002** reading: a *container-position* reference is the escaped `$$VAR` / `$${VAR}` (Compose
  leaves `$VAR` in place) where a shell evaluates it: `command:`, `entrypoint:` or
  `healthcheck.test` in string form, in list form after a shell (`sh`, `bash`, `ash`, `/bin/sh`,
  `/bin/bash`) and `-c` (or combined flags ending in `c`, such as `-ec`), or after `CMD-SHELL`.
  Names the same script assigns (`X=...`) or loops over (`for X in`) are skipped. `environment:`
  values are never evaluated by a shell, so `$$argon2id$$v=19...` there is not a read. Skipped when
  the scope is unknown, and for keys `example.env` defines while the own `.env` is missing.
  Image-provided variables are a known gap (see Follow-ups).
- **ENV004**: an `env_file:` path that is in the repo and missing (long form `required: false`
  exempt). The app's own `.env` is also expected whenever `example.env` exists, even when
  `env_file:` does not list it, because the orchestrator reads it for the deploy env (db-redis).
  Severity is split so the status invariant holds with no exceptions: the app's own `.env` (setup
  creates it from `example.env`) is **warn** and the stack is `NeedsSetup`; any other missing path is
  error and the stack is `Broken`. A stack is `Broken` if and only if it has an error finding. While
  the own `.env` is missing, references to keys its `example.env` defines are not also reported as
  ENV001 or ENV002 (ENV004 explains them). ENV004 (error) also covers an env file that exists but
  cannot be used: `Repo.ReadFile` returned `homelab.ErrUnreadable` (symlink leaving the repo, not a
  regular file), or it has an unterminated quoted value (the keys before it are used). The
  service's container scope is then unknown. Other repo errors abort validation. An env_file path
  that interpolates anything other than `HOMELAB_STORAGE` is shown raw (as written in compose.yaml)
  in messages and in `EnvVar.Source`, so no other variable's value is printed; a failing
  `HostPathExists` error names the raw compose path and line, never the resolved path.
- **Env files** follow compose-go's dotenv quoting: a quoted value ends at the first unescaped
  matching quote (text after it is ignored), may span lines, `\'` escapes a single quote, and
  double quotes unescape `\n \t \r \\ \" \$`.
- **Empty values**: `:-` and `:?` treat set-but-empty as unset; `-` and `?` do not. `${A:-${B}}`
  with `A=` checks B; `${VAR:?e}` with `VAR=` is ENV001 ("set but empty").
- **ENV005/006** only when both `example.env` and `.env` exist; ENV005 points at the key's line in
  `example.env`, ENV006 at its line in `.env`.
- **ENV008**: a key in `example.env` or `.env` (not `.global.env`) that compose.yaml never
  references (interpolation or `$$` reference, with or without default, or a bare `environment:`
  key), **reported only when the same stack has at least one ENV001**; the message lists the
  unresolved names. This is the registry's case (`PORT` defined while `${HTTP_PORT}` is unresolved).
  Without the condition it flagged every key an image reads straight from `env_file:`
  (`POSTGRES_PASSWORD` in db-postgres and db-timescale).
- **ENV009** checks `.env` only (a template is expected to hold placeholders): `REPLACE_THIS`,
  `CHANGEME`, `changeme`, `TODO`.
- **ENV007** applies `[env_validators]` to deploy-env values; an absent key is skipped. `one_of` takes
  its allowed set from `values = [...]`; `not_one_of` applies to every type. An unknown `type` is
  SETUP002.
- **PATH**: host paths are bind-mount sources (short syntax with a path-like host side, or long
  syntax `type: bind`) and top-level `secrets: <name>: file:`. A literal absolute path (starting
  `/`) whose raw text starts with a storage-root value or `/mnt/` is PATH002; if it also contains
  `..` and its cleaned form is outside every root it is PATH003 too
  (`/mnt/fast/appdata/../../../etc`). A literal path outside the roots without `..`
  (`/var/run/docker.sock`) is allowed. A path starting with `$` or `.` is
  interpolated (relative to the app dir) and is PATH003 when, after `path.Clean`, it is outside every
  storage-root value (`HOMELAB_STORAGE`, `APPCONFIG_STORAGE`, `DATABASE_STORAGE`, `MEDIA_STORAGE`). A
  path with an unresolved variable is skipped (ENV001/ENV003 covers it). PATH001 is checked on every
  resolved absolute path except a PATH003 one; it is info, and makes the stack `NeedsSetup`, when
  setup.toml `[[dirs]] path`, `[[secrets]] file` or `[[config_files]] to` declares exactly that path.
  Messages show the raw compose path, never the resolved one (which contains env values).
- **NET001** per service reference outside the four networks, plus top-level declarations no service
  uses. The contract applies to the network's name: a declaration's `name:` when set
  (`proxy_net: {name: proxy, external: true}` passes), else its key. A bare-string `networks: databases` is COMPOSE001 and is still read as one network.
- **COMPOSE001** for a schema violation (line of the offending node, from compose-go's instance path;
  keys containing dots resolve by longest match), a non-mapping top level, and an interpolation
  template Compose rejects (`${}`, `${FOO BAR}`, unterminated `${FOO`), at the template's line.
  **COMPOSE006** (new) for an app directory name not matching `[a-z0-9][a-z0-9-]*`
  (`Repo.Apps` returns every directory); the stack is not validated further. Reference lines inside
  block (`|`, `>`) and multi-line flow scalars are the line of the reference itself. **COMPOSE005** (new) for an app directory with neither compose.yaml nor
  compose.yml; no other check runs. **COMPOSE002** stops all other checks for that stack.
  **COMPOSE004** still parses compose.yml.
- **SETUP002** for an unknown key, a value of the wrong type for a known key (`requires = "a"`: the
  file is valid TOML, so it is a schema problem; the other tables are still decoded one by one), and
  an unknown `type` in `[env_validators]`. **SETUP005** (new) only when the file is not valid TOML
  (or not a readable regular file); nothing else from that setup.toml is used. **SETUP006** (new) when
  `[app] name` is set and differs from the directory name. **SETUP003** also fires when `requires`
  names a stack that does not exist. **SETUP004 is not emitted**: it needs the setup-run record from
  M3.
- **GIT001** for `compose/.global.env` and every `compose/<app>/.env` that `Repo.Tracked` reports;
  GIT002 on `Status.Dirty`; GIT003 on `Status.Behind > 0`. Repo-level, in `Report.Findings`.
- **Interpolation engine.** The grammar is implemented in `internal/homelab/interpolate.go`
  (`ScanTemplate`, `Interpolate`) and differentially tested against `compose-go/v2/template`
  (`TestGrammarMatchesComposeGo`); see D29 in `docs/decisions.md` for why the library is not called
  at runtime. compose-go's `schema.Validate` still decides COMPOSE001.

### Follow-ups (known gaps)

- ENV002 reports variables the image itself provides, such as `$$HOSTNAME` or `$$HOME`, because
  container scope only knows `env_file:` and `environment:`.
- A service with no `networks:` key (it joins the implicit `default` network) is not checked by
  NET001.
- `schema.Validate` reports only the most specific error per file, so a compose file with several
  spec violations shows one COMPOSE001 at a time.
- `.env` values are not interpolated, while Compose expands `${X}` inside env_file values. A volume
  that uses such a key resolves to the unexpanded text, and when that leaves `$` in the path the
  PATH checks are skipped for it. Follow-up for M2.
