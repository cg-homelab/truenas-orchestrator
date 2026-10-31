# Hand-install guide

This guide prepares a TrueNAS server for truenas-orchestrator by hand. It covers the same ground as
[`deploy/bootstrap.sh`](../deploy/bootstrap.sh), with the same step ids in the same order, and both routes
reach the same end state (D11). The orchestrator's environment-readiness check (`/environment`) accepts
either one. Use this guide if you want to see every change before you make it, or if the script stops at
a step you would rather finish yourself. Steps already done by an earlier run of the script, or by hand,
are checked and skipped in the same way.

The end state is defined in [decisions.md](decisions.md) D32 (identity, datasets, permissions, deploy
key) and D33 (networks). The orchestrator never manages its own TrueNAS app entry (D11).

## Prerequisites

- TrueNAS 25.10 through 27.x (D36), with an apps pool configured.
- A shell on the TrueNAS host as root: **System > Shell** and `sudo -i`, or SSH and `sudo -i`.
- A checkout of this repository's `deploy/` directory on the host. It holds `homelab-networks.yaml` and
  `compose.yaml`, which you paste in B6 and B13.
- Your inputs. Fill in [`deploy/bootstrap.env.example`](../deploy/bootstrap.env.example) as a worksheet
  and save it as `deploy/bootstrap.env`. You can then check your work at any point with
  `deploy/bootstrap.sh --dry-run`, which only reads.

| Input | Example | Meaning |
|---|---|---|
| `HOMELAB_STORAGE` | `/mnt/tank/apps/homelab` | The homelab repo checkout |
| `APPCONFIG_STORAGE` | `/mnt/tank/apps/appconfigs` | Per-app config and secrets |
| `DATABASE_STORAGE` | `/mnt/tank/databases` | Database data directories |
| `MEDIA_STORAGE` | `/mnt/tank/media` | Media: user data, only checked |
| `ORCH_STATE_STORAGE` | `/mnt/tank/apps/orchestrator` | The orchestrator's state dataset (D10) |
| `PUID`, `PGID` | `1000`, `1000` | The one homelab identity (D32). `PUID` must be 1000 in M4 (B3) |
| `REPO_URL` | `git@github.com:you/homelab.git` | The homelab git repository (SSH URL, no embedded credentials) |
| `ORCH_IMAGE` | `ghcr.io/cg-homelab/truenas-orchestrator:<version>` | The orchestrator image |
| `ORCH_APP_NAME` | `truenas-orchestrator` | The orchestrator's TrueNAS app name |
| `TRUENAS_API_USER` | `truenas_admin` | The user that owns the orchestrator's API key |

The commands below use these names as shell variables. Load them into your root shell once:

```sh
cd /path/to/deploy
set -a; . ./bootstrap.env; set +a
: "${ORCH_APP_NAME:=truenas-orchestrator}" "${TRUENAS_API_USER:=truenas_admin}"
```

Stay in this shell for the rest of the guide: its default umask (022) is the one `bootstrap.sh` uses for
the B8 clone, so both routes leave the same file modes.

## B0 Preflight

**Check.** The Dashboard shows a version from 25.10 through 27.x (`midclt call system.version`). Apps >
Configuration shows a pool, and `midclt call docker.status` reports `RUNNING`. Your inputs are loaded
(Prerequisites), and:

- The storage roots are absolute paths below `/mnt/<pool>/`, all different, and none inside another.
- `PUID` and `PGID` are integers, neither is 0 (root) or 950 (`truenas_admin`), and `PUID` is 1000 (B3).
- `REPO_URL` carries no credentials (no `https://user:token@…`): access is the B7 deploy key.

**Do.** If no apps pool is set: **Apps > Configuration > Choose Pool**. On an older TrueNAS, upgrade
first.

**After.** Version in range and Docker running. Nothing else changes in this step.

## B1 Media check

**Check.** `MEDIA_STORAGE` exists (**Datasets**, or `ls -ld "$MEDIA_STORAGE"`).

