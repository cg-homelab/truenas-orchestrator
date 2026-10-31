# M4 — TrueNAS client and bootstrap

**Status:** done · **Depends on:** M2 · **Issue:** _(unset)_

## Goal

The orchestrator can talk to TrueNAS and prepare a server from nothing: datasets, the four docker
networks, the git user and groups, and permissions. Two supported routes to the same end state — a
bootstrap script and a hand-install guide.

Everything is developed and tested against a fake; the real middleware is touched only by an explicit
smoke recipe.

## Out of scope

- Deploying apps (M5)
- The orchestrator managing its own TrueNAS app entry — it never does (see [decisions.md](../decisions.md) D11)

## Tasks

- [x] `homelab.TrueNAS` interface covering: dataset create/list, user and group create/list, ACL get/set, docker network create/list, app list, system version
- [x] Websocket JSON-RPC implementation against the TrueNAS middleware, with API-key auth
- [x] `truenas.Fake` backed by `fixtures/truenas/` recorded responses, including failure cases (the fixtures are synthetic until `just smoke --record` runs against a real server)
- [x] Middleware API version detection with a clear error when the version is outside the supported range
- [x] Dataset creation with the properties the homelab needs
- [x] User and group creation, including the git user
- [x] ACL / permission application (`filesystem.setperm` with stripacl, per D32) for `$HOMELAB_STORAGE`, `$APPCONFIG_STORAGE`, `$DATABASE_STORAGE`
- [x] Creation of the four networks: `home`, `proxy`, `isolated`, `databases`
- [x] Git credential setup — SSH deploy key generation and display of the public half (deploy key file plus `GIT_SSH_COMMAND` for fetch and pull)
- [x] Environment-readiness check: one function reporting what exists, what is missing, and what is wrong
- [x] Dry-run preview and explicit confirm on every destructive or creating operation
- [x] Audit log entries for every TrueNAS mutation
- [x] API key storage through the secret-storage interface; never logged, never returned by the API
- [x] `deploy/bootstrap.sh` — first run end to end
- [x] `docs/` hand-install guide covering the identical steps
- [x] `just smoke HOST` — read-only calls against a real TrueNAS to verify API compatibility
- [x] Frontend: environment-readiness view, first-run wizard, API key entry

## Acceptance criteria

1. Every client test passes against `truenas.Fake` with no network.
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

Manual: the list in the design's section 12 ("Manual verification") below.

## Notes / decisions made during implementation

_(append as work happens)_

### Design (2026-10-30)

Design phase. The decisions are D32–D36 in [decisions.md](../decisions.md) (accepted 2026-10-31 by the
tech lead; Martin to confirm). Everything below was researched without a TrueNAS:
API claims come from the middleware source and the published API docs, and each carries its source and
the version it was checked against. Anything that could not be checked that way is marked
**UNVERIFIED** and listed in section 13 with the smoke check or first real run that settles it.

Versions checked: **25.10** (branch `stable/goldeye`, API models `api/v25_10_0`; newest 25.10 tag
`TS-25.10.7`), spot-checked on **27** (`stable/27`, `v27_0_0`; `TS-27.0.0-RC.1`) and **28**
(`master`, `v28_0_0`). api.truenas.com lists 25.10, 27 ("Early") and 28 ("Preview"); 25.04 is in the
archive. The release developed as 26 was renamed 27 at RC.1 ([TrueNAS blog][blog-27]).

#### 1. API facts

Every call goes to the pinned endpoint `/api/v25.10.0` (D35), so the shapes below are the `v25_10_0`
models wherever the server runs 25.10, 27 or 28. "Job" means a `@job` method: the session sets
`legacy_jobs: false`, so the reply arrives when the job finishes and carries its result or error.

| Method / endpoint | Params we send | Result we read | Job | Verified | Source |
|---|---|---|---|---|---|
| `GET /api/<version>` (websocket upgrade) | — | One route per shipped API version. `stable/goldeye`: v24_10, v25_04_0–2, v25_10_0–2; `stable/27`: up to v25_10_5 and v27_0_0; `master`: v25_10_0–5 and v27, v28 (v25.10.3/4 aliased to v25.10.2) | — | 25.10 source; 27 and 28 directory listing | [main.py][ge-main] `_add_api_route`; [api dirs][ge-api]; [aliases.py][master-aliases] |
| `GET /api/versions` (plain HTTPS, no auth) | — | JSON array of version strings, `v24.10` omitted | — | 25.10 | [main.py][ge-main] `api_versions_handler`; proxied by [nginx.conf.mako][ge-nginx] |
| `auth.login_ex` | `[{"mechanism":"API_KEY_PLAIN","username":U,"api_key":K}]` | `response_type`: `SUCCESS` \| `AUTH_ERR` \| `EXPIRED` \| `OTP_REQUIRED` \| `REDIRECT` | no | 25.10; still present on 27 and 28 (`auth.login_with_api_key` is `removed_in='v28'`) | [v25_10_0/auth.py][ge-auth-model]; [plugins/auth.py][ge-auth] |
| `system.version` | `[]` | string `TrueNAS-25.10.1`. 24.10 and earlier: `TrueNAS-SCALE-24.10.2`. "SCALE" was already gone in 25.04.0, not 25.10 | no; authentication required, authorization not | tags TS-25.04.0, TS-25.10.0, TS-27.0.0-RC.1 | [product.py][ge-product]; `sw_info` in [utils/\_\_init\_\_.py][ge-utils]; 24.10 [utils][ee-utils] |
| `core.set_options` | `[{"legacy_jobs":false}]` | `{legacy_jobs, private_methods, py_exceptions}` | no; no auth needed | 25.10, 27, 28. Not in `v25_04_*` models (extra keys ignored there) | [core_service.py][ge-core] `set_options`; [app.py][ge-app-base] default `legacy_jobs=True`; [method.py][ge-method] |
| `core.ping` | `[]` | `"pong"` | no | 25.10 | [core_service.py][ge-core] |
| `core.bulk` (smoke only) | `["core.ping",[[]]]` | `[{job_id, error, result}]` | **yes**: the read-only probe for job semantics | 25.10 | [core_service.py][ge-core] `bulk` |
| `docker.status` | `[]` | `{status: PENDING\|RUNNING\|STOPPED\|INITIALIZING\|STOPPING\|UNCONFIGURED\|FAILED, description}` | no | 25.10 | [v25_10_0/docker.py][ge-docker-model] |
| `pool.dataset.query` | `[[["type","=","FILESYSTEM"]], {"extra":{"retrieve_children":false,"retrieve_user_props":false,"properties":["acltype","aclmode","atime","compression","recordsize","mountpoint","encryption","keystatus"]}}]`. `mountpoint`, `encryption` and `keystatus` must be in the list: with a `properties` list the middleware fills `mountpoint`, `encrypted` and `locked` only from fetched properties, and drops them otherwise (`__transform`; `get_instance_quick` adds the same three) | `id`, `name`, `pool`, `encrypted`, `locked`, `mountpoint` (string or null), and `.value` of `acltype`, `aclmode`, `atime`, `compression`, `recordsize`. Case of `.value` is **UNVERIFIED**: decode upper-cased | no | 25.10 | [v25_10_0/pool_dataset.py][ge-ds-model]; query docs in [pool\_/dataset.py][ge-ds] |
| `pool.dataset.create` | root: `[{"name":"tank/apps/appconfigs","type":"FILESYSTEM","share_type":"GENERIC","acltype":"POSIX","aclmode":"DISCARD","atime":"OFF","compression":"LZ4","recordsize":"128K"}]`. Ancestor: `[{"name":"tank/apps","type":"FILESYSTEM"}]` | dataset entry (ignored) | no | 25.10. The parent must exist (`create_ancestors` defaults to false). An existing mountpoint path is an EINVAL validation error: "Path /mnt/x already exists" | [v25_10_0/pool_dataset.py][ge-ds-model]; `do_create` in [pool\_/dataset.py][ge-ds] |
| `user.query` | `[[["local","=",true]]]` | `id`, `uid`, `username`, `builtin`, `local`; `group` is the related row (`id`, `bsdgrp_gid`, `bsdgrp_group`: key names **UNVERIFIED**) | no | 25.10 | [v25_10_0/user.py][ge-user-model]; `user_extend` in [account.py][ge-account] |
| `user.create` | `[{"username":"homelab","uid":1000,"full_name":"Homelab containers (truenas-orchestrator)","group":<group row id>,"group_create":false,"password_disabled":true,"smb":false,"shell":"/usr/sbin/nologin","home":"/var/empty"}]` | user entry (ignored) | no | 25.10. `group` is the row **id, not the gid**. `smb` defaults to true and needs a password, so it must be false. A taken uid is EEXIST "Uid N is already used" | [v25_10_0/user.py][ge-user-model]; [account.py][ge-account]; `NO_LOGIN_SHELL`, `MIN_AUTO_XID=3000` in [constants.py][ge-acct-const] |
| `group.query` | `[[["local","=",true]]]` | `id`, `gid`, `name`, `builtin`, `local` | no | 25.10 | [v25_10_0/group.py][ge-group-model] |
| `group.create` | `[{"name":"homelab","gid":1000,"smb":false}]` | int (row id) | no | 25.10 | [v25_10_0/group.py][ge-group-model] |
| `filesystem.stat` | `["/mnt/tank/apps/appconfigs"]` | `type` (DIRECTORY/FILE/SYMLINK/OTHER), `uid`, `gid`, `mode` (st_mode with type bits; mask `0o7777`), `acl` (extended ACL present), `is_mountpoint`, `realpath` | no. Absent path: CallError ENOENT "Path X not found". Outside TrueNAS: EXDEV. Relative: EINVAL | 25.10 | [v25_10_0/filesystem.py][ge-fs-model]; `stat` in [plugins/filesystem.py][ge-fs] |
| `filesystem.setperm` | `[{"path":P,"uid":1000,"gid":1000,"mode":"750","options":{"stripacl":true,"recursive":false,"traverse":false}}]` | null | **yes** (lock `perm_change`) | 25.10. `mode` is an octal string 000–777: no setuid, setgid or sticky | [v25_10_0/filesystem.py][ge-fs-model]; `setperm` in [filesystem\_/acl.py][ge-acl]; `UnixPerm` in [types/filesystem.py][ge-fs-types] |
| `filesystem.getacl` / `setacl` | **not used** (D32 uses POSIX mode plus `stripacl`) | — | setacl is a job | 25.10 | [v25_10_0/acl.py][ge-acl-model] |
| `docker.network.query` | `[[["name","in",["home","proxy","isolated","databases"]]]]` | `name`, `id`, `driver`, `scope`, `labels` (`com.docker.compose.project`). **No `internal` field** | no. `[]` when docker is not configured (master source; 25.10 implementation not read) | 25.10 model; 27 and 28 identical. **No create method in any version** | [v25_10_0/docker_network.py][ge-net-model]; [api.truenas.com docker.network][doc-net]; [docker_network_crud.py][master-net] |
| `app.query` | `[[], {"extra":{"retrieve_config":false}}]` | `name` (equals `id`), `state` (CRASHED/DEPLOYING/RUNNING/STOPPED/STOPPING), `custom_app` | no | 25.10 | [v25_10_0/app.py][ge-app-model]; [apps/crud.py][ge-apps-crud] |
| `app.create` | `[{"app_name":"homelab-networks","custom_app":true,"custom_compose_config_string":"<yaml>"}]` | app entry (ignored) | **yes** (lock `app_create_<name>`). The compose is validated with `docker compose config` | 25.10 | [v25_10_0/app.py][ge-app-model]; [custom_app_utils.py][ge-custom-app] |
| `api_key.create` (bootstrap only, via `midclt`) | `{"name":"truenas-orchestrator","username":"truenas_admin"}` | entry with `key`, shown once | no | 25.10 | [v25_10_0/api_key.py][ge-apikey-model] |

