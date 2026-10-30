# Fixture homelab repo

Test input for the validator. **Every defect here is deliberate.** Do not "fix" anything in this tree —
the tests assert these findings are produced.

Seeded from the real `martin-homelab/compose/` so the genuine defects are genuine, then extended with
synthetic stacks covering the codes the real repo does not happen to exhibit.

Codes are defined in [`docs/validator-checks.md`](../../docs/validator-checks.md).

## What each stack is for

The table is the exact set of codes each stack produces with host-path checks disabled
(`Options.HostPathExists == nil`, as in CI) and `HOMELAB_STORAGE` injected by the orchestrator.
`internal/homelab/validate_test.go` (`TestValidateRepoFixture`) asserts it row by row.

Semantics behind it (see `docs/milestones/m1-validator.md` Notes): the deploy env is injected
`HOMELAB_STORAGE` + `compose/.global.env` + `compose/<app>/.env`, so a variable defined in either env
file resolves in interpolation position. `ENV003` fires only for a variable that exists solely through
a *non-deploy* `env_file:` or through `environment:`. A missing `compose/<app>/.env` is `ENV004` at
**warning** severity and makes the stack `NeedsSetup`; references to keys its `example.env` defines
are then not also reported as `ENV001`. `ENV008` (unused key) is reported only in a stack that also
has an `ENV001`. A stack is `Broken` if and only if it has an error-severity finding.

| Stack | Origin | Status | Codes it must produce |
|---|---|---|---|
| `ok-control` | synthetic | Ready | **none** — zero findings. The control |
| `db-postgres` | real (+ synthetic `.env`) | Broken | `COMPOSE001` (`networks: databases` bare string), `ENV009` (`POSTGRES_PASSWORD=REPLACE_THIS` in `.env`). `${DATABASE_STORAGE}` resolves from `compose/.global.env` line 14: no `ENV003`. `POSTGRES_PASSWORD` is unreferenced but the stack has no `ENV001`, so no `ENV008` |
| `db-prometheus` | real (+ synthetic `.env`, `setup.toml`) | Broken | `ENV001` (`${APPDATA_STORAGE}` undefined), `ENV007` (`METRICS_RETENTION_TIME=60` fails `type = "duration"`), `ENV008` (`PORT` unused, listed against `APPDATA_STORAGE`). `${HTTP_PORT}` is inside a YAML comment, so it is not a reference |
| `authelia` | real | NeedsSetup | `ENV004` (warn: no `.env`), `PATH002` (hardcoded `/mnt/fast/appdata/...`, four `secrets:` and one `volumes:` entry), `SETUP001` (legacy `setup.sh`), `SETUP003` (warn: `setup.toml` has `requires = ["db-postgres"]` and `db-postgres` is Broken) |
| `db-client` | real | NeedsSetup | `ENV004` (warn: no `.env`) |
| `db-redis` | real | NeedsSetup | `ENV004` (warn: no `.env`; reported on `compose/db-redis/.env` itself since the file has no services and so no `env_file:`) |
| `db-timescale` | real | Broken | `COMPOSE001` (`networks: databases`), `ENV004` (warn: no `.env`), `PATH002` (`/mnt/fast/databases/timescale`) |
| `broken-networks` | synthetic | Broken | `NET001` (`backend` is not a contract network), `NET002` (`databases` used, not declared), `NET003` (`home` declared without `external: true`) |
| `broken-paths` | synthetic | Broken | `PATH003` (`..` escape out of the storage roots). With host checks enabled and the path absent, also `PATH001` (line 17, host path nothing creates) |
| `env-drift` | synthetic | Broken | `ENV001` (`${GREETING}` — `.env` lacks it, and `environment: GREETING: ${GREETING}` does not supply it), `ENV005` (`GREETING` in `example.env`, absent from `.env`), `ENV006` (`UNDOCUMENTED_KEY` only in `.env`), `ENV008` (`UNDOCUMENTED_KEY` unused, listed against `GREETING`), `ENV010` (`SERVICE_NAME` ≠ directory name) |
| `env-container-only` | synthetic | Broken | `ENV003` ×2 (`${DATA_DIR}` defined only under `environment:`, `${EXTRA_DIR}` only in the non-deploy `env_file: ./extra.env`, both used in `volumes:`), `ENV002` (`$$MISSING` in `command:` that no container-scope entry defines) |
| `broken-setup` | synthetic | Broken | `SETUP002` (`recursive_chown` is not a registered primitive), `SETUP003` (requires `db-postgres`, which is `Broken`), `SETUP006` (`[app] name = "setup-broken"` ≠ directory) |
| `broken-toml` | synthetic | Broken | `SETUP005` (unclosed `[[dirs]` table header: not parseable TOML) |
| `no-compose` | synthetic | Broken | `COMPOSE005` (app directory with only `example.env`); no other check runs |
| `Bad_Name` | synthetic | Broken | `COMPOSE006` (directory name does not match `[a-z0-9][a-z0-9-]*`); the stack is not validated further |
| `bad-yaml` | synthetic | Broken | `COMPOSE002` — not parseable as YAML; no other check runs |
| `legacy-name` | synthetic | Ready | `COMPOSE004` (named `compose.yml`), `COMPOSE003` (no `deploy.resources.limits`) |

`authelia/setup.toml` is the port of its `setup.sh` (M3 acceptance criterion 9) and includes a deferred
`[[provision.postgres]]`, which setup lists but does not run.

With host checks enabled and every host path absent, `ok-control` reports one `PATH001` at **info**
severity (its `setup.toml` `[[dirs]]` declares the path) and becomes `NeedsSetup`
(`TestValidateRepoHostPathChecks`).

`SETUP004` (setup declared but never run) needs the setup-run record from M3 and is not emitted in M1.

The repo-level codes need git state, which a plain directory cannot express. `GIT001` (a real env
file tracked by git) is produced by `scripts/init-fixture-repo.sh`, which force-adds
`compose/env-drift/.env` past the gitignore. `GIT002` and `GIT003` come from the repo's dirty and
behind state; the domain tests drive all three through the fake repo's status (`TestGitFindings`).

## Why the gitignore is stored as `gitignore`, undotted

A real `.gitignore` in this directory applies to **this** repository as well as to the fixture. It
would match the fixture's own `compose/.global.env` and `compose/*/.env`, so those files would never
be committed and a fresh clone would get a fixture that is silently missing its env files — changing
what the validator finds without anything failing.

`scripts/init-fixture-repo.sh` restores the dot when it materialises the repo.

## Why this tree has no `.git`

A nested git repository inside this repository is awkward to commit and easy to get wrong. Instead the
tree is committed as plain files, and `scripts/init-fixture-repo.sh` copies it to a temporary directory
and initialises a real repository there when a test needs git history, branches or tracked-file state.

## Adding a case

1. Add the stack here.
2. Add its row to the table above.
3. Register the code in `docs/validator-checks.md` if it is new.
4. Add the assertion in the validator tests.

A fixture nothing asserts against is dead weight — wire it up in the same change.
