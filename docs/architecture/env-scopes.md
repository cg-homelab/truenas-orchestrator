# Environment variable scopes

**Read this before touching anything that resolves environment variables.** Getting this wrong is the
single largest source of silent breakage in the homelab repo today, and correctly modelling it is the
core value of the validator.

## The two scopes

Docker Compose resolves environment variables in two entirely separate places. They do not share values.

### 1. Interpolation scope — fills `${VAR}` *inside* `compose.yaml`

Applies to every `${VAR}` written in the compose file itself: `volumes:`, `labels:`, `image:`,
`container_name:`, `command:`, `ports:`, `secrets: file:`.

Sources, in Compose's own precedence order:

1. the shell environment of the process running compose
2. the `--env-file` passed to compose (or the project-directory `.env` file)

**It does not include anything from an `env_file:` key.** This is the part that surprises people.

### 2. Container scope — what `env_file:` and `environment:` inject into the running container

Applies only to variables the process inside the container reads. The compose file itself never sees them.

## Why this matters here

Every converted stack in the homelab repo looks like this:

```yaml
    volumes:
      - ${DATABASE_STORAGE}/postgres:/var/lib/postgresql/data   # interpolation scope
    env_file:
      - ${HOMELAB_STORAGE}/compose/.global.env                  # container scope
      - ${HOMELAB_STORAGE}/compose/db-postgres/.env             # container scope
```

`DATABASE_STORAGE` is defined in `.global.env`. But `.global.env` is listed under `env_file:`, which is
container scope. So `${DATABASE_STORAGE}` in the `volumes:` line is **never** substituted from it. Unless
something else supplies it in interpolation scope, Compose substitutes the empty string and the volume
mounts at `/postgres`.

The same applies to `${SERVICE_NAME}` in `container_name:` and in every Traefik label, and to
`${DOMAIN_NAME}` in the router rules.

**Validator check `ENV003` exists exactly for this.** A variable that is available only in container scope
but referenced in an interpolation position is a defect, even though nothing about the file looks wrong.

## How the orchestrator resolves it

At deploy time the orchestrator supplies a **deploy env** to the compose invocation — the merged contents
of `.global.env` and `<app>/.env`, in that order, with `<app>/.env` winning. That deploy env populates
interpolation scope. The `env_file:` entries still populate container scope independently.

So the same values reach both scopes, but by two different mechanisms, and the orchestrator is what makes
the interpolation half happen. That is what `README.md` means by "adds the base env file and path to the
docker compose file".

## The `HOMELAB_STORAGE` bootstrap rule

`HOMELAB_STORAGE` is special and must be handled deliberately.

The `env_file:` paths are written absolutely, as `${HOMELAB_STORAGE}/compose/.global.env`. So
`HOMELAB_STORAGE` is required in order to *locate* `.global.env` — it cannot be read from it. That is a
cycle.

**Rule:** `HOMELAB_STORAGE` is injected by the orchestrator into the deploy environment, from its own
configuration. `.global.env` may also define it for the benefit of humans reading the file and for anyone
running compose by hand, but that definition is never the source of truth and the orchestrator does not
depend on it.

The validator must not report `HOMELAB_STORAGE` as unresolved on the grounds that it is absent from
`.global.env`. It is resolved from orchestrator config, and is reported as such in the UI's resolved-env
table so the provenance is visible.

## Precedence, normatively

For interpolation scope, later wins:

1. `HOMELAB_STORAGE` and any other orchestrator-injected variables
2. `compose/.global.env`
3. `compose/<app>/.env`

For container scope, the `env_file:` list order in the compose file wins, then `environment:` overrides
everything.

## Implementation notes

- `compose-model` extracts variable references tagged with the scope of the position they appear in.
  A single variable can legitimately appear in both scopes.
- Preserve the source line for every reference. Findings must point at a line.
- Support the full Compose interpolation syntax, not just `${VAR}`: `${VAR:-default}`, `${VAR-default}`,
  `${VAR:?err}`, `${VAR?err}` and `$VAR`. A variable with a default is resolved and must not be reported
  as missing.
- `$$` is an escaped literal `$` and is not a variable reference.
- The resolved-env view in the UI shows the two scopes as separate tables, with the provenance of each
  value (injected / `.global.env` / `<app>/.env` / default). Users cannot debug this without seeing which
  scope a value landed in.
