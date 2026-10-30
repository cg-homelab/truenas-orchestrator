#!/usr/bin/env bash
# Materialise fixtures/homelab-repo as a real git repository in a temporary directory.
#
# The fixture tree is committed as plain files (a nested .git is awkward to version), so tests that
# need git history, branch state or tracked-file state call this first.
#
# Prints the path of the created repository on stdout.
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/fixtures/homelab-repo"
dest="${1:-$(mktemp -d)}"

mkdir -p "$dest"
cp -R "$src/." "$dest/"

# The fixture's gitignore is stored undotted. A real .gitignore inside fixtures/ would apply to THIS
# repository too and would stop the fixture's own .env files from ever being committed, leaving a
# fresh clone with a silently different fixture. Restore the dot only in the materialised copy.
mv "$dest/gitignore" "$dest/.gitignore"
rm -f "$dest/FIXTURE.md"

git -C "$dest" init --quiet --initial-branch=main
git -C "$dest" config user.email "fixture@example.invalid"
git -C "$dest" config user.name "Fixture"

git -C "$dest" add -A
git -C "$dest" commit --quiet -m "Fixture homelab repo"

# GIT001: a real env file tracked by git despite the gitignore rule. Deliberate — the validator must
# report it. See fixtures/homelab-repo/FIXTURE.md.
git -C "$dest" add -f compose/env-drift/.env
git -C "$dest" commit --quiet -m "Track an env file, which the validator must flag as GIT001"

echo "$dest"
