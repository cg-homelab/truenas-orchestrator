# Fixture homelab repo

Test input for the validator. **Every defect here is deliberate.** Do not "fix" anything in this tree —
the tests assert these findings are produced.

Seeded from the real `martin-homelab/compose/` so the genuine defects are genuine, then extended with
synthetic stacks covering the codes the real repo does not happen to exhibit.

Codes are defined in [`docs/validator-checks.md`](../../docs/validator-checks.md).

## What each stack is for

| Stack | Origin | Codes it must produce |
|---|---|---|
| `ok-control` | synthetic | **none** — must validate as `Ready` with zero findings. The control |
| `db-postgres` | real | `COMPOSE001` (`networks: databases` bare string), `ENV003` (`${DATABASE_STORAGE}` only in container scope), `ENV009` (`POSTGRES_PASSWORD=REPLACE_THIS`) |
| `db-prometheus` | real | `ENV001` (`${APPDATA_STORAGE}` undefined; `${HTTP_PORT}` while `PORT` is defined), `ENV007` (`METRICS_RETENTION_TIME=60`, no unit), `ENV008` (`PORT` unused) |
| `authelia` | real | `PATH002` (hardcoded `/mnt/fast/appdata/...`), `SETUP001` (legacy `setup.sh`) |
| `db-client`, `db-redis`, `db-timescale` | real | `ENV004` — no `.env` exists, so the `env_file:` path is missing |
| `broken-networks` | synthetic | `NET001` (`backend` is not a contract network), `NET002` (`databases` used, not declared), `NET003` (`home` declared without `external: true`) |
| `broken-paths` | synthetic | `PATH001` (host path nothing creates), `PATH003` (`..` escape out of the storage roots) |
| `env-drift` | synthetic | `ENV005` (`GREETING` in `example.env`, absent from `.env`), `ENV006` (`UNDOCUMENTED_KEY` only in `.env`), `ENV010` (`SERVICE_NAME` ≠ directory name) |
| `broken-setup` | synthetic | `SETUP002` (`recursive_chown` is not a registered primitive), `SETUP003` (requires `db-postgres`, which is `Broken`) |
| `bad-yaml` | synthetic | `COMPOSE002` — not parseable as YAML |
| `legacy-name` | synthetic | `COMPOSE004` — named `compose.yml`, contract requires `compose.yaml` |

`GIT001` (a real env file tracked by git) cannot be expressed in a plain directory. It is produced by
`scripts/init-fixture-repo.sh`, which force-adds `compose/env-drift/.env` past the gitignore.

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
