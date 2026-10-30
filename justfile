set shell := ["bash", "-euo", "pipefail", "-c"]

gobin := `go env GOPATH` + "/bin"
export PATH := gobin + ":" + env("PATH")

dev_env := "ORCH_LISTEN=:8080 ORCH_DB_PATH=dist/dev.db HOMELAB_STORAGE=fixtures/homelab-repo ORCH_SECURE_COOKIES=false"

# List recipes.
default:
    @just --list

# Install pinned tools and frontend dependencies, then build the SPA the Go binary embeds.
setup:
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
    go install mvdan.cc/gofumpt@latest
    go install golang.org/x/vuln/cmd/govulncheck@latest
    cd frontend && npm ci && npm run build

# Read-only checks: lint, format, sqlc drift, API drift, vulnerabilities, frontend types.
check: _frontend-build
    golangci-lint run ./...
    golangci-lint fmt --diff ./...
    go tool sqlc diff
    just check-api
    govulncheck ./...
    cd frontend && npm run check

# Fail if the committed frontend API types differ from the backend's OpenAPI spec.
check-api:
    mkdir -p tmp && go run ./cmd/orchestrator openapi > tmp/openapi.json && (cd frontend && npx openapi-typescript ../tmp/openapi.json -o ../tmp/api.d.ts) && diff -u frontend/src/lib/api.d.ts tmp/api.d.ts

# Regenerate sqlc code, the OpenAPI spec and the frontend API types.
generate:
    go generate ./...
    mkdir -p dist && go run ./cmd/orchestrator openapi > dist/openapi.json && cd frontend && npm run generate:api

# Run Go and frontend tests.
test: _frontend-build
    go test -race -shuffle=on ./...
    cd frontend && npm test --if-present

# Run backend and frontend dev server together.
dev: _frontend-build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p dist
    export {{dev_env}}
    go run ./cmd/orchestrator serve &
    backend=$!
    trap 'kill "$backend" 2>/dev/null || true' EXIT INT TERM
    (cd frontend && npm run dev) &
    wait

# Run the backend only, against the fixture repo.
dev-api:
    mkdir -p dist
    {{dev_env}} go run ./cmd/orchestrator serve

# Run the frontend dev server only.
dev-web:
    cd frontend && npm run dev

# Build the host binary.
build: _frontend-build
    go build -o dist/orchestrator ./cmd/orchestrator

# Build the static linux/amd64 binary.
build-linux: _frontend-build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/linux-amd64/orchestrator ./cmd/orchestrator

# Build the container image from the linux binary.
build-image: build-linux
    docker build --platform linux/amd64 -t truenas-orchestrator:dev .

# Validate a homelab repo checkout.
validate path:
    go run ./cmd/orchestrator validate {{path}}

# Smoke test against a deployed host.
smoke host:
    echo "smoke: not yet implemented (M4)"; exit 1

# Remove build output.
clean:
    rm -rf dist tmp frontend/build

[private]
_frontend-build:
    cd frontend && npm run build