**Do.** Nothing here. Media is user data, often shared over SMB with its own ACLs: it is never created
or chowned by this procedure (D32). It must exist before B13, though: the orchestrator app mounts it
read-only with `create_host_path: false`, so Docker cannot create it (root-owned) behind your back, and
`bootstrap.sh` refuses to install the app while it is missing. Create it yourself, as a dataset or a
directory, with the ownership your media shares need.

## B2 Group

**Check.** `midclt call group.query '[["gid","=",'"$PGID"']]'`. If a group already holds gid `PGID`, under
any name, it is used as-is: skip to B3. If a group named `homelab` holds a different gid, stop and resolve
that first (change `PGID` or rename the group).

**Do.** **Credentials > Groups > Add**: name `homelab`, GID `PGID` (1000–2999, not 950), Samba
Authentication off. Save.

**After.** `midclt call group.query '[["gid","=",'"$PGID"']]'` returns the group.

## B3 User

**Check.** `midclt call user.query '[["uid","=",'"$PUID"']]'`. A user that already holds uid `PUID` is used
as-is: skip to B4. A user named `homelab` with a different uid must be resolved first.

`PUID` must be 1000 in M4 (decided in the M4 review): the published orchestrator image has a passwd entry
only for uid 1000, and `ssh` (git fetch with the deploy key) needs one for the uid it runs as. Images that
run as any uid are a follow-up. `PGID` only needs to be in 1000–2999 when the group is new.

**Do.** **Credentials > Users > Add**:

- Username `homelab`, full name `Homelab containers (truenas-orchestrator)`, UID `PUID` (1000).
- Primary group: the B2 group. Do not create a new primary group.
- Disable Password on. SMB User off. Shell `nologin` (`/usr/sbin/nologin`). Home directory `/var/empty`.

**After.** `midclt call user.query '[["uid","=",'"$PUID"']]'` returns the user, with the B2 group as its
primary group.

## B4 Datasets

**Check.** For each of `HOMELAB_STORAGE`, `APPCONFIG_STORAGE`, `DATABASE_STORAGE` and `ORCH_STATE_STORAGE`,
find the deepest existing dataset whose mountpoint contains the path (**Datasets**, or
`midclt call pool.dataset.query '[["type","=","FILESYSTEM"]]' | jq -r '.[] | "\(.name) \(.mountpoint)"'`).

- The path is a dataset's mountpoint: nothing to do. If its properties differ from the list below, leave
  them; the difference is reported, never changed.
- The path is an existing plain directory: accepted as-is (reported as differing). Go on to B5.
- The path is missing and a directory between that dataset and the path already exists as a plain
  directory: a dataset cannot be created there. Stop and resolve it (move the directory, or make it a
  dataset).

**Do.** Missing parents first, created as plain datasets that inherit everything: select the containing
dataset, **Add Dataset**, the next path component as the name, defaults. Then each root: select its
parent, **Add Dataset**, the last path component as the name, Dataset Preset **Generic**, then **Advanced
Options**:

| Property | Value |
|---|---|
| ACL Type | POSIX |
| ACL Mode | Discard |
| Atime | Off |
| Compression | LZ4 |
| Record Size | 128K, except `DATABASE_STORAGE`: 16K |

**After.** All four paths are dataset mountpoints, and new ones carry the properties above.

## B5 Permissions

**Check.** `midclt call filesystem.stat "\"$path\"" | jq '{uid, gid, mode: (.mode % 4096), acl}'` for each
root. Wanted: uid `PUID`, gid `PGID`, mode `0750` (decimal 488) for `HOMELAB_STORAGE`, `APPCONFIG_STORAGE`
and `DATABASE_STORAGE`, `0700` (decimal 448) for `ORCH_STATE_STORAGE`, and `acl: false`.

**Do.** **Datasets**, select the root, **Permissions > Edit**:

- If an ACL is present, **Strip ACL** first.
- User `homelab` (uid `PUID`) and group `homelab` (gid `PGID`), with Apply User and Apply Group.
- Mode `750` (state: `700`).
- **Not** recursive: this changes only the root directory, never its contents.

The same from the shell, one root at a time:

