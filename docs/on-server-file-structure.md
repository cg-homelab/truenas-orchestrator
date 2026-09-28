# On-server file structure

The on-server paths and storage variables this project depends on.

Normative repository layout lives in
[architecture/repo-layout-contract.md](architecture/repo-layout-contract.md). Environment variable
resolution — which is subtle and easy to get wrong — lives in
[architecture/env-scopes.md](architecture/env-scopes.md).

## Storage environment variables

Defined in `compose/example.global.env` and copied to `compose/.global.env`:

| Variable | Meaning |
|---|---|
| `HOMELAB_STORAGE` | Path to the homelab git checkout on this server |
| `APPCONFIG_STORAGE` | Root directory for per-app config directories |
| `DATABASE_STORAGE` | Root directory for database data directories |
| `MEDIA_STORAGE` | Root directory for media |

`APPCONFIG_STORAGE` is the correct name. `APPDATA_STORAGE` appears in some existing compose files and in
`authelia/setup.sh`; it is defined nowhere and is a defect (`ENV001`).

**`HOMELAB_STORAGE` is special.** Compose files reference their env files as
`${HOMELAB_STORAGE}/compose/.global.env`, so the variable is needed in order to *locate* `.global.env` and
cannot be read from it. The orchestrator injects it into the deploy environment from its own
configuration. `.global.env` may restate it for humans, but that is never the source of truth. See
[architecture/env-scopes.md](architecture/env-scopes.md).

## Repository structure

```
$HOMELAB_STORAGE/                   # git repository on the server; source of truth
├── compose/                        # all compose stacks
│   ├── example.global.env          # committed template
│   ├── .global.env                 # real global env — gitignored
│   ├── <app>/
│   │   ├── compose.yaml            # required
│   │   ├── example.env             # committed template
│   │   ├── .env                    # real app env — gitignored
│   │   ├── setup.toml              # optional; declarative setup
│   │   ├── config/                 # optional; templates copied into $APPCONFIG_STORAGE/<app>/
│   │   └── README.md               # optional
│   └── ...
└── ansible/                        # ansible playbooks — not relevant to this project
```

```
$APPCONFIG_STORAGE/                 # per-app config and secret directories, outside git
└── <app>/
    ├── config/                     # populated from the app's config/ templates
    └── secrets/                    # generated secret files, mode 0600
```

```
$DATABASE_STORAGE/                  # database data directories
└── <app>/
```

Real env files (`.global.env`, `<app>/.env`) are dot-prefixed and gitignored. Their committed
counterparts are `example.global.env` and `example.env`.

An older layout used `stacks/` with `stack.env`, per-service `env/<service>.env` and `compose.yml`. It is
legacy and unsupported.

## Docker networks

Four external networks, created during bootstrap:

| Network | Purpose |
|---|---|
| `home` | General LAN-facing services |
| `proxy` | Anything Traefik routes to |
| `isolated` | No egress |
| `databases` | Database servers and their clients |

## Orchestrator state

The orchestrator's own SQLite database lives on a dedicated dataset, separate from the homelab repo, so it
survives container replacement and can never be committed by accident.
