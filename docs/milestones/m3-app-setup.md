# M3 — App setup

**Status:** done · **Depends on:** M2 · **Issue:** _(unset)_

## Goal

The first write path. From the UI, take an app from `NeedsSetup` to `Ready`: create `.env` from
`example.env`, fill in values with validation and generated secrets, and run its `setup.toml` to create
directories, secret files and config files under `$APPCONFIG_STORAGE`.

Every run previews its plan before writing anything, and running it twice changes nothing.

## Out of scope

- `provision.postgres` and anything that touches another running stack (M5)
- Any TrueNAS API call — no datasets, no ACLs (M4)
- Deploying anything (M5)
- Adding apps from the catalog (M6)

## Tasks

- [x] Full `setup.toml` schema per [architecture/setup-toml.md](../architecture/setup-toml.md); unknown keys are a hard parse error
- [x] Planner: produce the exact set of writes (dirs, secret files, config files, env keys) without performing any
- [x] Executor for `dirs` — create with explicit mode, idempotent
- [x] Executor for `secrets` — generate from a CSPRNG, or take from a prompted value. **Never overwrite an existing secret file**
- [x] Executor for `config_files` — copy a committed template, or create empty; honour `on_exists = keep | overwrite | prompt`
- [x] Executor for `prompts` — collect values, offer generated defaults, write to `<app>/.env`
- [x] `requires` gate — refuse to run if a required stack is not `Ready`
- [x] Path confinement — every resolved write path must fall under `$APPCONFIG_STORAGE`, `$DATABASE_STORAGE` or the app directory; reject `..` escapes as a hard error
- [x] Abort before any write if a `${...}` in `setup.toml` is unresolved
- [x] Copy `example.env` → `.env` preserving comments and key order
- [x] Env editor API: read (masked), write, per-key validation using `[env_validators]`
- [x] Verify `.env` is gitignored before writing to it; refuse and emit `GIT001` if not
- [x] Secret-storage interface is the only path to secret reads and writes — no direct file I/O at call sites
- [x] Audit log entries for every write; secret values recorded as written, never captured
- [x] Setup runs as a job with SSE progress
- [x] Frontend: `.env` editor with per-key validation, masked values, and reveal
- [x] Frontend: setup wizard driven by `setup.toml` — plan preview, prompts, run, result

## Acceptance criteria

1. An app in `NeedsSetup` reaches `Ready` through the UI with no shell access.
2. Every setup run shows a complete plan before writing; cancelling writes nothing.
3. Running setup twice produces zero changes on the second run. Asserted by a test, not by inspection.
4. An existing secret file is never overwritten; it is reported as already present.
5. Secret files are created with mode `0600` and directories with the mode declared in `setup.toml`.
6. A `setup.toml` path that escapes the permitted roots is rejected before any write.
7. An unresolved variable in `setup.toml` aborts the run with nothing written.
8. Setup refuses to run when a `requires` stack is not `Ready`.
9. `authelia`'s ported `setup.toml` produces the same end state its `setup.sh` intended, with the four bugs in that script not reproduced.
10. No secret value appears in logs, job output or the audit trail.
11. Writing to `.env` is refused if that file is tracked by git.

## Verification

```
just test
just dev
```

Manual and scripted:

- Point `$APPCONFIG_STORAGE` at a scratch directory. Run setup for `authelia`.
- Assert directory modes, secret file modes, and secret lengths match the declarations.
- Run setup again; assert the job reports no changes.
- Modify a secret file, run setup again, assert it is untouched.
- Craft a `setup.toml` with `path = "${APPCONFIG_STORAGE}/../../etc/passwd"`; assert a hard error and no write.
- Run the M1 validator afterwards; the app must now be `Ready`.
- `grep -ri` the logs for a generated secret value; assert no match.

## Notes / decisions made during implementation

- 2026-10-30, packages:
  - The planner and executor are `homelab.PlanSetup` and `homelab.ApplySetup` in `internal/homelab`
    (`setupplan.go`, `setupapply.go`), I/O-free: they read and write only through `Repo`, `SecretStore` and
    `SetupHost`.
  - New `internal/hostfs` implements `homelab.SetupHost` with `os.Root` (D30). It is separate from
    `gitrepo` because `gitrepo` is the only package that touches the repo, while `hostfs` writes under
    the storage roots; one `os.Root` boundary per kind of tree keeps either from reaching the other.
  - `gitrepo.EnvStore` is the plaintext `SecretStore`. It is file-shaped (`ReadEnv`/`WriteEnv` on the whole
    file, so comments and key order survive) and replaces the M0 key/value placeholder.
- 2026-10-30, rules the implementation settled:
  - Permitted roots for `dirs`, `secrets` and `config_files` are `APPCONFIG_STORAGE` and `DATABASE_STORAGE`
    only. The app dir is written only through `SecretStore`, so `compose.yaml` can never be a target (D14).
    Roots must exist and are never created; a path that is a root itself is a problem.
  - `${...}` in `setup.toml` may reference only variables injected by the orchestrator or defined in
    `compose/.global.env`, never `<app>/.env` (D31). No secret can reach a path, a plan or a job line.
  - Implied parent directories are planned with mode 0750. Existing directories are never re-chmodded.
  - Secret files use exclusive create; a dangling or any symlink at a target is a problem. A value in
    `example.env` is never treated as an existing secret.
  - `.env` is written once, before any host write, and only when its bytes change. It must be both
    untracked and gitignored (`GIT001`) before any write (`ErrTracked`, `ErrNotIgnored`).
  - `on_exists = "prompt"` yields an `ask` step; the answer goes in the request (`overwrite`, by path).
  - The plan digest binds a run to the plan the user saw: a fresh plan with a different digest is a 409.
    A decision does not change the digest.
  - Generate encodings: `base64` (standard, padded, no newline), `hex`, `alphanumeric`. Default modes: dirs
    0750, secrets 0600 (no group or other bits allowed), config files 0640. Setuid, setgid and sticky are
    refused.
  - `[[provision.postgres]]` is listed in the plan as deferred and never run (M5).
  - New `SETUP002` field rules: mode, generate, `on_exists`, exactly one of `generate`/`value_from` and of
    `from`/`create_empty`.