```sh
midclt call -j filesystem.setperm '{"path": "'"$APPCONFIG_STORAGE"'", "uid": '"$PUID"', "gid": '"$PGID"', "mode": "750", "options": {"stripacl": true, "recursive": false, "traverse": false}}'
```

**After.** The check above shows the wanted owner, mode and `acl: false` on all four roots.

## B6 Networks

**Check.** List the four networks with the compose project that owns each, and look for the app:

```sh
midclt call docker.network.query '[["name","in",["home","proxy","isolated","databases"]]]' |
  jq -r '.[] | "\(.name) \((.labels // {})["com.docker.compose.project"] // "(made by hand)")"'
midclt call app.query '[["name","=","homelab-networks"]]' | jq -r '.[] | "\(.name) \(.state)"'
```

- All four exist, however they were made: nothing to do.
- Some are missing and the `homelab-networks` app exists: it is stopped. Start it in **Apps**; do not
  install it again.
- None exists, or every one that exists belongs to `ix-homelab-networks`, and the app does not exist:
  install the app below.
- Some were made by hand and others are missing: compose cannot adopt hand-made networks, so create the
  missing ones by hand (`docker network create <name>`; `docker network create --internal isolated`).

**Do.** **Apps > Discover Apps > ⋮ > Install via YAML**: name `homelab-networks`, paste
[`deploy/homelab-networks.yaml`](../deploy/homelab-networks.yaml) unchanged, Save.

**After.** The app is Running and the network query lists all four. Do not stop or delete this app while
any stack runs: stopping it removes unused networks (D33).

## B7 Deploy key

**Check.** `stat -c '%n %u:%g %a' "$ORCH_STATE_STORAGE/ssh" "$ORCH_STATE_STORAGE/ssh/id_ed25519"
"$ORCH_STATE_STORAGE/ssh/id_ed25519.pub"`. If all three exist with owner `PUID:PGID` and modes `700`,
`600` and `644`, skip to B8. If they exist with another owner or mode (an interrupted run, a skipped
`chown`), run only the `chown` and `chmod` lines below. Never overwrite an existing key.

**Do.** In the root shell:

```sh
install -d -m 700 -o "$PUID" -g "$PGID" "$ORCH_STATE_STORAGE/ssh"
ssh-keygen -q -t ed25519 -N '' -C "truenas-orchestrator@$(hostname)" -f "$ORCH_STATE_STORAGE/ssh/id_ed25519"
chown "$PUID:$PGID" "$ORCH_STATE_STORAGE/ssh" "$ORCH_STATE_STORAGE/ssh/id_ed25519" "$ORCH_STATE_STORAGE/ssh/id_ed25519.pub"
chmod 700 "$ORCH_STATE_STORAGE/ssh"
chmod 600 "$ORCH_STATE_STORAGE/ssh/id_ed25519"
chmod 644 "$ORCH_STATE_STORAGE/ssh/id_ed25519.pub"
cat "$ORCH_STATE_STORAGE/ssh/id_ed25519.pub"
```

Add the printed public key to the repository as a **read-only** deploy key (GitHub: Settings > Deploy
keys > Add deploy key, write access off).

**After.** The check above shows `PUID:PGID` with `700`, `600` and `644`. Never share the private half.

## B8 Repo

**Check.** `HOMELAB_STORAGE` is empty, or already a clone of `REPO_URL`
(`git -c safe.directory="$HOMELAB_STORAGE" -C "$HOMELAB_STORAGE" remote get-url origin`) whose `.git` is
owned by `PUID:PGID` (`stat -c '%u:%g' "$HOMELAB_STORAGE/.git"`). A clone with another owner only needs
the `chown -R` line below. A clone of another remote, or a non-empty directory that is not a clone, must
be resolved first.

**Do.** If empty, in the shell from Prerequisites (inputs loaded, umask 022):

