# `setup.toml` — declarative app setup

Replaces the legacy per-app `setup.sh`. The backend **interprets** this file; it never executes scripts
from the homelab repo. See [decisions.md](../decisions.md) D6 for why.

Every construct below is a named backend capability. Adding a new one is a deliberate code change with a
review, not a shell line in a git repo running as root.

## Full example

Ported from `compose/authelia/setup.sh`, which is the most demanding real case.

```toml
[app]
name = "authelia"
requires = ["db-postgres"]          # stacks that must be Ready before setup can run

[[dirs]]
path = "${APPCONFIG_STORAGE}/authelia/config"
mode = "0750"

[[dirs]]
path = "${APPCONFIG_STORAGE}/authelia/secrets"
mode = "0700"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/JWT_SECRET"
generate = { bytes = 32, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/SESSION_SECRET"
generate = { bytes = 64, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/STORAGE_ENCRYPTION_KEY"
generate = { bytes = 64, encoding = "base64" }
mode = "0600"

[[secrets]]
file = "${APPCONFIG_STORAGE}/authelia/secrets/STORAGE_PASSWORD"
value_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"   # same value the DB user is created with
mode = "0600"

[[config_files]]
from = "config/configuration.yml"                    # path within the app dir, committed
to   = "${APPCONFIG_STORAGE}/authelia/config/configuration.yml"
on_exists = "keep"                                   # keep | overwrite | prompt

[[config_files]]
to = "${APPCONFIG_STORAGE}/authelia/config/users.yml"
create_empty = true
on_exists = "keep"

[[prompts]]
key = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
label = "Authelia database password"
secret = true
generate = { bytes = 24, encoding = "base64" }       # offer a generated default

[[provision.postgres]]                               # M4+, not implemented in M2
stack = "db-postgres"
database = "authelia"
user = "authelia"
password_from = "AUTHELIA_STORAGE_POSTGRES_PASSWORD"
```

## Primitive registry

| Primitive | Milestone | What it does |
|---|---|---|
| `app.name` | M2 | Must equal the app directory name |
| `app.requires` | M2 | Other stacks that must validate as Ready before setup runs |
| `dirs` | M2 | Create a directory with an explicit mode. Idempotent |
| `secrets` | M2 | Create a secret file, either generated or taken from a prompted value. Never overwritten once present |
| `config_files` | M2 | Copy a committed template into `$APPCONFIG_STORAGE`, or create it empty |
| `prompts` | M2 | Ask the user for a value, write it to `<app>/.env`. May offer a generated default |
| `env_validators` | M2 | Per-key value constraints used by validator check `ENV007` |
| `provision.postgres` | M4 | Create a database and role in a running `db-postgres` stack |

Nothing outside this table is valid. An unknown key is a hard parse error, not a warning — silently
ignoring setup steps is how apps end up half-configured.

## Value validators

Used by check `ENV007` so that value-level defects are caught, not just missing keys.

```toml
[env_validators]
METRICS_RETENTION_TIME = { type = "duration" }      # rejects bare "60"; wants "60d"
HTTP_PORT              = { type = "port" }
SERVICE_NAME           = { type = "identifier", equals_app_name = true }
POSTGRES_PASSWORD      = { type = "non_empty", not_one_of = ["REPLACE_THIS"] }
APPCONFIG_STORAGE      = { type = "absolute_path" }
```

Types: `duration`, `port`, `identifier`, `absolute_path`, `non_empty`, `integer`, `bool`, `one_of`.

## Execution rules

These are requirements on the interpreter, not suggestions.

1. **Dry run first.** Every setup run produces a plan — the exact directories, files and env keys it will
   create or change — and the plan is shown before anything is written.
2. **Idempotent.** Running setup twice must produce no changes the second time. This is asserted in tests.
3. **Never overwrite a secret.** If a secret file exists, it is kept and reported as already present.
   Regeneration is a separate, explicit, confirmed action.
4. **Interpolation uses the deploy env.** `${...}` in `setup.toml` resolves from the same merged deploy
   env as compose interpolation (see [env-scopes.md](env-scopes.md)). An unresolved variable aborts the
   run before any write.
5. **Paths are confined.** Every resolved write path must fall under `$APPCONFIG_STORAGE`,
   `$DATABASE_STORAGE` or the app's own directory. A path escaping those roots — including via `..` — is
   a hard error.
6. **Secrets never hit the log.** Generated and prompted values are redacted in logs, job output and the
   audit trail. The audit records that a secret was written, never its value.
7. **`requires` is checked, not assumed.** Setup refuses to run if a required stack is not Ready.

## Porting from `setup.sh`

Existing `setup.sh` files are not executed and are reported as `SETUP001`. Port them by hand. The
authelia script is the worked example above; note that porting it fixes four latent bugs in it (the stray
`.` in the path, secrets written to a relative directory, `STORAGE_PASSWORD` never generated, and an
unquoted password interpolated into SQL).
