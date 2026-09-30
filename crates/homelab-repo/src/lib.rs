//! Git operations and on-disk layout walking for the homelab repository.
//!
//! The only crate that touches the homelab repo on disk. Clones, fetches, pulls, branches, commits
//! and diffs; walks the layout defined in `docs/architecture/repo-layout-contract.md` and hands
//! content to `homelab-model`, which does the reasoning.
//!
//! Enforces that real env files (`.global.env`, `<app>/.env`) stay gitignored.
//!
//! Implemented in M1.
