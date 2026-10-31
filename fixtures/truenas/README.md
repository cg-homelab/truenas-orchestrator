# TrueNAS fixtures

Inputs for `internal/truenas`: the wire replies its client tests replay, and the scenarios that seed
`truenas.Fake`. The design is in [docs/milestones/m4-truenas-client-bootstrap.md](../../docs/milestones/m4-truenas-client-bootstrap.md)
(Design, sections 1, 7 and 10).

## `wire/<major.minor>/` — reply envelopes

Each file is one JSON-RPC 2.0 reply without its `id` (the replaying test server fills it in), plus a
`_provenance` string. `api.versions*.json` hold the body of the plain `GET /api/versions` reply under
`http_body` instead. File names are `<method>[.<case>].json`.

Every file in `wire/25.10/` is currently **synthetic**: hand-written from the `api/v25_10_0` models and
the middleware source on `stable/goldeye` (`"_provenance": "synthetic from api/v25_10_0 ..."`). Replace
them with real recordings:

```sh
TRUENAS_URL=wss://<host> TRUENAS_API_KEY=... go run ./cmd/orchestrator smoke --record fixtures/truenas/wire
```

`--record` writes only the replies of the read-only smoke calls, scrubbed (`unixhash`, `smbhash`,
`password_history`, `sshpubkey`, `trace` and `py_exception` removed at any depth; list results trimmed to
5 entries), to `wire/<major.minor>/`. **Review a recording before committing it:** hostnames, usernames,
dataset names and IP addresses stay visible. Login replies, create replies, job polling and error cases
smoke cannot provoke stay synthetic.

