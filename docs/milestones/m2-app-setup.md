# M2 — App setup

**Status:** not started · **Depends on:** M1 · **Issue:** _(unset)_

## Goal

The first write path. From the UI, take an app from `NeedsSetup` to `Ready`: create `.env` from
`example.env`, fill in values with validation and generated secrets, and run its `setup.toml` to create
directories, secret files and config files under `$APPCONFIG_STORAGE`.

Every run previews its plan before writing anything, and running it twice changes nothing.

## Out of scope

- `provision.postgres` and anything that touches another running stack (M4)
- Any TrueNAS API call — no datasets, no ACLs (M3)
- Deploying anything (M4)
- Adding apps from the catalog (M5)

## Tasks

- [ ] Full `setup.toml` schema per [architecture/setup-toml.md](../architecture/setup-toml.md); unknown keys are a hard parse error
- [ ] Planner: produce the exact set of writes (dirs, secret files, config files, env keys) without performing any
- [ ] Executor for `dirs` — create with explicit mode, idempotent
- [ ] Executor for `secrets` — generate from a CSPRNG, or take from a prompted value. **Never overwrite an existing secret file**
- [ ] Executor for `config_files` — copy a committed template, or create empty; honour `on_exists = keep | overwrite | prompt`
- [ ] Executor for `prompts` — collect values, offer generated defaults, write to `<app>/.env`
- [ ] `requires` gate — refuse to run if a required stack is not `Ready`
- [ ] Path confinement — every resolved write path must fall under `$APPCONFIG_STORAGE`, `$DATABASE_STORAGE` or the app directory; reject `..` escapes as a hard error
- [ ] Abort before any write if a `${...}` in `setup.toml` is unresolved
- [ ] Copy `example.env` → `.env` preserving comments and key order
- [ ] Env editor API: read (masked), write, per-key validation using `[env_validators]`
- [ ] Verify `.env` is gitignored before writing to it; refuse and emit `GIT001` if not
- [ ] Secret-storage trait is the only path to secret reads and writes — no direct file I/O at call sites
- [ ] Audit log entries for every write; secret values recorded as written, never captured
- [ ] Setup runs as a job with SSE progress
- [ ] Frontend: `.env` editor with per-key validation, masked values, and reveal
- [ ] Frontend: setup wizard driven by `setup.toml` — plan preview, prompts, run, result

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

_(append as work happens)_
