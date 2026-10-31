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
| [M0](milestones/m0-skeleton.md) | Go module, justfile, lint, CI, fixture repo, SvelteKit shell, health endpoint | — | done | _(unset)_ |
| [M1](milestones/m1-validator.md) | **MVP.** Validator engine + `orchestrator validate` CLI: parse every stack, resolve both env scopes, report findings. No HTTP | M0 | done | _(unset)_ |
| [M2](milestones/m2-validator-server.md) | Validator server, auth, and the validator UI | M1 | done | _(unset)_ |
| [M3](milestones/m3-app-setup.md) | App setup: `setup.toml` interpreter, `.env` editor, `$APPCONFIG_STORAGE` trees and secret files | M2 | done | _(unset)_ |
| [M4](milestones/m4-truenas-client-bootstrap.md) | `homelab.TrueNAS` interface + websocket client + fake; datasets, users, groups, ACLs, networks; bootstrap script and hand-install guide | M2 | done | _(unset)_ |
| [M5](milestones/m5-deploy.md) | Deploy a stack via the TrueNAS middleware API, with a paste-able manifest fallback; overlay-based compose customization | M3, M4 | not started | _(unset)_ |
| [M6](milestones/m6-catalog.md) | App catalog: browse `truenas-orchestrator-apps`, vendor an app into the homelab repo with provenance, branch-per-change git writes | M3 | not started | _(unset)_ |
| [M7](milestones/m7-lifecycle.md) | Image update checks, auto-update schedules, deploy history and rollback | M5 | not started | _(unset)_ |

## Dependency shape

```
M0 ──> M1 ──> M2 ──┬──> M3 ──┬──> M5 ──> M7
                   │         │
                   └──> M4 ──┘
                             │
                        M3 ──┴──> M6
```

M3 and M4 are independent of each other and can be worked in either order once M2 lands. M5 needs both.