```sh
GIT_SSH_COMMAND="ssh -i $ORCH_STATE_STORAGE/ssh/id_ed25519 -o IdentitiesOnly=yes -o UserKnownHostsFile=$ORCH_STATE_STORAGE/ssh/known_hosts -o StrictHostKeyChecking=accept-new" \
  git -c safe.directory="$HOMELAB_STORAGE" clone -- "$REPO_URL" "$HOMELAB_STORAGE"
chown -R "$PUID:$PGID" "$HOMELAB_STORAGE"
chown "$PUID:$PGID" "$ORCH_STATE_STORAGE/ssh/known_hosts" && chmod 644 "$ORCH_STATE_STORAGE/ssh/known_hosts"
```

**After.** `HOMELAB_STORAGE` holds the checkout, owned by `PUID:PGID`, with `origin` equal to `REPO_URL`.

## B9 known_hosts

**Check.** `$ORCH_STATE_STORAGE/ssh/known_hosts` exists.

**Do.** Nothing. The B8 clone writes it on first contact (`accept-new`, trust on first use). If you cloned
over HTTPS, the orchestrator's first fetch writes it instead.

## B10 .global.env

**Check.** `$HOMELAB_STORAGE/compose/.global.env` exists, is owned by `PUID:PGID` with mode `600`
(`stat -c '%u:%g %a'`), and its `PUID`, `PGID`, `HOMELAB_STORAGE`, `APPCONFIG_STORAGE`,
`DATABASE_STORAGE` and `MEDIA_STORAGE` equal your inputs. Values are compared literally: write each as a
plain value (a trailing ` # comment` is fine), not as `${OTHER_VAR}`. If any of the six differ, decide
which side is right and make the two agree; bootstrap never edits this file. If only the owner or mode is
wrong, fix those with `chown "$PUID:$PGID"` and `chmod 600`.

**Do.** If absent, copy the template, set the six keys to your inputs and leave the rest as the template
has it:

```sh
install -m 600 -o "$PUID" -g "$PGID" "$HOMELAB_STORAGE/compose/example.global.env" "$HOMELAB_STORAGE/compose/.global.env"
vi "$HOMELAB_STORAGE/compose/.global.env"
```

**After.** `ls -l` shows `-rw-------` owned by `PUID:PGID`, and the six keys match.

## B11 API key

**Check.** **Credentials > Users > `TRUENAS_API_USER` > API Keys** lists a key named
`truenas-orchestrator`, or `midclt call api_key.query '[["name","=","truenas-orchestrator"]]' | jq length`
is 1. If it exists but you no longer have its value, delete it and create a new one.

**Do.** **Credentials > Users > `TRUENAS_API_USER` > API Keys > Add**: name `truenas-orchestrator`, no
expiry. TrueNAS shows the key once: paste it into the orchestrator's first-run wizard after B13, and do not
save it anywhere else. (`bootstrap.sh --create-api-key` does the same and prints it once.)

**After.** The key is listed. Its value exists only in the orchestrator's database, once you have entered
it there.

## B12 UI certificate

**Check.** `$ORCH_STATE_STORAGE/truenas-ca.pem` exists, is owned by `PUID:PGID` with mode `644`, and
equals the certificate the web UI uses.

**Do.** From the shell (only the public certificate is extracted; the commands stop if TrueNAS has no UI
certificate):

```sh
midclt call system.general.config | jq -er '.ui_certificate.certificate // empty' > /tmp/truenas-ca.pem &&
  install -m 644 -o "$PUID" -g "$PGID" /tmp/truenas-ca.pem "$ORCH_STATE_STORAGE/truenas-ca.pem"
rm -f /tmp/truenas-ca.pem
```

Or in the UI: **System > General Settings > GUI** names the certificate; **System > Certificates**, that
certificate, **Download**, and copy the certificate file (not the key) to the path above with the same
owner and mode. Repeat after replacing the UI certificate.

Then note the two values B13 needs: how the orchestrator reaches the UI, and the name it verifies the
certificate against.

```sh
TRUENAS_UI_PORT=$(midclt call system.general.config | jq -r '.ui_httpsport')
openssl x509 -in "$ORCH_STATE_STORAGE/truenas-ca.pem" -noout -ext subjectAltName
```

