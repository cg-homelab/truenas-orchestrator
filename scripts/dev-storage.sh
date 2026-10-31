#!/usr/bin/env bash
# Point a materialised dev homelab repo at local storage roots laid out like the fake TrueNAS's pool.
#
# Usage: dev-storage.sh <repo-dir> <storage-dir>
#
# The fake TrueNAS scenarios (fixtures/truenas/scenarios) put pool tank at @MNT@/tank, and `just dev`
# sets ORCH_TRUENAS_FAKE_MNT=<storage-dir>/mnt. This creates, under <storage-dir>/mnt/tank:
#
#   apps/appconfigs   APPCONFIG_STORAGE  0750
#   databases         DATABASE_STORAGE   0750
#   media             MEDIA_STORAGE      0755
#   orchestrator      ORCH_STATE_STORAGE 0700
#
# and rewrites the storage keys in <repo-dir>/compose/.global.env to those absolute paths, with
# HOMELAB_STORAGE set to <repo-dir>. For the environment check against the fake to pass, <repo-dir>
# should be <storage-dir>/mnt/tank/apps/homelab (the scenarios' tank/apps/homelab dataset), as
# `just dev` arranges.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <repo-dir> <storage-dir>" >&2
  exit 2
fi

repo="$(cd "$1" && pwd)"
mkdir -p "$2"
storage="$(cd "$2" && pwd)"
tank="$storage/mnt/tank"
env_file="$repo/compose/.global.env"

mkdir -p "$tank/apps/appconfigs" "$tank/databases" "$tank/media" "$tank/orchestrator"
chmod 0750 "$tank/apps/appconfigs" "$tank/databases"
chmod 0755 "$tank/media"
chmod 0700 "$tank/orchestrator"

if [ "$repo" != "$tank/apps/homelab" ]; then
  echo "note: $repo is not $tank/apps/homelab; the fake TrueNAS will report HOMELAB_STORAGE as not on a pool" >&2
fi

set_key() {
  local key="$1" value="$2" tmp
  tmp="$(mktemp)"
  awk -v key="$key" -v value="$value" '
    index($0, key "=") == 1 { print key "=" value; found = 1; next }
    { print }
    END { if (!found) print key "=" value }
  ' "$env_file" >"$tmp"
  cat "$tmp" >"$env_file"
  rm -f "$tmp"
}

set_key APPCONFIG_STORAGE "$tank/apps/appconfigs"
set_key DATABASE_STORAGE "$tank/databases"
set_key MEDIA_STORAGE "$tank/media"
set_key HOMELAB_STORAGE "$repo"
