#!/usr/bin/env bash
# Point a materialised dev homelab repo at local storage roots.
#
# Usage: dev-storage.sh <repo-dir> <storage-dir>
#
# Creates appconfigs, databases and media under <storage-dir> and rewrites the storage keys in
# <repo-dir>/compose/.global.env to absolute paths, so setup can write somewhere real on a dev machine.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <repo-dir> <storage-dir>" >&2
  exit 2
fi

repo="$(cd "$1" && pwd)"
mkdir -p "$2"
storage="$(cd "$2" && pwd)"
env_file="$repo/compose/.global.env"

mkdir -p "$storage/appconfigs" "$storage/databases" "$storage/media"

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

set_key APPCONFIG_STORAGE "$storage/appconfigs"
set_key DATABASE_STORAGE "$storage/databases"
set_key MEDIA_STORAGE "$storage/media"
set_key HOMELAB_STORAGE "$repo"