Set `TRUENAS_TLS_SERVER_NAME=localhost` if the DNS names include `localhost` (the default
`truenas_default` certificate), otherwise the first DNS name that is not a wildcard. If there is no such
name, stop: select a UI certificate with a DNS name in **System > General Settings > GUI** and repeat
this step.

**After.** `stat -c '%u:%g %a' "$ORCH_STATE_STORAGE/truenas-ca.pem"` shows `PUID:PGID 644`, and
`TRUENAS_UI_PORT` and `TRUENAS_TLS_SERVER_NAME` are set in your shell.

## B13 Orchestrator app

**Check.** **Apps** has no app named `ORCH_APP_NAME`. If it exists, leave it alone: neither bootstrap nor
the orchestrator ever modifies it (D11). Before installing, all five roots must exist, `MEDIA_STORAGE`
included (B1): every mount uses `create_host_path: false`, so a missing root makes the app fail instead
of Docker creating it root-owned. `TRUENAS_TLS_SERVER_NAME` from B12 must not be empty.

**Do.** Render [`deploy/compose.yaml`](../deploy/compose.yaml). Every `${NAME}` placeholder in it is one
of your inputs (`ORCH_APP_NAME` fills `ORCH_SELF_APP`) or one of the two B12 values:

```sh
sed -e "s|\${ORCH_IMAGE}|$ORCH_IMAGE|g" -e "s|\${PUID}|$PUID|g" -e "s|\${PGID}|$PGID|g" \
    -e "s|\${HOMELAB_STORAGE}|$HOMELAB_STORAGE|g" -e "s|\${APPCONFIG_STORAGE}|$APPCONFIG_STORAGE|g" \
    -e "s|\${DATABASE_STORAGE}|$DATABASE_STORAGE|g" -e "s|\${MEDIA_STORAGE}|$MEDIA_STORAGE|g" \
    -e "s|\${ORCH_STATE_STORAGE}|$ORCH_STATE_STORAGE|g" -e "s|\${ORCH_APP_NAME}|$ORCH_APP_NAME|g" \
    -e "s|\${TRUENAS_UI_PORT}|$TRUENAS_UI_PORT|g" -e "s|\${TRUENAS_TLS_SERVER_NAME}|$TRUENAS_TLS_SERVER_NAME|g" \
    compose.yaml
```

Check that the output has no `${` left, that `ORCH_TRUENAS_URL` ends in your UI port, and that
`ORCH_TRUENAS_TLS_SERVER_NAME` is the B12 name. Then **Apps > Discover Apps > ⋮ > Install via YAML**:
name `ORCH_APP_NAME`, paste the rendered YAML, Save.

**After.** The app is Running. It joins the external `proxy` network for your reverse proxy, and publishes
`127.0.0.1:30808` on the host for break-glass access over SSH:
`ssh -L 30808:127.0.0.1:30808 <truenas>`, then `http://localhost:30808`.

## Verify

1. Open the orchestrator, sign in, and go to **`/environment`**. Enter the B11 API key in the first-run
   wizard (Connect). The page must show the connection as ok, your TrueNAS version inside the supported
   range, and the environment as **Ready** with zero planned steps.
2. Run `deploy/bootstrap.sh --dry-run` with the same `bootstrap.env`. It must print `nothing to do`
   (B11 may show `skip` if you created the key by hand; that is not a change).
3. To compare this route with the script (milestone M4 acceptance criterion 3): on a server prepared by
   `bootstrap.sh`, save the JSON of `GET /api/environment` (signed in, from the browser) as
   `after-bootstrap.json`; on the hand-installed one, save it as `after-hand-install.json`. Normalise the
   paths and diff:

   ```sh
   norm() { jq -S 'del(.digest, .connection.message)' "$1" | sed -e "s|$2|<ROOT>|g"; }
   diff <(norm after-bootstrap.json /mnt/tank) <(norm after-hand-install.json /mnt/scratch)
   ```

   The item states and the step lists must match. The full manual procedure is in
   [milestones/m4-truenas-client-bootstrap.md](milestones/m4-truenas-client-bootstrap.md), Design,
   section 12.
