# Roadmap

Status values: `not started` · `in progress` · `done` · `blocked`

Update the status column as work lands. Each milestone has a file in [milestones/](milestones/) written
as a GitHub issue body — create the issue from it and record the number in the Issue column.

## Preliminaries

These are not milestones. They are the setup that makes the milestones resumable.

| Part | What | Status |
|---|---|---|
| Part 0 | Land the plan in `docs/` | done |
| Part 1 | Correct the stale on-server layout spec | done |

## Milestones

| ID | Goal | Depends on | Status | Issue |
|---|---|---|---|---|
| [M0](milestones/m0-skeleton.md) | Cargo workspace, justfile, CI, fixture repo, SvelteKit shell, health endpoint | — | done (except the container image, deliberately deferred to M3 — see its notes) | _(unset)_ |
| [M1](milestones/m1-validator.md) | **MVP.** Read-only validator: parse every stack, resolve both env scopes, report findings in the UI. Auth ships here | M0 | not started | _(unset)_ |
| [M2](milestones/m2-app-setup.md) | App setup: `setup.toml` interpreter, `.env` editor, `$APPCONFIG_STORAGE` trees and secret files | M1 | not started | _(unset)_ |
| [M3](milestones/m3-truenas-client-bootstrap.md) | `TrueNasApi` trait + websocket client + fake; datasets, users, groups, ACLs, networks; bootstrap script and hand-install guide | M1 | not started | _(unset)_ |
| [M4](milestones/m4-deploy.md) | Deploy a stack via the TrueNAS middleware API, with a paste-able manifest fallback; overlay-based compose customization | M2, M3 | not started | _(unset)_ |
| [M5](milestones/m5-catalog.md) | App catalog: browse `truenas-orchestrator-apps`, vendor an app into the homelab repo with provenance, branch-per-change git writes | M2 | not started | _(unset)_ |
| [M6](milestones/m6-lifecycle.md) | Image update checks, auto-update schedules, deploy history and rollback | M4 | not started | _(unset)_ |

## Dependency shape

```
M0 ──> M1 ──┬──> M2 ──┬──> M4 ──> M6
            │         │
            └──> M3 ──┘
                      │
                 M2 ──┴──> M5
```

M2 and M3 are independent of each other and can be worked in either order once M1 lands. M4 needs both.
