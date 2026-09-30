# truenas-orchestrator — developer commands.
#
# `just dev` is the only command needed to start. Nothing in the default loop touches TrueNAS:
# the inner loop runs against fixtures/homelab-repo and a fake middleware. See docs/decisions.md D12.

set shell := ["bash", "-uc"]
set dotenv-load := true

# Where `vite dev` sends /api.
export ORCHESTRATOR_API := env_var_or_default("ORCHESTRATOR_API", "http://127.0.0.1:8080")
# Homelab repo the backend reads during development.
export ORCHESTRATOR_HOMELAB_REPO := env_var_or_default("ORCHESTRATOR_HOMELAB_REPO", justfile_directory() / "fixtures/homelab-repo")

# List available recipes
default:
    @just --list

# ---------------------------------------------------------------- setup

# Install toolchains, cargo tools and frontend deps from a clean checkout
setup:
    rustup show active-toolchain
    cargo install cargo-nextest --locked
    cargo install cargo-watch --locked
    cd frontend && bun install
    @echo "ready — run 'just dev'"

# ---------------------------------------------------------------- dev

# Run backend and frontend together (the normal inner loop)
dev:
    #!/usr/bin/env bash
    set -uo pipefail
    trap 'kill 0' EXIT INT TERM
    just dev-backend &
    just dev-frontend &
    wait

# Backend only, rebuilding on change, against the fixture homelab repo
dev-backend:
    cargo watch -x 'run -p orchestrator-server --bin orchestrator -- --bind 127.0.0.1:8080'

# Frontend only, proxying /api to $ORCHESTRATOR_API
dev-frontend:
    cd frontend && bun run dev

# usage: just dev-remote truenas.example.com:8080
# Frontend against a real backend on HOST — the external-client workflow
dev-remote host:
    cd frontend && ORCHESTRATOR_API="http://{{host}}" bun run dev

# Standalone fake TrueNAS middleware (M3)
fake-truenas:
    @echo "not yet implemented — arrives in M3 (docs/milestones/m3-truenas-client-bootstrap.md)"

# ---------------------------------------------------------------- quality

# Format, lint and type-check everything. Fails on any warning
check: check-rust check-frontend

check-rust:
    cargo fmt --all -- --check
    cargo clippy --workspace --all-targets -- -D warnings

check-frontend:
    cd frontend && bun run check
    cd frontend && bun run lint

# Format everything in place
fmt:
    cargo fmt --all
    cd frontend && bun run format

# Run all tests
test: test-rust test-frontend

test-rust:
    cargo nextest run --workspace

test-frontend:
    cd frontend && bun run test

# Validator tests over the fixtures only — the M1 inner loop
test-validator:
    cargo nextest run -p homelab-model -p compose-model

# Accept changed insta snapshots (M1)
snapshot-accept:
    cargo insta accept

# ---------------------------------------------------------------- run

# usage: just validate ../martin-homelab
# Validate a homelab repo from the CLI (M1)
validate path:
    @echo "not yet implemented — arrives in M1 (docs/milestones/m1-validator.md)"
    @echo "would validate: {{path}}"

# ---------------------------------------------------------------- database

# Apply migrations to the dev database
#
# Migrations are embedded in the binary and applied on connect, so this exists for the case where
# you want them applied without starting anything. Deployments migrate themselves.
db-migrate:
    cargo test -p orchestrator-db -- --nocapture

# Delete the dev database; it is recreated and migrated on next connect
db-reset:
    rm -f orchestrator.dev.db orchestrator.dev.db-shm orchestrator.dev.db-wal
    @echo "dev database removed — recreated on next connect"

# Regenerate offline query data so CI builds without a database.
# Only needed once query!/query_as! macros exist (M1). Requires: cargo install sqlx-cli
sqlx-prepare:
    cargo sqlx prepare --workspace -- --all-targets

# ---------------------------------------------------------------- api types

# Regenerate the frontend's typed client from the backend's OpenAPI description
gen-api:
    cargo run -q -p orchestrator-server --bin openapi-dump > openapi.json
    cd frontend && bun run gen-api

# Fail if the generated client is stale — CI guard against route drift
check-api: gen-api
    git diff --exit-code openapi.json frontend/src/lib/api/schema.d.ts

# ---------------------------------------------------------------- build

# Release build: frontend first, then the binary that embeds it
build:
    cd frontend && bun run build
    cargo build --release -p orchestrator-server

# Cross-compile a linux/amd64 image from this darwin/arm64 host
build-image tag="truenas-orchestrator:dev":
    docker buildx build --platform linux/amd64 -t {{tag}} --load .

# Tag, build and push a release image
release tag:
    git tag -a "v{{tag}}" -m "v{{tag}}"
    just build-image "ghcr.io/cg-homelab/truenas-orchestrator:{{tag}}"
    docker push "ghcr.io/cg-homelab/truenas-orchestrator:{{tag}}"

# ---------------------------------------------------------------- truenas

# Read-only calls against a real TrueNAS to verify API compatibility (M3). Never in the default loop
smoke host:
    @echo "not yet implemented — arrives in M3 (docs/milestones/m3-truenas-client-bootstrap.md)"
    @echo "would smoke-test: {{host}}"

# ---------------------------------------------------------------- fixtures

# Materialise the fixture homelab repo as a real git repo, printing its path
fixture-repo dest="":
    @./scripts/init-fixture-repo.sh {{dest}}
