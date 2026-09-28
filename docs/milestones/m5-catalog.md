# M5 — App catalog

**Status:** not started · **Depends on:** M2 · **Issue:** _(unset)_

## Goal

Add a new app to the homelab from a centralized catalog. Browse `truenas-orchestrator-apps`, pick an app,
and have its `compose.yaml`, `example.env`, `config/` and `setup.toml` vendored into the homelab repo on a
branch, with the source and version recorded so upstream drift can be reported later.

## Out of scope

- Automatic merging of upstream changes. Drift is reported; the user decides (see [decisions.md](../decisions.md) D7)
- Publishing to the catalog

## Tasks

- [ ] Catalog repo layout spec, documented in `docs/architecture/`
- [ ] Catalog fetch and cache; it is read-only and never written to
- [ ] Catalog browse and search API
- [ ] Vendor an app: copy `compose.yaml`, `example.env`, `config/`, `setup.toml` into `compose/<app>/`
- [ ] Record provenance in SQLite: catalog source, app, version, commit, vendored-at
- [ ] Validate the vendored app immediately; surface findings before the branch is offered
- [ ] Git writes on a branch per change, with a diff surfaced in the UI. Never commit to the default branch
- [ ] Never vendor a real `.env`; only `example.env`
- [ ] Drift detection: compare the vendored copy against its recorded upstream version and report differences
- [ ] Remove an app: delete the directory on a branch; warn about `$APPCONFIG_STORAGE` data left behind and never delete it implicitly
- [ ] Frontend: catalog browser, app detail, add-app flow with diff preview, drift indicator on stack detail

## Acceptance criteria

1. Adding an app from the catalog produces a branch containing exactly the vendored files and nothing else.
2. The default branch is never written to.
3. Provenance is recorded and shown on the stack detail view.
4. A vendored app that the user then edits is reported as drifted from upstream, with a diff.
5. A newer upstream version is reported, with a diff, and nothing is merged automatically.
6. A vendored app validates immediately; if it has findings they are shown before the branch is offered.
7. No `.env` file is ever vendored or committed.
8. Removing an app never deletes anything under `$APPCONFIG_STORAGE`.

## Verification

```
just test
just dev
```

Manual:

- Add an app from a fixture catalog; `git log` and `git diff` the branch and confirm the contents.
- Confirm the default branch is unchanged.
- Edit a vendored file; confirm the drift indicator and diff appear.
- Bump the fixture catalog's version; confirm the update is reported and nothing merged.
- Remove an app; confirm `$APPCONFIG_STORAGE` is untouched and the warning was shown.

## Notes / decisions made during implementation

_(append as work happens)_