| File | Reply | Used by |
|---|---|---|
| `api.versions.json` | `GET /api/versions` on 25.10 (includes `v25.10.0`) | smoke endpoint row |
| `api.versions.25.04.json` | `GET /api/versions` on 25.04 (no `v25.10.0`) | a non-101 upgrade → `UnsupportedVersionError{ServedAPIs}` |
| `auth.login_ex.json` | `SUCCESS` with `user_info` | handshake |
| `auth.login_ex.auth_err.json` | `AUTH_ERR` | rejected key → `ErrUnauthenticated` |
| `auth.login_ex.expired.json` | `EXPIRED` | revoked key → `ErrUnauthenticated` |
| `auth.login_ex.otp_required.json` | `OTP_REQUIRED` | two-factor → `ErrValidation` |
| `system.version.json` | `TrueNAS-25.10.1` | version gate (in range) |
| `system.version.25.04.json` | `TrueNAS-25.04.2.6` | version gate (out of range) |
| `core.set_options.json` | `{legacy_jobs: false, ...}` | handshake, smoke jobs row |
| `core.ping.json` | `"pong"` | — |
| `core.bulk.json` | bulk result list (non-legacy job reply) | smoke jobs row |
| `core.get_jobs.running.json` / `.success.json` / `.failed.json` | one job in each state | legacy-job fallback polling |
| `core.get_jobs.failed_validation.json` | a failed job whose `exc_info.type` is `VALIDATION` (`job.py`) | fallback → `ErrValidation` with fields |
| `docker.status.json` | `RUNNING` | `DockerStatus` |
| `pool.dataset.query.json` | six datasets, one encrypted and locked with a null mountpoint | `Datasets` |
| `pool.dataset.create.json` | the created dataset entry | `CreateDataset` |
| `pool.dataset.create.einval.json` | -32602 EINVAL with `extra` fields and a `trace` holding locals | validation errors; trace is never decoded |
| `user.query.json` | four local users, `group` row with `bsdgrp_*` keys | `Users` |
| `user.create.json` | the created user entry | `CreateUser` |
| `group.query.json` | seven local groups (exercises the recorder's trim) | `Groups`, `CreateUser` gid → row id |
| `group.create.json` | row id `45` | `CreateGroup` |
| `filesystem.stat.json` | `/mnt` directory | `Stat` |
| `filesystem.stat.enoent.json` | -32001 ENOENT | absent path → `PathStat{}` |
| `filesystem.stat.einval.json` | -32602 EINVAL for `""` | smoke validation row |
| `filesystem.setperm.json` | `null` (non-legacy job result) | `SetPermissions` |
| `filesystem.setperm.legacy.json` | `1234` (a job id: legacy reply) | legacy-job fallback |
| `docker.network.query.json` | the four networks, project `ix-homelab-networks` | `Networks` |
| `app.query.json` | `truenas-orchestrator`, `homelab-networks` | `Apps` |
| `app.create.json` | the created app entry | `CreateApp` |
| `app.create.legacy.json` | `1240` (a job id: legacy reply) | legacy-job fallback |
| `error.method_not_found.json` | -32601 | API drift → `ErrUnavailable` |
| `error.not_authenticated.json` | -32001 errno 207 | `ErrUnauthenticated` |

## `scenarios/<name>.json` — Fake seeds

`truenas.LoadScenario(os.DirFS("fixtures/truenas"), name, opts)` and `truenas.FakeDialer` read these.
Fields: `description`, `version` (the `system.version` string), `auth` (`ok`, `auth_err`, `expired`,
`otp_required`), `results` (raw middleware results for `docker.status`, `pool.dataset.query`,
`user.query`, `group.query`, `docker.network.query`, `app.query`, and `filesystem.stat` as a map from
path to stat result), and optional `errors` (method → JSON-RPC error object, plus `"once": true` to fire
on the first call only). An `errors` key must be a method the Fake answers: the thirteen
`homelab.TrueNAS` methods' wire names, or `auth.login_ex`, `system.version` and `core.set_options`, which
`FakeDialer` fires during its login, version and options steps as `Dial` would. A validation error whose
every field errno is 17 (EEXIST) unwraps to `homelab.ErrConflict`, any other to `ErrValidation`. Results
go through the client's own decoders, so a seed has the wire shape. Unknown fields, unknown `errors`
keys and data after the JSON document are rejected.

`@MNT@` is replaced by `FakeOptions.Mnt` (default `/mnt`; `ORCH_TRUENAS_FAKE_MNT` in development). All
scenarios use pool `tank` at `@MNT@/tank` and the paths the orchestrator config expects:

| Variable | Path | Dataset |
|---|---|---|
| `HOMELAB_STORAGE` | `@MNT@/tank/apps/homelab` | `tank/apps/homelab` |
| `APPCONFIG_STORAGE` | `@MNT@/tank/apps/appconfigs` | `tank/apps/appconfigs` |
| `DATABASE_STORAGE` | `@MNT@/tank/databases` | `tank/databases` (recordsize 16K) |
| `MEDIA_STORAGE` | `@MNT@/tank/media` | `tank/media` (NFSv4, user data) |
| `ORCH_STATE_STORAGE` | `@MNT@/tank/orchestrator` | `tank/orchestrator` (1000:1000 0700) |

| Scenario | State |
|---|---|
| `fresh` | 25.10.1, docker running; the orchestrator app and its state dataset exist, media exists; no `homelab` group or user, no `tank/apps`, no roots, no homelab networks |
| `ready` | everything provisioned: group and user `homelab` 1000, parents and roots as POSIX datasets at 1000:1000 0750, state 0700, the four networks from the running `homelab-networks` app |
| `partial` | `ready`, except appconfigs is 0:0 0755, the `databases` network is missing, and the `homelab-networks` app is gone (its three remaining networks keep the `ix-homelab-networks` label) |
| `unmanaged-networks` | `ready`, but the four networks were made by hand (no compose labels) and there is no networks app: ready as-is |
| `mixed-networks` | `ready`, but only `home` and `proxy` exist, made by hand: blocking |
| `docker-unconfigured` | `fresh`, but `docker.status` is `UNCONFIGURED` and there are no networks or apps |
| `name-collision` | `fresh`, plus group `homelab` 1000 and a user `homelab` holding uid 1001: blocking |
| `version-out-of-range` | `fresh` on `TrueNAS-25.04.2.6` |
| `auth-failure` | `fresh`, but the login answers `AUTH_ERR` |
| `dataset-create-einval` | `fresh`, but every `pool.dataset.create` fails with EINVAL "Path ... already exists" |

The Fake changes its state as the middleware would: created groups and users get the next row id,
`CreateDataset` needs an existing unlocked parent and adds a stat entry `0:0 0755` at its mountpoint,
`SetPermissions` rewrites the stat, and `CreateApp` adds the app as `RUNNING` plus the networks its
compose declares (labelled `ix-<app>`, existing names left alone). `Calls()` lists the mutating calls
that reached it, as `<method> <target>`.
