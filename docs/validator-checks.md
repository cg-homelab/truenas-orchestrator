# Validator checks

Registry of every finding the validator can emit. **Codes are stable and must never be renumbered or
reused.** They appear in the UI, in CLI output, and in user bug reports. Retire a check by marking it
deprecated; do not recycle its code.

Severity:

- **error** — the stack cannot work. Status becomes `Broken`
- **warn** — the stack may work but is wrong or unportable. Status becomes `NeedsSetup` or stays `Ready`
  with warnings, depending on the check
- **info** — informational only

Every finding carries: code, severity, file, line, message, and a suggested fix.

## Stack status

| Status | Meaning |
|---|---|
| `Ready` | No errors. Deployable |
| `NeedsSetup` | No errors, but setup steps are outstanding (missing `.env`, unrun `setup.toml`) |
| `Broken` | One or more errors |

---

## COMPOSE — compose file structure

| Code | Severity | Detects | Real example | Suggested fix |
|---|---|---|---|---|
| `COMPOSE001` | error | `compose.yaml` is not structurally valid per the Compose spec | `compose/db-postgres/compose.yaml` — `networks: databases` is a bare string where a sequence or mapping is required | Change to `networks:` then `- databases` |
| `COMPOSE002` | error | File is not parseable YAML | — | Fix the YAML syntax |
| `COMPOSE003` | warn | A service has no `deploy.resources.limits` | — | Add cpu and memory limits |
| `COMPOSE004` | warn | File is named `compose.yml`, contract requires `compose.yaml` | legacy `stacks/` layout | Rename to `compose.yaml` |

## ENV — environment variables

| Code | Severity | Detects | Real example | Suggested fix |
|---|---|---|---|---|
| `ENV001` | error | A `${VAR}` in **interpolation** position resolves from nothing in the deploy env | `compose/db-prometheus/compose.yaml` uses `${APPDATA_STORAGE}`; only `APPCONFIG_STORAGE` is defined. Also `${HTTP_PORT}`, where `example.env` defines `PORT` | Define the variable, or correct the reference to the defined name |
| `ENV002` | error | A `${VAR}` in **container** position resolves from nothing in the `env_file:` chain | — | Add the key to `.global.env` or `<app>/.env` |
| `ENV003` | error | A variable is available only in container scope but is referenced in an interpolation position. **The highest-value check** — see [architecture/env-scopes.md](architecture/env-scopes.md) | `${DATABASE_STORAGE}` in a `volumes:` entry, supplied only via `env_file:` | Ensure the orchestrator supplies it in the deploy env; do not rely on `env_file:` for interpolation |
| `ENV004` | error | An `env_file:` path does not exist after interpolation | `.env` not yet created from `example.env` | Run app setup, or create the file |
| `ENV005` | warn | A key present in `example.env` is missing from `.env` | — | Add the key; the template defines the expected contract |
| `ENV006` | info | A key present in `.env` is absent from `example.env` | — | Add it to the template so other deployments know it exists |
| `ENV007` | error | A value fails a validator declared in `setup.toml` `[env_validators]` | `METRICS_RETENTION_TIME=60` fails `type = "duration"`; Prometheus rejects `--storage.tsdb.retention.time=60` | Use a unit, e.g. `60d` |
| `ENV008` | warn | A key is defined in `example.env` or `.env` but referenced nowhere in `compose.yaml` | `PORT` in `compose/db-prometheus/example.env` | Remove it, or correct the compose reference |
| `ENV009` | error | A placeholder value was never replaced | `POSTGRES_PASSWORD=REPLACE_THIS` in `compose/db-postgres/example.env`, once copied to `.env` | Set a real value |
| `ENV010` | warn | `SERVICE_NAME` does not equal the app directory name | — | Make them match; Traefik labels and `container_name` depend on it |

## PATH — filesystem paths

| Code | Severity | Detects | Real example | Suggested fix |
|---|---|---|---|---|
| `PATH001` | warn | A host path in `volumes:` or `secrets: file:` does not exist. Downgraded to info when `setup.toml` declares that it will be created | — | Run app setup, or declare it in `setup.toml` |
| `PATH002` | warn | A literal absolute path under a known storage root, where a `${VAR}` belongs | `compose/authelia/compose.yaml` hardcodes `/mnt/fast/appdata/...` in both `secrets:` and `volumes:` | Replace with `${APPCONFIG_STORAGE}/...` |
| `PATH003` | error | A resolved path escapes the permitted storage roots, including via `..` | — | Correct the path |

## NET — networks

| Code | Severity | Detects | Suggested fix |
|---|---|---|---|
| `NET001` | error | A referenced network is not one of `home`, `proxy`, `isolated`, `databases` | Use one of the four, or add it to the contract deliberately |
| `NET002` | error | A network is used by a service but not declared in the top-level `networks:` block | Declare it as `external: true` |
| `NET003` | warn | A declared network is not `external: true` | Mark it external; these networks are created during bootstrap |

## SETUP — app setup

| Code | Severity | Detects | Real example | Suggested fix |
|---|---|---|---|---|
| `SETUP001` | warn | App ships a legacy `setup.sh`, which is never executed | `compose/authelia/setup.sh` | Port it to `setup.toml` — see [architecture/setup-toml.md](architecture/setup-toml.md) |
| `SETUP002` | error | `setup.toml` contains an unknown key | — | Remove it, or add the primitive to the registry deliberately |
| `SETUP003` | warn | `setup.toml` declares `requires` on a stack that is not `Ready` | — | Set up the required stack first |
| `SETUP004` | warn | Setup is declared but has never been run for this app | — | Run setup |

## GIT — repository hygiene

| Code | Severity | Detects | Suggested fix |
|---|---|---|---|
| `GIT001` | error | A real env file (`.global.env`, `<app>/.env`) is tracked by git | Add the gitignore rule and `git rm --cached` the file. Treat any committed secrets as compromised and rotate them |
| `GIT002` | warn | The repo has uncommitted changes to tracked files | Commit or revert |
| `GIT003` | info | The local branch is behind its remote | Pull |

---

## Not machine-checkable

Recorded so nobody wastes time trying to automate them:

- Prose drift in per-app `README.md` files. `compose/authelia/README.md` refers to a container named
  `db-pg` when the stack is `db-postgres`. Real, but not worth a checker.