- 2026-10-30, server:
  - Operations: `get-stack-env` (404, 422), `update-stack-env` (404, 409, 422), `plan-stack-setup` (404,
    422), `run-stack-setup` (202; 404, 409, 422, 503) and `list-audit`.
  - The audit log is the `audit_log` table (migration `0004_audit.sql`). Actions: `env.reveal`,
    `env.write`, `secret.write`, `dir.create`, `config.write`, `setup.run`. Details hold key names, paths
    and counts, never a value.
  - One setup job runs at a time (job kind `setup.run`); a second start is a 409. `server.Deps` gained
    `Secrets` (`homelab.SecretStore`) and `Host` (`homelab.SetupHost`), wired in `serve`.
- 2026-10-30, dev loop: `_dev-homelab` now also runs `scripts/dev-storage.sh`, so the fixture's storage
  roots in `compose/.global.env` point at `dist/dev-storage/*` and setup can write on a dev machine.
- 2026-10-30, fixture: `authelia/setup.toml` is ported from its `setup.sh` (acceptance criterion 9). It
  adds the `SETUP003` finding (`db-postgres` is Broken) to the stack's codes.
- Known limits and follow-ups:
  - No chown or ownership handling: a container UID may not be able to read 0600 files. M4/M5 concern.
  - `SETUP004` is still not emitted. It needs the setup-run record; the audit log now holds it, wiring is
    a follow-up.
  - A crash during `WriteEnv` can leave a stale `.env.tmp-*`; the next `WriteEnv` for the app removes it.
  - The plan digest does not cover config template content.
  - `hostfs.ReadFile` has an Lstat-then-read window.
  - The plan's deploy env is the pre-run one.
- 2026-10-30, review fixes:
  - Domain review:
    1. A value in `example.env` is never treated as an existing secret.
    2. `${...}` in `setup.toml` is restricted to injected and `.global.env` variables (D31).
    3. `hostfs.Stat` uses Lstat; a symlink at any target or ancestor is a plan problem.
    4. The plan digest binds the run to the reviewed plan (409 on change).
    5. `.env` must be both untracked and gitignored (`ErrTracked`/`ErrNotIgnored`).
    6. Whitespace-only answers are rejected.
    7. `generate` for a key that already has a value is a problem.
    8. fsync before close on every write.
    9. `hostfs.ReadFile` refuses non-regular files.
    10. A relative repo root is a problem.
    11. `[[provision.postgres]]` is listed as deferred.
    12. `config_files.from` may not name an env file.
    13. A stale `.env.tmp-*` is swept before each write.
  - Server review:
    1. A per-app lock serialises env PUTs and setup runs (per-app mutex on `services`, held from read
       through write and across `ApplySetup`).
    2. A no-op PUT on a tracked or non-ignored `.env` is 409, and the view reports real git state.
    3. The setup job rebuilds its input from a fresh report and `setup.toml` with the request's answers
       and digest.
    4. Invalid JSON bodies return 400 "invalid JSON" with no echo.
    5. Control characters in values are rejected.
    6. A key in both `set` and `unset` is 422.
    7. A failed setup job is logged at Warn.
    8. Plan problems in the run 422 are located at `body`.
  - Frontend review:
    1. Adding an existing key is refused, and a staged empty value is marked "Will be set to empty".
    2. Requires and prompts are keyed by index.
    3. `autocomplete="new-password"` and password-manager ignore attributes on every value input.
    4. The wizard re-plans after a 422.
    5. `followJob` resets the log on an SSE reconnect and asks `GET /api/jobs/{id}` for the real state on
       a lost stream.
    6. A repo fetch no longer remounts the editor and wizard.
    7. The editor has a load sequence guard.
- Still manual: walk the UI in a browser (`just dev`: ok-control run setup; db-client create `.env`;
  authelia requires blocked). Frontend checklist: reveal, edit, 422 under input, unset/undo, add/remove,
  run log stream, status update. Also the real-repo check of acceptance criterion 9 against
  `martin-homelab` with `$APPCONFIG_STORAGE` pointed at a scratch dir.
- Follow-ups:
  - Duplicate `requires` entries or `[[prompts]]` keys are not rejected by `ParseSetup` (`SETUP002`
    candidate).
  - `ParseDotEnv` accepts keys like `FOO-BAR` that the env editor's `[A-Za-z_][A-Za-z0-9_]*` rule refuses
    to edit or unset.
  - The stack page's own `load` has no sequence guard.
  - Wire `SETUP004` from the audit log.
- 2026-10-30, e2e record (built binary + curl): ok-control NeedsSetup to Ready via plan/run/job (2 dirs
  created, modes 0750/0755); wrong digest 409; db-client `.env` created byte-identical to `example.env`
  at mode 0600; env-drift PUT 409 on a tracked `.env`; invalid JSON 400; audit entries `env.write`,
  `setup.run`, `dir.create`; zero hits grepping a submitted secret in the server log, audit log and job
  log.
