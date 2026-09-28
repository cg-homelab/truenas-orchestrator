# M3 — TrueNAS client and bootstrap

**Status:** not started · **Depends on:** M1 · **Issue:** _(unset)_

## Goal

The orchestrator can talk to TrueNAS and prepare a server from nothing: datasets, the four docker
networks, the git user and groups, and permissions. Two supported routes to the same end state — a
bootstrap script and a hand-install guide.

Everything is developed and tested against a fake; the real middleware is touched only by an explicit
smoke recipe.

## Out of scope

- Deploying apps (M4)
- The orchestrator managing its own TrueNAS app entry — it never does (see [decisions.md](../decisions.md) D11)

## Tasks

- [ ] `TrueNasApi` trait covering: dataset create/list, user and group create/list, ACL get/set, docker network create/list, app list, system version
- [ ] Websocket JSON-RPC implementation against the TrueNAS middleware, with API-key auth
- [ ] `FakeTrueNas` backed by `fixtures/truenas/` recorded responses, including failure cases
- [ ] Middleware API version detection with a clear error when the version is outside the supported range
- [ ] Dataset creation with the properties the homelab needs
- [ ] User and group creation, including the git user
- [ ] ACL / permission application for `$HOMELAB_STORAGE`, `$APPCONFIG_STORAGE`, `$DATABASE_STORAGE`
- [ ] Creation of the four networks: `home`, `proxy`, `isolated`, `databases`
- [ ] Git credential setup — SSH deploy key generation and display of the public half
- [ ] Environment-readiness check: one function reporting what exists, what is missing, and what is wrong
- [ ] Dry-run preview and explicit confirm on every destructive or creating operation
- [ ] Audit log entries for every TrueNAS mutation
- [ ] API key storage through the secret-storage trait; never logged, never returned by the API
- [ ] `deploy/bootstrap.sh` — first run end to end
- [ ] `docs/` hand-install guide covering the identical steps
- [ ] `just smoke HOST` — read-only calls against a real TrueNAS to verify API compatibility
- [ ] Frontend: environment-readiness view, first-run wizard, API key entry

## Acceptance criteria

1. Every client test passes against `FakeTrueNas` with no network.
2. `just smoke <host>` against the real TrueNAS confirms the client matches the live middleware API, and fails loudly with an actionable message if it does not.
3. `bootstrap.sh` and the hand-install guide produce end states that both pass the environment-readiness check, with no differences.
4. Re-running bootstrap on an already-configured server changes nothing.
5. Every dataset, user, group, ACL and network operation shows a preview and requires confirmation.
6. The TrueNAS API key never appears in a log, an API response, or the audit trail.
7. A middleware version outside the supported range produces a clear error naming the detected and supported versions, not a deserialization failure.
8. The orchestrator refuses to modify its own TrueNAS app entry.

## Verification

```
just test                 # all against the fake
just smoke <truenas-host> # read-only, real server
just dev                  # walk the first-run wizard against the fake
```

Manual:

- Run `bootstrap.sh` against the fake in a container; capture the resulting state.
- Follow the hand-install guide against a fresh fake; capture the state; diff the two. They must match.
- Run bootstrap twice; assert the second run reports no changes.
- Attempt an operation against the orchestrator's own app entry; assert refusal.

## Notes / decisions made during implementation

_(append as work happens)_
