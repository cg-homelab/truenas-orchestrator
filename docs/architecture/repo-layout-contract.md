# Homelab repo layout contract

Normative specification of the repository the orchestrator manages. The validator implements this
document; if the two disagree, one of them is a bug.

Reference implementation in progress: `martin-homelab/compose/`.

## Layout

```
<homelab repo>/
├── compose/
│   ├── example.global.env          # committed template for the global env
│   ├── .global.env                 # real global env — GITIGNORED, created from the template
│   ├── <app>/
│   │   ├── compose.yaml            # required
│   │   ├── example.env             # committed template for this app's env
│   │   ├── .env                    # real app env — GITIGNORED, created from the template
│   │   ├── setup.toml              # optional; declarative setup (see setup-toml.md)
│   │   ├── config/                 # optional; config file templates copied to $APPCONFIG_STORAGE
│   │   └── README.md               # optional; human notes
│   └── ...
└── ansible/                        # out of scope for this project
```

The app directory name is the stack identity. It must match `[a-z0-9][a-z0-9-]*`.

`stacks/` in an older repo is the **legacy** layout and is not supported. It contained `stack.env`,
per-service `env/<service>.env`, and `compose.yml`. Do not write code against it.

## File naming rules

| Kind | Committed template | Real file (gitignored) |
|---|---|---|
| Global env | `compose/example.global.env` | `compose/.global.env` |
| App env | `compose/<app>/example.env` | `compose/<app>/.env` |

The real files are dot-prefixed. This is deliberate: it makes the gitignore rule (`.env`, `.global.env`)
short and hard to get wrong, and makes the real files visually distinct from templates in a listing.

The compose file is `compose.yaml`, not `compose.yml`.

## Required `.gitignore` entries in the homelab repo

```
compose/.global.env
compose/*/.env
```

The validator reports `GIT001` if a real env file is tracked by git.

## Environment variables

See [env-scopes.md](env-scopes.md) — required reading. In summary: `.global.env` and `<app>/.env` are
merged (app wins) to form the **deploy env**, which the orchestrator supplies as compose's interpolation
environment. The same files are also listed under `env_file:` for container scope.

`HOMELAB_STORAGE` is injected by the orchestrator and is not sourced from `.global.env`.

### Conventional global variables

Defined in `example.global.env`:

| Variable | Meaning |
|---|---|
| `TZ` | Timezone |
| `DOMAIN_NAME` | Base domain for Traefik routing |
| `PUID` / `PGID` | Owner uid/gid for app data |
| `HOMELAB_STORAGE` | Path to the homelab git checkout on the server |
| `APPCONFIG_STORAGE` | Root for per-app config directories |
| `DATABASE_STORAGE` | Root for database data directories |
| `MEDIA_STORAGE` | Root for media |

**`APPCONFIG_STORAGE` is the correct name.** `APPDATA_STORAGE` appears in some existing compose files and
is a defect (`ENV001`) — it is defined nowhere.

### Conventional per-app variables

| Variable | Meaning |
|---|---|
| `SERVICE_NAME` | The container name and the Traefik host label. Must equal the app directory name |
| `HTTP_PORT` | Host port when the app is exposed directly rather than via Traefik |

`PORT` is not the convention; `db-prometheus/example.env` uses it and is a defect.

## Networks

Exactly four external networks exist, created during bootstrap:

| Network | Purpose |
|---|---|
| `home` | General LAN-facing services |
| `proxy` | Anything Traefik routes to |
| `isolated` | No egress; services that must not reach the network |
| `databases` | Database servers and their clients |

Every compose file must declare the networks it uses as `external: true`. Referencing a network outside
this set is `NET001`.

## Compose file conventions

- `container_name: "${SERVICE_NAME}"`
- `env_file:` lists `${HOMELAB_STORAGE}/compose/.global.env` then `${HOMELAB_STORAGE}/compose/<app>/.env`
- Host paths in `volumes:` and `secrets: file:` use a storage variable, never a literal `/mnt/...` path.
  A literal path under a known storage root is `PATH002`.
- Resource limits under `deploy.resources.limits` are expected on every service.
- Traefik exposure is expressed with labels templated on `${SERVICE_NAME}` and `${DOMAIN_NAME}`.

Comments in these files are load-bearing (`# Comment out: if not public access`). The orchestrator must
never rewrite `compose.yaml` — customization is applied via a generated `compose.override.yaml` (see
[decisions.md](../decisions.md) D14).

## App setup

An app that needs directories, generated secrets, config files copied into `$APPCONFIG_STORAGE`, or
values prompted from the user declares them in `setup.toml`. See [setup-toml.md](setup-toml.md).

A `setup.sh` is the legacy mechanism, is never executed, and is reported as `SETUP001`.
