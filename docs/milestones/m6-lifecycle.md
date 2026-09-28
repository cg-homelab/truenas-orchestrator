# M6 — Lifecycle

**Status:** not started · **Depends on:** M4 · **Issue:** _(unset)_

## Goal

Keep deployed apps current and recoverable. Check container images for updates, optionally on a schedule,
and keep enough deploy history to roll back a bad one.

## Out of scope

- Automatically applying updates without an explicit opt-in per app
- Rolling back anything outside the orchestrator's own deploy history

## Tasks

- [ ] Image update check: compare the deployed image digest against the registry for each service
- [ ] Support pinned tags, floating tags and `latest`, and report which an app uses
- [ ] Schedules stored in SQLite; a scheduler that survives restarts and does not double-fire
- [ ] Per-app auto-update opt-in, default off
- [ ] Periodic `git fetch` on a schedule, with the repo status surfaced
- [ ] Deploy history: retain the rendered manifest and resolved env (secrets redacted) per deploy
- [ ] Rollback: redeploy a previous manifest, with a diff preview first
- [ ] Notify on available updates and on failed scheduled runs
- [ ] Frontend: updates view, schedule configuration, deploy history with diff and rollback

## Acceptance criteria

1. An app running an outdated image is reported, naming the current and available versions.
2. An app pinned to a specific digest is never reported as outdated by a floating tag.
3. Schedules survive a container restart and do not fire twice for one due time.
4. Auto-update is off by default and requires an explicit per-app opt-in.
5. Deploy history retains manifests with secret values redacted.
6. Rollback shows a diff against the current deploy and requires confirmation.
7. A failed scheduled run is visible in the UI and does not silently retry forever.

## Verification

```
just test
just dev
```

Manual:

- Deploy a stack on an older tag; confirm the update check reports it.
- Pin to a digest; confirm no false positive.
- Set a schedule, restart the container, confirm the schedule persists and fires once.
- Roll back a deploy; confirm the diff preview and that the previous manifest is restored.
- Confirm deploy history contains no secret values.

## Notes / decisions made during implementation

_(append as work happens)_
