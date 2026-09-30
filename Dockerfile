# Cross-compiles to linux/amd64 from any host, so a laptop and CI produce identical images and
# nothing needs a cross toolchain installed locally. See docs/decisions.md D12.

# ---- frontend ----------------------------------------------------------------
FROM docker.io/oven/bun:1-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/bun.lock* ./
RUN bun install --frozen-lockfile
COPY frontend/ ./
RUN bun run build

# ---- rust dependency cache ---------------------------------------------------
FROM docker.io/library/rust:1.96-slim-bookworm AS chef
RUN cargo install cargo-chef --locked
WORKDIR /app

FROM chef AS planner
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY crates/ crates/
RUN cargo chef prepare --recipe-path recipe.json

FROM chef AS builder
COPY --from=planner /app/recipe.json recipe.json
RUN cargo chef cook --release --recipe-path recipe.json
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY crates/ crates/
# rust-embed bakes these into the binary, so the frontend must exist before the build.
COPY --from=frontend /app/frontend/build/ frontend/build/
RUN cargo build --release -p orchestrator-server --bin orchestrator

# ---- runtime -----------------------------------------------------------------
FROM docker.io/library/debian:bookworm-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Runs unprivileged. It reaches TrueNAS over the middleware API, not through host privileges.
RUN useradd --system --uid 10001 --create-home orchestrator
USER orchestrator

COPY --from=builder /app/target/release/orchestrator /usr/local/bin/orchestrator

EXPOSE 8080
ENV ORCHESTRATOR_BIND=0.0.0.0:8080
ENTRYPOINT ["/usr/local/bin/orchestrator"]