Wire facts that shape the client (25.10 source unless noted):

- **Framing.** JSON-RPC 2.0 text frames. `params` must be an array, and batches are not supported
  ([rpc.py][ge-rpc] `validate_message`; [jsonrpc.html][doc-jsonrpc]). The server pushes notifications
  with `method` `collection_update` and `notify_unsubscribed`. They arrive only after `core.subscribe`,
  which we never call, so the client logs any notification at debug and drops it.
- **Error object.** `{"code":C,"message":M,"data":{"error":errno,"errname":"EINVAL","reason":"...","trace":{...}|null,"extra":[[attribute,errmsg,errno],...]|null}}`.
  Codes: -32700, -32600, -32601 (method missing), -32602 (`ValidationError`/`ValidationErrors`, with
  `data.error` 22 and `extra` listing the fields), -32603, -32000 (too many concurrent calls), -32001
  (method call error: `CallError` errno in `data.error`). Source: [rpc.py][ge-rpc],
  [api_client jsonrpc.py][ac-jsonrpc]. Middleware-specific errnos: `ENOTAUTHENTICATED = 207`
  ([exc.py][ac-exc]). A call before login is CallError 207 "Not authenticated", and a missing role is
  EACCES "Not authorized" ([main.py][ge-main]). **`trace` holds stack frames with local variables, so the
  client discards `trace` and `py_exception` unread.**
- **API key revocation over insecure transport.** On a successful `API_KEY_PLAIN` login over a connection
  where `origin.secure_transport` is false, the middleware calls `api_key.revoke` and answers `EXPIRED`
  ([plugins/auth.py][ge-auth]). Secure means the `X-Https: on` header from nginx, AF_UNIX, or a loopback
  peer ([origin.py][ge-origin]). From a container, only `wss://` through nginx qualifies.
- **AF_UNIX as uid 0 is root with no credentials** (`check_permission` in [plugins/auth.py][ge-auth]).
  This is how `midclt` works on the host, and it is why bootstrap needs no API key while the container
  must never get the socket (D35).
- **nginx.** It listens on `ui_address` (default all addresses) and the HTTPS UI port, and proxies
  `/api` to `127.0.0.1:6000` with no `proxy_read_timeout`, so the nginx default of 60 s applies
  ([nginx.conf.mako][ge-nginx]). The client pings every 20 s. nginx serves the UI certificate. The
  default `truenas_default` certificate is self-signed with CN and SAN `localhost`, RSA-2048 and EKU
  serverAuth ([generate_self_signed.py][tcu-selfsigned]).
- **Jobs.** By default (`legacy_jobs=True`) a job method returns the job id, and completion is learned
  through a `core.get_jobs` subscription ([jobs.html][doc-jobs]). After `core.set_options
  {"legacy_jobs": false}`, `method.call` waits for the job and returns its result or raises its error
  ([method.py][ge-method]). `core.job_wait` is itself a job, so in legacy mode it only returns another
  id. The docs' job example describes the non-legacy behaviour as the default; the source says
  otherwise. The source wins, and smoke verifies the live behaviour with `core.bulk`.
- **Host tools** for bootstrap: `midclt` ships with the middleware (`midclt = "truenas_api_client:main"`
  in [api_client pyproject][ac-pyproject]; `midclt call -j <method> <json-arg>...`). `git` is on the host
  because the middleware itself runs it ([utils/git.py][ge-git]). Bootstrap parses JSON with `jq` (see
  section 9), not `python3`.

#### 2. Transport, auth and errors (D35)

