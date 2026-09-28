# truenas-orchestrator — documentation

Opinionated homelab orchestration tool for TrueNAS SCALE. It runs as a TrueNAS app and manages a
git-backed homelab repo full of Docker Compose stacks.

## Current state and how to resume

**Status:** planning complete, no code written.
**Next up:** M0 — skeleton (see [roadmap](roadmap.md)).

To pick this work up cold, read in this order:

1. [roadmap.md](roadmap.md) — milestone table with live status. Start at the first row that is not `done`.
2. That milestone's file in [milestones/](milestones/) — it is written as a GitHub issue body:
   goal, out-of-scope, task checkboxes, acceptance criteria, verification commands, and a running
   Notes section with decisions made during implementation.
3. [decisions.md](decisions.md) — every settled architectural decision and why. Do not relitigate these
   without saying so explicitly.
4. [architecture/env-scopes.md](architecture/env-scopes.md) — the single most important concept in this
   project. Read it before touching anything that resolves environment variables.

When you finish a chunk of work: tick the task boxes, append to that milestone's Notes section, and
update the status column in `roadmap.md`. That is the handoff surface.

## Document index

| Document | What it is |
|---|---|
| [roadmap.md](roadmap.md) | Milestone table: id, goal, status, dependencies, issue link |
| [decisions.md](decisions.md) | ADR log — settled decisions with rationale and alternatives rejected |
| [validator-checks.md](validator-checks.md) | Registry of validator finding codes. Codes are stable and must not be renumbered |
| [architecture/env-scopes.md](architecture/env-scopes.md) | Compose interpolation scope vs container scope; the `HOMELAB_STORAGE` injection rule |
| [architecture/repo-layout-contract.md](architecture/repo-layout-contract.md) | Normative layout of a homelab repo — what the validator enforces |
| [architecture/setup-toml.md](architecture/setup-toml.md) | `setup.toml` schema and the registry of setup primitives |
| [architecture/components.md](architecture/components.md) | Crate and frontend layout, and why the boundaries sit where they do |
| [on-server-file-structure.md](on-server-file-structure.md) | On-server paths and storage environment variables |
| [milestones/](milestones/) | One issue-ready file per milestone |

## What this tool is for

The homelab repo it manages (`martin-homelab`) accumulates configuration drift that nothing currently
catches: environment variables referenced but never defined, variables defined in the wrong scope to
ever be substituted, hardcoded absolute paths that should be variables, and per-app `setup.sh` scripts
that silently half-succeed.

Milestone 1 is a read-only validator that catches exactly that class of error. Later milestones grow it
into app setup, TrueNAS dataset and permission management, deploy, and an app catalog.
