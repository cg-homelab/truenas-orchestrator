# truenas-homelab-orchestrator

Opinionated homelab orchestration tool for TrueNAS.

It runs as a TrueNAS SCALE app and manages a git-backed homelab repository full of Docker Compose
stacks — validating them, setting them up, and deploying them.

> **Status: planning.** No code yet. Start at **[docs/README.md](docs/README.md)**, then
> [docs/roadmap.md](docs/roadmap.md).

## The problem

A homelab repo of compose stacks accumulates drift that nothing catches: environment variables referenced
but never defined, variables defined in a scope where Compose will never substitute them, hardcoded
absolute paths that should be variables, and per-app `setup.sh` scripts that silently half-succeed.

The first milestone is a read-only validator that catches exactly that. Everything after it builds on the
same model.

## Project goal

### In-repo guide

Document the initial setup needed to start using this tool on TrueNAS:

- Users, groups, permissions
- Datasets
- TrueNAS API access

### On the TrueNAS server

Backend and frontend run on TrueNAS as a TrueNAS app.

**Frontend** — GUI for the backend.

**Backend**

- Add the git user used for managing the homelab repo
- Hold the configuration for this deployment of the orchestrator
- Create and manage app secrets for the frontend and external frontend
- Manage TrueNAS
  - Create and manage datasets
  - Create and manage dataset permissions for this homelab
- Orchestrate the homelab from the git repo and the homelab dataset
  - Set up the required docker networks: `home`, `proxy`, `isolated`, `databases`
  - Create or display information about the homelab dataset
  - Add an existing or create a new git repository as the source of truth
  - Periodically fetch and pull
  - Extend the homelab repo with new apps from the centralized `truenas-orchestrator-apps` catalog
  - Set up an app
    - Create and manage its env file
    - Show whether the app is set up and deployable, or needs setup
  - Deploy apps on TrueNAS from the homelab repo
    - Customize compose files with options such as Traefik exposure, resource limits and GPU
      acceleration — applied as overlay files, never by rewriting `compose.yaml`
    - Render a TrueNAS deployment manifest that can also be pasted into the TrueNAS Apps page by hand,
      as a fallback to the API path
  - Check docker images for updates; set an auto-update schedule or update manually

### External client

Run the frontend locally against the backend running on the TrueNAS server.

## Documentation

| Document | What it is |
|---|---|
| [docs/README.md](docs/README.md) | Index, current state, and how to resume |
| [docs/roadmap.md](docs/roadmap.md) | Milestones with live status |
| [docs/decisions.md](docs/decisions.md) | Settled architectural decisions and why |
| [docs/validator-checks.md](docs/validator-checks.md) | Validator finding codes |
| [docs/architecture/](docs/architecture/) | Env scopes, repo layout contract, `setup.toml`, components |

## License

See [LICENSE](LICENSE).