- `truenas.Dial(ctx, cfg Config, creds homelab.TrueNASCredentials) (*Client, error)`: dial
  `cfg.URL + "/api/" + APIVersion` (`APIVersion = "v25.10.0"`), call `conn.SetReadLimit(16 << 20)`
  (coder/websocket's default is 32 KiB, and a dataset query exceeds that), start the read loop and the
  20 s ping loop, then `auth.login_ex`, then `system.version` with `homelab.CheckTrueNASVersion`, then
  `core.set_options`. Any failure closes the connection and returns:
  - handshake 404: `GET https://host/api/versions`, then `*homelab.UnsupportedVersionError{Detected: "",
    ServedAPIs: [...]}`;
  - `AUTH_ERR`: `homelab.ErrUnauthenticated` ("TrueNAS rejected the API key for user truenas_admin");
  - `EXPIRED`: the same sentinel, with "the API key is expired or revoked; TrueNAS revokes a key used over
    plain HTTP";
  - `OTP_REQUIRED` or `REDIRECT`: `homelab.ErrValidation` ("two-factor login is not supported for API
    keys");
  - dial, TLS or timeout failure: new sentinel `homelab.ErrUnavailable` ("TrueNAS unreachable at
    wss://…: x509: …"), mapped to HTTP 502.
- `Config{URL string; TLS *tls.Config; SelfApp string; Logger *slog.Logger}`. `ws://` is refused unless
  the host is loopback. `cmd/orchestrator serve` builds the `tls.Config` from `ORCH_TRUENAS_CA_FILE`,
  `ORCH_TRUENAS_TLS_SERVER_NAME` and `ORCH_TRUENAS_INSECURE_TLS`.
- **Legacy-job fallback** (found in implementation). If a job method still replies with a bare number
  (the server ignored `legacy_jobs: false`, e.g. an older middleware), the client treats it as a job id
  and polls `core.get_jobs [["id","=",id]]` until the job is `SUCCESS` or `FAILED`, returning its result
  or error. `core.job_wait` cannot serve: it is itself a job, so in legacy mode it only returns another
  id. Smoke's `jobs` row still reports which mode the server is in.
- Request ids are a `uint64` counter. A pending map holds one channel per call. Context cancellation
  abandons the reply (the middleware keeps running the call). Calls may run concurrently, but domain
  code issues them in sequence.
- **Errors.** A `*truenas.Error{Method, Code, Errno, Errname, Reason string, Fields []FieldError}` whose
  `Unwrap` maps to the homelab sentinels: EINVAL or -32602 to `ErrValidation`, ENOENT to `ErrNotFound`,
  EEXIST to `ErrConflict`, EACCES to `ErrForbidden`, 207 to `ErrUnauthenticated`, -32601 to
  `ErrUnavailable` ("method missing: middleware API drift; run just smoke"). `Error()` is
  `truenas: pool.dataset.create: EINVAL: <reason> (field: errmsg; ...)`. Reasons name datasets and
  paths, and never contain our key, because login failures carry no reason.
- **Logging.** Method name, duration and errname only. Params and results are never logged.
  `homelab.Secret` is revealed once, inside the `auth.login_ex` params.

#### 3. Version policy (D36)

`internal/homelab/truenasversion.go`:

```go
type TrueNASVersion struct {
	Major, Minor, Patch, Build int    // 25.10.0.1 -> 25, 10, 0, 1; missing parts are 0
	Pre                        string // "RC.1", "BETA.1", "MASTER-…"; empty for a release
	Raw                        string
}
// ParseTrueNASVersion accepts "TrueNAS-25.10.1", "TrueNAS-SCALE-24.10.2", "TrueNAS-27.0.0-RC.1",
// "TrueNAS-25.10-RC.1" and a bare "25.10.0.1".
func ParseTrueNASVersion(s string) (TrueNASVersion, error)

// SupportedTrueNAS: (major, minor) >= (25, 10) and major < 28.
var SupportedTrueNAS = VersionRange{MinMajor: 25, MinMinor: 10, BelowMajor: 28}
func (r VersionRange) String() string // "TrueNAS 25.10 through 27.x"

// CheckTrueNASVersion returns nil or *UnsupportedVersionError, which wraps ErrValidation.
func CheckTrueNASVersion(raw string) error

type UnsupportedVersionError struct {
	Detected   string   // as parsed, e.g. "25.04.2.6"; the raw string truncated to 64 runes if unparseable
	Supported  string   // SupportedTrueNAS.String()
	ServedAPIs []string // set when the pinned endpoint is missing
}
```

Table-driven tests cover every format above, plus empty strings, garbage, `28.0.0-RC.1` (out of range)
and `27.0.0-MASTER-…` (in range). Both `Client` and `Fake` run the check at connect time, so
`fixtures/truenas/scenarios/version-out-of-range.json` proves acceptance criterion 7 through the same
code path.

#### 4. End state (D32, D33)

Inputs: `HOMELAB_STORAGE` (orchestrator config, D31) and, from `compose/.global.env`, `APPCONFIG_STORAGE`,
`DATABASE_STORAGE`, `MEDIA_STORAGE`, `PUID` and `PGID`. Also `ORCH_STATE_STORAGE` (new orchestrator config;
the host path of the state dataset, mounted at the same path in the container) and `ORCH_SELF_APP`
(default `truenas-orchestrator`).

| Item | Expected | Orchestrator plans | Bootstrap / guide |
|---|---|---|---|
| version | in `SupportedTrueNAS` | — (blocking) | preflight, stop |
| docker | `docker.status` RUNNING | — (blocking: "choose an apps pool in Apps > Configuration") | preflight, stop |
| group | a group with gid `PGID` (created as `homelab`) | `group.create` | B2 |
| user | a user with uid `PUID` (created as `homelab`, primary group above) | `user.create` | B3 |
| dataset × 3 | `HOMELAB`, `APPCONFIG`, `DATABASE` roots are datasets, mounted at the path. Props per D32 (`DATABASE` recordsize 16K) | `pool.dataset.create` for missing parents (plain) then the root | B4 |
| state dataset | `ORCH_STATE_STORAGE` is a dataset, `PUID:PGID 0700` | **check only** | B4, B5 |
| permissions × 3 | root dir `PUID:PGID 0750`, `acl: false` | `filesystem.setperm` (stripacl, non-recursive) | B5 |
| media | `MEDIA_STORAGE` exists | — (advisory) | B1 (checked) |
| networks × 4 | exist by name | `app.create homelab-networks` (rules in D33) | B6 |
| networks app | exists and RUNNING when it owns the networks | as above | B6 |
| self app | `ORCH_SELF_APP` exists (informational; never modified) | **never** | B13 |
| deploy key | `$ORCH_STATE_STORAGE/ssh/id_ed25519` `0600` and `.pub` | separate `create-deploy-key` (not part of the plan digest) | B7 |
| global env | `compose/.global.env` exists and defines the six keys; roots are absolute, under a pool, distinct and not nested in each other | — (blocking) | B10 |

Item states: `ok`, `missing` (the plan fixes it), `wrong` (blocking; the plan cannot fix it; a manual
instruction is given), `differs` (advisory; ready anyway, e.g. compression ZSTD, or a root that is a
plain directory rather than a dataset), `unknown` (a read failed). `Ready` means no `missing`, `wrong` or
`unknown` items. Blocking rules worth naming: a `homelab` group or user holding a different id; a root
that is a symlink, a file or inside a locked or encrypted-locked dataset; a missing root whose nearest
existing ancestor is a plain directory, because a dataset cannot be created there; `PUID` or `PGID` out
of 1000–2999 (except an existing account) or equal to 950 (`truenas_admin`); `ORCH_SELF_APP` equal to
`homelab-networks` (criterion 8).

Networks app compose (the Go constant `homelab.NetworksAppCompose`; `deploy/homelab-networks.yaml` is a
byte-identical copy):

```yaml
# Managed by truenas-orchestrator (D33): creates the external networks the homelab stacks use.
# Do not stop or delete this app while any stack is running; stopping it removes unused networks.
services:
  holder:
    image: busybox:1.37.0
    command: ["sleep", "infinity"]
    restart: unless-stopped
    read_only: true
    user: "65534:65534"
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    networks: [home, proxy, isolated, databases]
networks:
  home: {name: home, driver: bridge}
  proxy: {name: proxy, driver: bridge}
  isolated: {name: isolated, driver: bridge, internal: true}
  databases: {name: databases, driver: bridge}
```

#### 5. Interface and types (`internal/homelab/truenas.go`)

It replaces the M0 one-method interface in `interfaces.go`.

```go
// TrueNAS is the subset of the TrueNAS middleware the domain calls (D22, D35). A value is one
// authenticated, version-checked session; truenas.Dial and truenas.Fake produce them.
type TrueNAS interface {
	SystemVersion(ctx context.Context) (string, error)
	DockerStatus(ctx context.Context) (DockerStatus, error)
	// Datasets returns every filesystem dataset.
	Datasets(ctx context.Context) ([]Dataset, error)
	CreateDataset(ctx context.Context, spec DatasetSpec) error
	// Users and Groups return local accounts only.
	Users(ctx context.Context) ([]User, error)
	CreateUser(ctx context.Context, spec UserSpec) error
	Groups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, spec GroupSpec) error
	// Stat describes a host path without following a final symlink; absent is PathStat{} and nil.
	Stat(ctx context.Context, path string) (PathStat, error)
	// SetPermissions sets owner and mode on one directory, non-recursively, stripping any extended ACL.
	SetPermissions(ctx context.Context, spec PermissionSpec) error
	Networks(ctx context.Context) ([]Network, error)
	Apps(ctx context.Context) ([]App, error)
	// CreateApp creates a custom compose app. Implementations refuse CheckAppMutation failures too.
	CreateApp(ctx context.Context, spec AppSpec) error
}

type TrueNASCredentials struct {
	Username string
	APIKey   Secret
}

type DockerStatus struct{ Status, Description string } // Status: RUNNING, UNCONFIGURED, ...

type Dataset struct {
	Name, Mountpoint string // Mountpoint "" when unmounted
	Encrypted, Locked bool
	Props DatasetProps
}
type DatasetProps struct{ ACLType, ACLMode, Atime, Compression, RecordSize string } // upper-case values
type DatasetSpec struct {
	Name  string
	Props *DatasetProps // nil: create with only the name and type (an ancestor inherits everything)
}

type User struct {
	ID, UID, GID int // GID of the primary group
	Name         string
	Builtin      bool
}
type UserSpec struct {
	Name     string
	UID, GID int    // the client resolves GID to the group row id
	FullName string
}
type Group struct {
	ID, GID int
	Name    string
	Builtin bool
}
type GroupSpec struct {
	Name string
	GID  int
}

type PathType string // "DIRECTORY", "FILE", "SYMLINK", "OTHER"
type PathStat struct {
	Exists       bool
	Type         PathType
	UID, GID     int
	Mode         FileMode // permission bits only (& 0o7777)
	ExtendedACL  bool
	IsMountpoint bool
}
type PermissionSpec struct {
	Path     string
	UID, GID int
	Mode     FileMode // <= 0o777
}

type Network struct {
	Name, ID, Driver string
	ComposeProject   string // label com.docker.compose.project, "" when absent
}
type App struct {
	Name, State string
	Custom      bool
}
type AppSpec struct {
	Name    string
	Compose string // custom_compose_config_string
}

// CheckAppMutation returns an error wrapping ErrForbidden when target is the orchestrator's own app.
// Comparison is exact: app names are lower-case by the middleware's own rule.
func CheckAppMutation(selfApp, target string) error
```

There is no `Close` on the interface. The server declares
`TrueNASConn interface{ homelab.TrueNAS; Close() error }`, which is the only extra it needs.

**Domain flow** (`internal/homelab`, the M3 pattern):

```go
type EnvironmentConfig struct {
	SelfApp, HomelabStorage, StateStorage string
	Global                                DotEnv // compose/.global.env
}
func NewEnvironmentSpec(cfg EnvironmentConfig) (EnvironmentSpec, []string /*problems*/)
func ObserveEnvironment(ctx context.Context, spec EnvironmentSpec, tn TrueNAS) (EnvironmentObserved, error) // reads only
func CheckEnvironment(spec EnvironmentSpec, obs EnvironmentObserved) Readiness                                // pure
func ApplyBootstrap(ctx context.Context, spec EnvironmentSpec, tn TrueNAS, expectDigest string,
	report func(line string)) (BootstrapResult, error)
```

`Readiness{Version VersionStatus; Items []ReadinessItem; Steps []BootstrapStep; Problems []string;
Ready, Runnable bool; Digest string}`. `BootstrapStep{Kind (group_create | user_create |
dataset_create | permissions_set | app_create), Target, Action (create | change), Detail
map[string]string}`. A `permissions_set` step carries `from_owner`, `from_mode` and `to_*`, so the
preview shows old and new. Steps are ordered group, user, datasets with parents first, permissions,
networks app. `Digest` is the hex SHA-256 of each step's kind, target, action and sorted details, so a
run is bound to the reviewed plan as in M3. `ApplyBootstrap` observes again, compares the digest
(mismatch: an error wrapping `ErrConflict`), refuses a non-runnable plan (`ErrValidation`), runs each
step after re-checking at the point of write (now present: `unchanged`), calls `CheckAppMutation`
before `CreateApp`, stops at the first error, and returns the steps done. Running it twice changes
nothing the second time (criterion 4).

**Deploy key** (`internal/homelab/deploykey.go`), through `SetupHost` on root
`spec.StateStorage`:

```go
type KeyGenerator func(comment string) (private Secret, authorizedKey string, err error)
func EnsureDeployKey(ctx context.Context, host SetupHost, stateRoot, comment string, gen KeyGenerator) (DeployKey, bool /*created*/, error)
func ReadDeployKey(ctx context.Context, host SetupHost, stateRoot string) (DeployKey, error) // ErrNotFound when absent
type DeployKey struct{ PublicKey, Fingerprint, Path string }
```

`gitrepo.NewDeployKey` implements `KeyGenerator` with `crypto/ed25519` and `ssh.MarshalPrivateKey`.
`ssh` imports `net`, so key generation stays out of homelab.

#### 6. Package and file plan (three tickets; no file has two owners)

**Ticket 1: TrueNAS client, fake and fixtures.** Starts first; its first commit is the homelab types.

- `internal/homelab/truenas.go` (new): the interface, value types and `CheckAppMutation` above, with
  `interfaces.go` edited to remove the old `TrueNAS` block. `internal/homelab/errors.go`: add
  `ErrUnavailable`. `internal/homelab/truenasversion.go` and `_test.go`.
- `internal/truenas/conn.go`: websocket and JSON-RPC (ids, pending map, read loop, ping loop, close).
- `internal/truenas/errors.go`: wire error decoding into `*Error` and sentinel mapping.
- `internal/truenas/client.go`: `Config`, `Dial`, the version gate and the 13 methods. Replaces the M0
  `NewClient` stub. Also `EncodeDatasetCreate(spec) []byte` and the matching encoders, which ticket 3's
  bootstrap payload test compares against.
- `internal/truenas/decode.go`: wire structs into homelab types, shared by `Client`, `Fake` and smoke.
- `internal/truenas/fake.go` and `scenario.go`: `Fake`, `LoadScenario(fsys fs.FS, name string,
  opts FakeOptions)`, `FakeDialer`, `(*Fake).Close`. Replaces the M0 canned `Fake`.
- `internal/truenas/smoke.go`: `Smoke(ctx, cfg, creds, w io.Writer, record string) (SmokeReport, error)`.
- Tests: `conn_test.go`, `client_test.go`, `fake_test.go`, `smoke_test.go` and `fakeserver_test.go` (an
  `httptest` websocket server on loopback replaying `fixtures/truenas/wire`). Includes a TLS test that
  generates a certificate shaped like `truenas_default` and checks that `ORCH_TRUENAS_CA_FILE` plus server name
  `localhost` verifies.
- `fixtures/truenas/README.md`, `fixtures/truenas/wire/25.10/*.json`, `fixtures/truenas/scenarios/*.json`.

**Ticket 2: domain, sqlite, server and wiring.** Depends on ticket 1's homelab types.

- `internal/homelab/environment.go` (config, spec, observe), `environmentcheck.go` (`CheckEnvironment`,
  digest), `bootstrapapply.go`, `deploykey.go`, the networks compose constant in `networks.go`, and tests
  with an in-package fake (`environment_fake_test.go`, as in M3).
- `internal/sqlite/migrations/0005_credentials.sql`, `queries/credentials.sql`, `credentials.go` plus
  the generated `db/` code (`just generate`), and tests.
- `internal/server/truenas.go` (`CredentialStore`, `TrueNASConn`, `TrueNASDialer`, settings operations),
  `environment.go` (`get-environment`, `run-bootstrap`, audit recording), `deploykey.go`. Edits to
  `server.go` (`Deps`), `routes.go`, `audit.go` (action constants) and `errors.go` (`ErrUnavailable` to
  502), with tests.
- `internal/gitrepo/deploykey.go` (`NewDeployKey`). `repo.go`: `Options{SSHKey, KnownHosts string}`,
  which sets `GIT_SSH_COMMAND=ssh -i <key> -o IdentitiesOnly=yes -o UserKnownHostsFile=<kh> -o
  StrictHostKeyChecking=accept-new` when the key file exists.
- `cmd/orchestrator/serve.go`: new config `ORCH_TRUENAS_URL` (default empty, which reports
  `not_configured`; `fake` or `fake:<scenario>` selects the Fake), `ORCH_TRUENAS_CA_FILE`,
  `ORCH_TRUENAS_TLS_SERVER_NAME`, `ORCH_TRUENAS_INSECURE_TLS`, `ORCH_TRUENAS_FIXTURES` (default
  `fixtures/truenas`), `ORCH_TRUENAS_FAKE_MNT` (replaces the scenario token `@MNT@`, default `/mnt`),
  `ORCH_STATE_STORAGE`, `ORCH_SELF_APP`.
- `justfile` (`dev_env` gains `ORCH_TRUENAS_URL=fake:fresh`, `ORCH_TRUENAS_FAKE_MNT` and
  `ORCH_STATE_STORAGE`; the `smoke host` recipe becomes `TRUENAS_URL=wss://{{host}} go run
  ./cmd/orchestrator smoke`), and `scripts/dev-storage.sh` (roots move under
  `dist/dev-storage/mnt/tank/...`, so the fake's pool dataset `tank` at `@MNT@/tank` contains them).
- `frontend/src/lib/api.d.ts` (regenerated), `docs/on-server-file-structure.md` (state dataset layout),
  the M4 Notes.

**Ticket 3: frontend, deploy, guide and smoke command.** The frontend can start from section 8's shapes
and switch to the regenerated types when ticket 2 lands.

- `frontend/src/routes/environment/+page.svelte`, and `frontend/src/lib/components/TrueNASConnect.svelte`,
  `EnvironmentReadiness.svelte`, `BootstrapPlan.svelte`, `DeployKey.svelte`. Edit
  `frontend/src/routes/+layout.svelte` for the nav link.
- `deploy/bootstrap.sh`, `deploy/bootstrap.env.example`, `deploy/compose.yaml` (the orchestrator app),
  `deploy/homelab-networks.yaml`, and `deploy/deploy_test.go` (package `deploy`: the networks YAML is
  byte-equal to `homelab.NetworksAppCompose`; every `# payload:<name>` heredoc in `bootstrap.sh` is
  JSON-equal to ticket 1's encoders for the same spec; the step ids B0–B13 appear in the same order in
  `bootstrap.sh` and the guide; `bash -n deploy/bootstrap.sh` passes).
- `docs/hand-install.md` (new): the guide.
- `cmd/orchestrator/smoke.go` and the dispatch in `main.go`, with `smoke_test.go` covering config errors
  only (a missing `TRUENAS_API_KEY` fails, and no flag accepts a key).

`docs/architecture/components.md` already lists the new table and subcommand (this design).

#### 7. Fake and fixtures

- `fixtures/truenas/wire/25.10/<method>[.<case>].json` holds exact JSON-RPC reply envelopes, for
  example `pool.dataset.query.json`, `filesystem.stat.enoent.json`, `pool.dataset.create.einval.json`,
  `auth.login_ex.auth_err.json`, `auth.login_ex.expired.json` and `system.version.25.04.json`. The client
  tests replay them through `fakeserver_test.go`. They start as **synthetic** (hand-written from the models
  above, `"_provenance": "synthetic from api/v25_10_0"` in the README index) and are replaced by
  `orchestrator smoke --record` output from the real server. A recording is reviewed before commit:
  hostnames, usernames and IPs are visible.
- `fixtures/truenas/scenarios/<name>.json` seeds the stateful `Fake`. A seed is raw middleware query
  results, decoded by the same `decode.go` as the client, so the Fake cannot drift from the wire shape
  unnoticed:

```json
{
  "description": "fresh 25.10.1, pool tank, apps pool configured, nothing provisioned",
  "version": "TrueNAS-25.10.1",
  "auth": "ok",
  "results": {
    "docker.status": {"status": "RUNNING", "description": "Application(s) are running"},
    "pool.dataset.query": [{"id": "tank", "name": "tank", "mountpoint": "@MNT@/tank", "...": "..."}],
    "user.query": [], "group.query": [],
    "filesystem.stat": {"@MNT@/tank": {"type": "DIRECTORY", "uid": 0, "gid": 0, "mode": 16877, "acl": false}},
    "docker.network.query": [], "app.query": [{"name": "truenas-orchestrator", "state": "RUNNING", "custom_app": true}]
  },
  "errors": {"pool.dataset.create": {"code": -32602, "message": "Invalid params", "data": {"error": 22, "errname": "EINVAL", "reason": "...", "extra": [["pool_dataset_create.name", "...", 22]], "trace": null}}}
}
```

  `auth` is `ok`, `auth_err` or `expired`. `errors` entries apply on every call to that method; an entry
  may set `"once": true`. Scenarios: `fresh`, `ready`, `partial` (one root with the wrong owner, one
  network missing), `unmanaged-networks` (all four networks made by hand: ready), `mixed-networks`
  (blocking), `docker-unconfigured`, `name-collision` (`homelab` user with the wrong uid),
  `version-out-of-range` (`TrueNAS-25.04.2.6`), `auth-failure`, `dataset-create-einval`.
- **Mutations change the state.** `CreateGroup` and `CreateUser` append rows. `CreateDataset` appends a
  dataset, plus a `filesystem.stat` entry for its mountpoint (`0:0 0755`, no ACL). `SetPermissions`
  rewrites the stat. `CreateApp` appends the app and, for compose that declares networks, those networks
  labelled with project `ix-<app>`. A second `CheckEnvironment` after `ApplyBootstrap` on `fresh` therefore
  has zero steps, a test pins that, and a scenario's errors still fire. `FakeDialer` runs the same auth
  and version gate as `Dial`. The Fake records every mutating call it receives (`Calls()`), so tests
  assert that a refused self-app mutation never reached it.

#### 8. Server operations

| Operation id | Route | Behaviour | Errors |
|---|---|---|---|
| `get-truenas-settings` | `GET /api/truenas/settings` | `{configured, username, url, self_app}`. Never the key | — |
| `put-truenas-settings` | `PUT /api/truenas/settings` | Body `{username, api_key}` (`secretField`). Dials, logs in and checks the version, and stores only on success. Returns `{configured: true, version}`. Audit `truenas.credentials.write` `{username}` | 422 (rejected key, unsupported version: the message names detected and supported), 502 (unreachable) |
| `delete-truenas-settings` | `DELETE /api/truenas/settings` | Removes the key. Audit `truenas.credentials.delete` | — |
| `get-environment` | `GET /api/environment` | Observe and check. `{connection: {state: ok\|not_configured\|unreachable\|auth_failed\|unsupported_version\|error, message, detected_version, supported}, ready, runnable, digest, items, steps, problems}`. Returns 200 even when not connected | 500 (no repo) |
| `run-bootstrap` | `POST /api/environment/bootstrap` | Body `{plan_digest}` (required). Plans in the handler: a different digest is 409, a non-runnable plan is 422 listing problems. Otherwise enqueues job kind `environment.bootstrap`, which re-dials, re-plans and runs `ApplyBootstrap`, with one job line per step and a summary. Runs with zero steps succeed with "nothing to do" | 202; 409, 422, 503 |
| `get-deploy-key` | `GET /api/environment/deploy-key` | `{exists, public_key, fingerprint, path}` | — |
| `create-deploy-key` | `POST /api/environment/deploy-key` | Creates it if absent. Audit `deploy_key.create` `{path, fingerprint}` | 409 (exists) |

Audit, recorded after each completed mutation with the job id: `group.create` (target name, `gid`),
`user.create` (name, `uid`, `gid`), `dataset.create` (dataset, the props), `acl.set` (path, `uid`, `gid`,
`mode`, `from_owner`, `from_mode`, `stripacl`), `app.create` (app), `network.create` (one per network
the app created, `app`), then `bootstrap.run` with counts and state, like `setup.run`. **Locking:** the
job runner already runs one job at a time and refuses a second job of the same kind (409), so a
bootstrap cannot overlap a setup or fetch. No new lock is needed. The handler and the job dial separate
sessions.

`server.Deps` gains `TrueNAS TrueNASDialer` (`func(ctx, homelab.TrueNASCredentials) (TrueNASConn,
error)`), `Credentials CredentialStore` (wired to the same `*sqlite.Store`) and `Environment
homelab.EnvironmentConfig` (without `Global`, which the handler reads from `compose/.global.env` through
`Repo.ReadFile` and `ParseDotEnv` on every request).

#### 9. Bootstrap step list (the single source for `bootstrap.sh` and `docs/hand-install.md`)

`bootstrap.sh` runs on the TrueNAS host as root (`sudo -i`, or over SSH). It reads `bootstrap.env`:
`HOMELAB_STORAGE`, `APPCONFIG_STORAGE`, `DATABASE_STORAGE`, `MEDIA_STORAGE`, `ORCH_STATE_STORAGE`,
`PUID`, `PGID`, `REPO_URL`, `ORCH_IMAGE`, `ORCH_APP_NAME` (default `truenas-orchestrator`) and
`TRUENAS_API_USER` (default `truenas_admin`). It has three phases. **Check** is read-only: it prints every
item as ok / will create / will change / blocked. **Confirm**: "Apply N changes? [y/N]", or `--yes`;
`--dry-run` stops after Check. **Apply** runs the steps in order. Re-running prints "nothing to do" and
exits 0 (criterion 4). JSON is parsed and built with `jq`, which TrueNAS SCALE ships as far as we know
(not verified on a live 25.10; B0 checks for it and stops with a message), not `python3`. `midclt` talks
to the middleware over AF_UNIX, which serves `/api/current`, not the pinned `v25.10.0`: on 27.x the
script's payloads are therefore validated against the live schema rather than the pinned one, and
`deploy/deploy_test.go` only proves they equal what the orchestrator sends over `v25.10.0`. Every TrueNAS object goes through `midclt call` (`-j` for jobs) with a
`# payload:<name>` heredoc. Local filesystem work (ssh-keygen, git, chown, the PEM file) is shell. No
secret is logged, except the API key in B11, printed once on request.

| Id | Step | Check | Script action | Guide action (TrueNAS UI) |
|---|---|---|---|---|
| B0 | Preflight | `midclt call system.version` in range; `docker.status` RUNNING; inputs absolute, under `/mnt/<pool>`, distinct; PUID/PGID integers | stop with the message | Dashboard version; Apps > Configuration > Choose Pool |
| B1 | Media check | `filesystem.stat $MEDIA_STORAGE` | warn only | Datasets: confirm it exists |
| B2 | Group | `group.query [["gid","=",PGID]]` | `group.create` (payload:group) | Credentials > Groups > Add (gid, no SMB) |
| B3 | User | `user.query [["uid","=",PUID]]` | `user.create` (payload:user) | Credentials > Users > Add (uid, primary group, disable password, no SMB, nologin) |
| B4 | Datasets | `pool.dataset.query`; resolve parents by mountpoint | `pool.dataset.create` parents (payload:dataset-parent), then roots (payload:dataset-root, payload:dataset-database) and state (payload:dataset-state) | Datasets > Add Dataset, Advanced: ACL Type POSIX, ACL Mode Discard, Atime off, LZ4, record size |
| B5 | Permissions | `filesystem.stat` uid, gid, mode, acl | `midclt call -j filesystem.setperm` (payload:perm) for the 3 roots at 750 and state at 700 | Datasets > Permissions > Edit: owner and group, mode, "apply" not recursive, strip ACL |
| B6 | Networks | `docker.network.query`, `app.query homelab-networks` | `midclt call -j app.create` with `deploy/homelab-networks.yaml` (payload:networks-app) | Apps > Discover > Custom App > Install via YAML, name `homelab-networks`, paste the file |
| B7 | Deploy key | `test -f $ORCH_STATE_STORAGE/ssh/id_ed25519` | `install -d -m 700 -o PUID -g PGID ssh`; `ssh-keygen -t ed25519 -N '' -C truenas-orchestrator@$(hostname) -f …`; chown; chmod 600/644; print `.pub`; wait for "added as a read-only deploy key" | Shell: the same commands |
| B8 | Repo | `HOMELAB_STORAGE` empty, or already a clone of `REPO_URL` | `GIT_SSH_COMMAND` as in section 6; `git clone`; `chown -R PUID:PGID` | Shell: same |
| B9 | known_hosts | written by the B8 clone (`accept-new`) | — | — |
| B10 | `.global.env` | exists and its six keys equal `bootstrap.env` | absent: copy `example.global.env`, set the keys, `0600 PUID:PGID`. Present and different: **stop**, never edit | Shell: copy and edit |
| B11 | API key | `api_key.query [["name","=","truenas-orchestrator"]]` | absent and `--create-api-key`: `api_key.create` (payload:api-key), print the key once to paste into the wizard. Never written to disk | Credentials > Users > truenas_admin > API Keys > Add |
| B12 | UI certificate | `system.general.config` `ui_certificate.certificate` vs `$ORCH_STATE_STORAGE/truenas-ca.pem` | write or refresh the PEM `0644` | System > Certificates > download, copy to the path |
| B13 | Orchestrator app | `app.query [["name","=",ORCH_APP_NAME]]` | absent: render `deploy/compose.yaml` (envsubst of the inputs) and `midclt call -j app.create` (payload:orchestrator-app). Present: never touched (D11) | Apps > Custom App > Install via YAML |

The orchestrator's readiness covers B0–B7 and B10 (the state dataset is check-only). B8 is covered by the
existing repo status, and B11–B13 by the connection state and the self-app item. `deploy/compose.yaml`
runs `${ORCH_IMAGE}` as `user: "${PUID}:${PGID}"`, mounts the four storage roots and the state dataset at
identical paths, sets `ORCH_DB_PATH=${ORCH_STATE_STORAGE}/orchestrator.db`, `ORCH_STATE_STORAGE`,
`HOMELAB_STORAGE`, `ORCH_SELF_APP`, `ORCH_TRUENAS_URL=wss://host.docker.internal`,
`ORCH_TRUENAS_CA_FILE=${ORCH_STATE_STORAGE}/truenas-ca.pem` and `ORCH_TRUENAS_TLS_SERVER_NAME=localhost`,
adds `extra_hosts: ["host.docker.internal:host-gateway"]`, joins `proxy` (external), and publishes
`127.0.0.1:30808:8080` for break-glass access over an SSH tunnel (open question 7).

**What cannot be tested on the Mac, and how each is covered:**

- `bootstrap.sh` end to end. Covered structurally by `deploy/deploy_test.go`: the same payloads, step ids
  and compose text as the orchestrator. A fake-backed `midclt` shim is not worth building: it would test
  the shim, and the payload equality already ties the script to the client's encoders. Also covered by
  one manual run (section 12).
- The guide matching the script. Covered by the step-id test plus the manual diff in section 12.
- Live API shapes, TLS and the job behaviour: `just smoke`.
- Container-to-host reachability (`host-gateway`, nginx bind address, `ui_allowlist`): the
  `get-environment` connection state on the deployed app.

#### 10. `just smoke <host>`

`orchestrator smoke` reads `TRUENAS_URL` (the recipe sets `wss://<host>`), `TRUENAS_API_KEY`
(required, env only, never a flag), `TRUENAS_USERNAME` (default `truenas_admin`), `TRUENAS_CA_FILE`,
`TRUENAS_TLS_SERVER_NAME` and `TRUENAS_INSECURE_TLS`, and `--record <dir>`. It calls `truenas.Smoke`,
which runs only read-only calls through the same decoders as the client and prints one row per check:

| Check | Call | Passes when |
|---|---|---|
| endpoint | upgrade `/api/v25.10.0`; `GET /api/versions` | upgrade succeeds; the list contains `v25.10.0` |
| auth | `auth.login_ex` | `SUCCESS` |
| version | `system.version` | parses and is in range |
| jobs | `core.set_options`, then `core.bulk ["core.ping",[[]]]` | the reply is the bulk result list, not a job id |
| docker | `docker.status` | decodes |
| datasets | `pool.dataset.query` (section 1 params, including `mountpoint`, `encryption` and `keystatus` in `properties`) | required fields present; property `.value` case recorded |
| users / groups | `user.query`, `group.query` (local) | required fields, including the `group` row keys |
| stat | `filesystem.stat /mnt`; `filesystem.stat /mnt/.orchestrator-smoke-absent` | DIRECTORY; ENOENT decoded as absent |
| validation shape | `filesystem.stat ""` | -32602 with errname EINVAL and `extra` decoded |
| networks / apps | `docker.network.query`, `app.query` | decode |

A failing row prints the method, the expected and actual shape, and the action ("middleware API
changed: re-record fixtures with --record and update decode.go; if the version is new, see D36").
The command exits non-zero on any failure. `--record` writes scrubbed envelopes (`unixhash`, `smbhash`,
`password_history` and `sshpubkey` removed; lists trimmed to 5 entries) to
`fixtures/truenas/wire/<major.minor>/`.

#### 11. Frontend

Route `/environment`, with a header link carrying a badge while not ready.

- **First-run wizard**, shown while the connection is not `ok` or the environment is not ready. A
  stepper: (1) **Connect**: `TrueNASConnect` with a username (default `truenas_admin`) and the API key
  (`type=password`, `autocomplete="new-password"`, the password-manager ignore attributes from M3),
  submitted through `put-truenas-settings`. The key field clears after submit, and a 422 shows the
  message, which names the detected and supported versions on a version error. (2) **Version**: the
  detected version and the supported range. (3) **Readiness**: `EnvironmentReadiness` groups items by
  kind with badges ok / missing / wrong / differs / unknown, with problems listed above. (4) **Preview**:
  `BootstrapPlan` lists the steps with action badges and old to new values for permissions. (5)
  **Confirm and run**: a checkbox "I reviewed these N changes" enables Run, which posts
  `run-bootstrap {plan_digest}`, streams with `followJob`, and on done re-fetches `get-environment`. A
  409 re-plans with "the plan changed; review it again", and a 422 shows the problems (M3's
  SetupWizard pattern). (6) **Deploy key**: `DeployKey`.
- **Readiness view** (after first run): the same components without the stepper, plus a "Re-check"
  button and a settings panel with "replace key" and "remove key".
- **`DeployKey`**: the public key in a `<pre>` with a copy button, the fingerprint and the path.
  "Generate" (with a confirm dialog) only when absent.
- Tests: `npm run check`. A Vitest component test only if the M3 frontend has one by then (it has none
  today).

#### 12. Manual verification (replaces the milestone's "bootstrap against the fake in a container")

1. On the real TrueNAS: `bootstrap.sh --dry-run`, then a real run. In the UI, `/environment` shows Ready.
   Save `GET /api/environment` as `after-bootstrap.json`.
2. Run `bootstrap.sh` again and expect "nothing to do". Re-check, and expect zero steps (criterion 4).
3. On a scratch pool, or after renaming the roots in a second `bootstrap.env`, follow
   `docs/hand-install.md` by hand. Save `GET /api/environment` and diff it against step 1 with paths
   normalised: the item states and the step lists must match (criterion 3).
4. Set `ORCH_SELF_APP=homelab-networks` and expect a blocking problem and no `app.create` in the
   TrueNAS audit (criterion 8). The automated equivalent is the Fake test.
5. `grep` the server log, the job log, the audit log and the `GET` responses for the key: zero hits
   (criterion 6).

#### 13. UNVERIFIED (each needs `just smoke` or the first real run)

1. Container to host: `host.docker.internal:host-gateway` reaches nginx on TrueNAS's docker; nginx
   listens there (`ui_address` default); `ui_allowlist`, if set, admits the container's address.
   Checked by: `get-environment` on the deployed app.
2. Go verifying the default UI certificate with `RootCAs` set to that certificate and `ServerName`
   `localhost`. Expected to work (a root may be the leaf). Ticket 1's unit test reproduces it with an
   identical certificate, and smoke confirms it live.
3. `legacy_jobs: false` live behaviour, given that the docs and the source disagree. Checked by: smoke
   `jobs`.
4. A custom app with explicit network `name:`, `internal: true` and the busybox placeholder installs,
   and `docker compose config` accepts it. Checked on the first real run (B6).
5. Property `.value` case in `pool.dataset.query`, the `user.query` `group` row key names, and the
   `docker.network.query` labels key. Checked by: smoke rows.
6. `filesystem.setperm` with explicit uid and gid and `stripacl` on an NFSv4 root (an existing dataset
   on Martin's server). Checked on the first real run.
7. `docker.network.query` returning `[]` when docker is unconfigured, on 25.10 (seen only in master
   source). Covered by readiness reading `docker.status` first.
8. The nginx idle timeout really closes an idle `/api` websocket after 60 s (inferred from no
   `proxy_read_timeout` on `/api`). Harmless either way with 20 s pings.

#### 14. Open questions for Martin

1. **Which version does the server run?** The design needs 25.10 or later (D36). On 25.04, recommend
   upgrading to 25.10 (the current stable line) before M4 lands. The alternative is a second job-wait
   path and endpoint for one release line.
2. **One identity or a dedicated git user?** Recommend one (`homelab` = `PUID`, D32). A separate git
   user conflicts with M3's `0600` secrets and with git's ownership check.
3. **Networks app or host `docker network create`?** Recommend the app (D33). Hand-made networks on the
   current server are accepted as-is, so nothing changes there unless a network is missing.
4. **Who owns the API key?** Recommend `truenas_admin` for M4. A dedicated least-privilege user with a
   privilege of `DATASET_WRITE`, `ACCOUNT_WRITE`, `FILESYSTEM_ATTRS_WRITE`, `APPS_WRITE` and
   `READONLY_ADMIN` is a follow-up (role names not verified).
5. **known_hosts policy.** Recommend `accept-new` (trust on first use), with the host key fingerprint
   shown beside the deploy key. Pinning GitHub's published keys is the stricter alternative.
6. **Image and registry.** Recommend `ghcr.io/cg-homelab/truenas-orchestrator:<version>`, published by a
   tag-triggered workflow, with `ORCH_IMAGE` in `bootstrap.env`.
7. **Break-glass access.** Recommend publishing `127.0.0.1:30808:8080` alongside the Traefik route, used
   over `ssh -L`. Browsers accept `Secure` cookies on `http://localhost`, so `ORCH_SECURE_COOKIES` stays
   true. This addresses D4's lockout concern when Traefik itself is down.
8. **Database recordsize 16K or the default 128K?** Recommend 16K (PostgreSQL pages are 8K). Existing
   datasets are never changed.
9. **Should bootstrap create the API key?** Recommend yes, behind `--create-api-key`, printed once
   (as the TrueNAS UI does) and never written to disk.
10. **Promote D32–D36 from draft to accepted**, or amend them, before ticket 2 starts.

### Ticket 2 notes (2026-10-31)

Domain, sqlite, server operations and gitrepo SSH support landed; `cmd/orchestrator/serve.go` wiring,
`justfile` and `scripts/dev-storage.sh` are a separate ticket. Deviations from the design and decisions
made on the way:

- **Readiness items.** Kinds: `version`, `docker`, `global_env`, `group`, `user`, `dataset` (one per
  root), `state_dataset`, `permissions` (one per root that exists or is planned), `media`, `networks`,
  `networks_app` (only when the app exists or is planned), `self_app`, `deploy_key`. `Problems` holds the
  input problems plus one line per `wrong` or `unknown` item; `Runnable` means no problems.
- **Networks.** As D33: all four missing, or the existing ones all labelled `ix-homelab-networks`, plans
  `app_create` when the app does not exist (compose adopts networks carrying its own project label). When
  the app exists, the same states are `wrong` ("start or redeploy it"), or `unknown` while it is
  DEPLOYING. The app stopped with all four networks present is `differs`.
- **State dataset.** Absent, a file or a symlink is `wrong`; a plain directory, or the wrong owner or mode,
  is `differs` (the orchestrator never changes it, and it is already running from it).
- **Media** absent is `differs` (advisory), per the table.
- **Deploy key** is a readiness item the caller observes (`EnvironmentObserved.DeployKey`, set by the
  server from `ReadDeployKey`). `missing` keeps the environment from being ready but never blocks a run,
  and even `unknown` is not a problem, because the key is not part of the plan.
- **`.global.env` must define all six keys**, `HOMELAB_STORAGE` included (B10 compares six keys), although
  the injected `HOMELAB_STORAGE` wins when set, as everywhere else (D31). All five paths (three roots,
  media, state) must be distinct and not nested. `ORCH_STATE_STORAGE` unset is a blocking problem.
- **Boot pool.** A dataset mounted at `/` or named `boot-pool/...` never counts as the parent of a
  root, so a root outside `/mnt/<pool>` is `wrong` rather than planned on the boot pool.
- **`get-truenas-settings.url`** comes from a new `Deps.TrueNASURL` (display only).
- **`create-deploy-key`** returns 201; `get-deploy-key` returns 200 with `exists: false` and an empty
  path when `ORCH_STATE_STORAGE` is unset (create is then a 422).
- **Fingerprint** is computed in homelab from the authorized_keys text (`homelab.SSHFingerprint`,
  crypto/sha256 + base64), so `EnsureDeployKey` and `ReadDeployKey` need no ssh package.
- **gitrepo.** `gitrepo.NewWithOptions(root, gitrepo.Options{SSHKey, KnownHosts})`; `New` is unchanged.
  `GIT_SSH_COMMAND` is set on fetch and pull only when the key file exists at that moment, so a key
  generated after start-up is used from the next fetch on. Paths are single-quoted for the shell.
- **Dial error messages** (`auth_failed`, `unreachable`, `unsupported_version`) are passed through from
  the client, which by design never puts the key in an error. An unexpected dial error is logged and
  shown as a fixed message.
- **`identity` item (new).** `EnvironmentConfig.CheckIdentity` (and the same on the spec) enables it:
  `ProcessUID`/`ProcessGID`, the uid and gid the orchestrator runs as, are compared with PUID and PGID.
  The zero value skips the check; `serve` sets `CheckIdentity: true` with `os.Getuid()`/`os.Getgid()` for
  a real `wss://` TrueNAS. A uid other than PUID is `wrong` ("the M4 image runs as uid 1000"); a gid
  other than PGID is `differs`.

Review fixes (2026-10-31):

1. A root at `/mnt` or a pool's mountpoint is `wrong` ("must be a child dataset, not the pool root"); no
   `permissions_set` is ever planned on a pool root.
2. App-labelled networks with some missing and no app: `missing` plus `app_create`; apps unreadable:
   `unknown`; `wrong` only when the app exists.
3. PUID/PGID 0 is refused, and so is an id held by a builtin account (568 `apps` and the like), besides 950.
4. A dataset that holds a planned name but is unmounted, locked or mounted elsewhere makes the root
   `wrong`; at apply time an existing dataset is `unchanged` only at the planned mountpoint, otherwise a
   conflict.
5. `GIT_SSH_COMMAND` passes both paths through `-o IdentityFile="…"` and `-o UserKnownHostsFile="…"`
   (`"` and `\` escaped, `%` doubled), single-quoted for the shell. `-i` is not used: ssh takes it
   literally, so it cannot carry a quoted path (checked with `ssh -G`).
6. A whitespace-only root or `ORCH_STATE_STORAGE` is a problem, not a silently dropped root.
7. `freenas-boot` datasets never contain a root, like `boot-pool`.
8. Losing the race to create the deploy key is a 409, never the "recreate the public key" 422.
9. The networks app DEPLOYING with networks absent is `unknown` ("check again").

Follow-up: `gitrepo.Clone` ignores `Options`, so a first clone from the orchestrator cannot use the
deploy key. Bootstrap does the first clone today (B8).

### Implementation notes (2026-10-31)

What landed:

- `internal/homelab`: `truenas.go` (types and the `TrueNAS` interface), `truenasversion.go` (D36),
  `networks.go`, `environment.go` and `environmentcheck.go` (readiness and plan), `bootstrapapply.go`,
  `deploykey.go`.
- `internal/truenas`: `conn.go`, `errors.go`, `decode.go`, `client.go` (websocket JSON-RPC, API-key
  auth), `scenario.go`, `fake.go`, `smoke.go`. `fixtures/truenas/` holds `README.md`, `wire/25.10/*.json`
  and 10 scenarios under `scenarios/*.json`; the envelopes are synthetic.
- `internal/sqlite`: migration `0005_credentials.sql`, queries and `credentials.go`.
- `internal/server`: `truenas.go`, `environment.go`, `deploykey.go`. Operations get/put/delete-truenas-
  settings, get-environment, run-bootstrap, get/create-deploy-key; job kind `environment.bootstrap`; audit
  actions listed in the Ticket 2 notes and in the e2e record below.
- `internal/gitrepo`: `deploykey.go`, `Options` and `NewWithOptions` in `repo.go`, `remote.go`.
- `cmd/orchestrator`: `serve.go` wiring and `smoke.go`.
- `deploy/`: `bootstrap.sh`, `bootstrap.env.example`, `compose.yaml`, `homelab-networks.yaml`,
  `deploy_test.go`; the hand-install guide is [hand-install.md](../hand-install.md).
- Frontend: the `/environment` route, five components and `lib/environment.svelte.ts`.
- `justfile` (`dev_env`, `smoke host`), `scripts/dev-storage.sh` (the dev checkout moved to
  `dist/dev-storage/mnt/tank/apps/homelab`), a Dockerfile comment.
- Decisions made on the way: D37 (PUID 1000 limitation) and D38 (legacy-job fallback); D32–D36 stand.

E2E record (2026-10-31, tech lead; `just check` and `just test` green, all 9 Go packages with `-race
-shuffle`, svelte-check 0/0): built binary with `fake:fresh`: put settings 200 (version TrueNAS-25.10.1);
get-environment runnable with 10 steps; run-bootstrap 202; job succeeded; second get-environment 0 steps
with only `deploy_key` missing; create-deploy-key 201 (modes 0700/0600/0644 on disk); third
get-environment ready true. Audit actions in order: `truenas.credentials.write`, `group.create`,
`user.create`, `dataset.create` x4, `acl.set` x3, `app.create`, `network.create` x4, `bootstrap.run`,
`deploy_key.create`. Zero hits grepping the API key in the server log, job record, audit log and
responses. `fake:ready` is ready with 0 steps. `fake:version-out-of-range`: put settings 422 naming
detected 25.04.2.6 and supported 25.10 through 27.x. Expected, not recorded here: 409 on a stale digest;
`ORCH_SELF_APP=homelab-networks` is a blocking problem and run-bootstrap returns 422.

Reviews: three independent reviews (bootstrap script 16 findings; truenas client 11; domain and server 9
plus the identity check; frontend 11) were all fixed with regression tests. The HIGH findings:

- The media bind mount would have let Docker create `$MEDIA_STORAGE` root-owned.
- A missing pong never closed the websocket, so calls could hang.
- A storage root at a pool's root dataset would have had the pool root chmod'ed to 0750.

UNVERIFIED (each needs `just smoke` or the first real run; items 1-8 are design section 13):

1. Container to host reachability (`host.docker.internal`, nginx, `ui_allowlist`).
2. Go verifying the default UI certificate with `RootCAs` set to it and `ServerName` `localhost`.
3. `legacy_jobs: false` live behaviour (docs and source disagree; see D38).
4. A custom app with explicit network `name:`, `internal: true` and the busybox placeholder installs.
5. Property `.value` case in `pool.dataset.query`, `user.query` `group` row keys, `docker.network.query`
   labels key.
6. `filesystem.setperm` with explicit uid/gid and `stripacl` on an NFSv4 root.
7. `docker.network.query` returning `[]` when docker is unconfigured.
8. The nginx idle timeout closing an idle `/api` websocket after 60 s.
9. `jq` and `openssl` availability on TrueNAS (used by `bootstrap.sh`).
10. `ui_httpsport` in `system.general.config`.
11. `bind.create_host_path: false` accepted by `app.create`.
12. `midclt -j` output format.

Still manual for Martin:

1. `just smoke <host>` with `TRUENAS_API_KEY` in the environment, against the real server.
2. The real-server bootstrap run per design section 12: dry run, apply, re-run ("nothing to do"), and the
   hand-install diff.
3. Browser walkthrough of `/environment` with `just dev`: the `fake:fresh` wizard to Ready;
   `ORCH_TRUENAS_URL=fake:ready just dev-api` for the readiness view; the auth-failure and
   version-out-of-range messages.
4. Confirm D32-D38 and open questions 1-9 in design section 14 (all recommendations were taken).

Follow-ups:

- An any-uid image (nss_wrapper or an entrypoint passwd entry) to lift D37.
- `gitrepo.Clone` ignores `Options`, so a first clone from the orchestrator cannot use the deploy key.
- `--record` fixtures from a real server to replace the synthetic ones.
- Display the known_hosts fingerprint (open question 5).
- The networks app caveat: stopping it removes unused networks.
- A least-privilege API user instead of `truenas_admin`.


[blog-27]: https://www.truenas.com/blog/truenas-27-is-here/
[ge-main]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/main.py
[ge-api]: https://github.com/truenas/middleware/tree/stable/goldeye/src/middlewared/middlewared/api
[master-aliases]: https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/api/aliases.py
[ge-nginx]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/etc_files/local/nginx/nginx.conf.mako
[ge-auth-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/auth.py
[ge-auth]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/auth.py
[ge-product]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/system/product.py
[ge-utils]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/utils/__init__.py
[ee-utils]: https://github.com/truenas/middleware/blob/stable/electriceel/src/middlewared/middlewared/utils/__init__.py
[ge-core]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/service/core_service.py
[ge-app-base]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/base/server/app.py
[ge-method]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/base/server/method.py
[ge-docker-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/docker.py
[ge-ds-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/pool_dataset.py
[ge-ds]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/pool_/dataset.py
[ge-user-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/user.py
[ge-account]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/account.py
[ge-acct-const]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/account_/constants.py
[ge-group-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/group.py
[ge-fs-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/filesystem.py
[ge-fs]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/filesystem.py
[ge-acl]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/filesystem_/acl.py
[ge-fs-types]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/base/types/filesystem.py
[ge-acl-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/acl.py
[ge-net-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/docker_network.py
[doc-net]: https://api.truenas.com/v25.10/api_methods_docker.network.html
[master-net]: https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/plugins/docker/docker_network_crud.py
[ge-app-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/app.py
[ge-apps-crud]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/apps/crud.py
[ge-custom-app]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/plugins/apps/custom_app_utils.py
[ge-apikey-model]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/v25_10_0/api_key.py
[ge-rpc]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/api/base/server/ws_handler/rpc.py
[ge-origin]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/utils/origin.py
[ge-git]: https://github.com/truenas/middleware/blob/stable/goldeye/src/middlewared/middlewared/utils/git.py
[ac-jsonrpc]: https://github.com/truenas/api_client/blob/master/truenas_api_client/jsonrpc.py
[ac-exc]: https://github.com/truenas/api_client/blob/master/truenas_api_client/exc.py
[ac-pyproject]: https://github.com/truenas/api_client/blob/master/pyproject.toml
[doc-jsonrpc]: https://api.truenas.com/v25.10/jsonrpc.html
[doc-jobs]: https://api.truenas.com/v25.10/jobs.html
[tcu-selfsigned]: https://github.com/truenas/truenas_crypto_utils/blob/master/truenas_crypto_utils/generate_self_signed.py
