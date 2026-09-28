# M4 — Deploy

**Status:** not started · **Depends on:** M2, M3 · **Issue:** _(unset)_

## Goal

Deploy a stack to TrueNAS from the UI. Render the app manifest from a stack plus its resolved deploy env,
push it via the middleware API, and show job status and logs. A copy-able paste-able manifest is always
rendered alongside, so the manual TrueNAS Custom App route stays available when the API path fails.

Per-deployment customization — Traefik exposure, resource limits, GPU — is applied as a generated overlay
file. `compose.yaml` is never rewritten.

## Out of scope

- The catalog (M5)
- Update checking, schedules and rollback (M6)

## Tasks

- [ ] Manifest renderer: stack plus resolved deploy env plus overlay → TrueNAS app definition
- [ ] The deploy env is supplied as compose's **interpolation** environment, per [architecture/env-scopes.md](../architecture/env-scopes.md). This is the point of the whole design; get it right and assert it
- [ ] `HOMELAB_STORAGE` injected into the deploy env from orchestrator config
- [ ] Deploy via `app.create` / `app.update`; detect which applies
- [ ] Paste-able manifest rendered on every deploy view, always, including when the API path succeeds
- [ ] Overlay generation into `compose.override.yaml`: Traefik exposure toggle, resource limits, GPU passthrough
- [ ] `compose.yaml` is opened read-only during customization; a test asserts it is byte-identical afterwards
- [ ] Pre-deploy gate: refuse to deploy a stack that is not `Ready`
- [ ] Deploy as a job with SSE progress and container logs
- [ ] `provision.postgres` setup primitive — create a database and role in a running `db-postgres` stack
- [ ] Deploy recorded in the audit log, with the manifest retained for diffing
- [ ] Frontend: deploy view with customization toggles, rendered manifest, deploy button, job log, and the paste fallback

## Acceptance criteria

1. `db-client` deploys from the UI, appears in the TrueNAS Apps UI, and remains manageable there.
2. The paste-able manifest, applied by hand through the TrueNAS Custom App UI, produces an identical app to the API path.
3. Interpolated values — `${SERVICE_NAME}`, `${DOMAIN_NAME}`, every storage root — are correct in the deployed container's name, labels and mounts. No empty-string substitutions.
4. Customization writes only `compose.override.yaml`; `compose.yaml` is byte-identical before and after, comments intact. Asserted by test.
5. A stack that is not `Ready` cannot be deployed.
6. A middleware failure surfaces a clear error and the paste fallback, rather than a partial deploy.
7. `provision.postgres` is idempotent and does not overwrite an existing role's password without an explicit confirm.

## Verification

```
just test
just dev
```

Against real TrueNAS, starting with the lowest-risk stack:

- Deploy `db-client`. Confirm it appears in the TrueNAS Apps UI.
- `docker inspect` the container; confirm `container_name`, mounts and Traefik labels have fully substituted values.
- Delete it, then create it by hand from the rendered paste manifest; `docker inspect` again and diff against the first result.
- Toggle a customization option; `git diff` the homelab repo and confirm only `compose.override.yaml` changed.
- Attempt to deploy a `Broken` stack; confirm refusal.

## Notes / decisions made during implementation

_(append as work happens)_
