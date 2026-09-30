//! Pure domain model for the homelab repository.
//!
//! **This crate performs no I/O.** No filesystem, no network, no database. It takes already-read
//! content in and returns findings out. That constraint is what makes the validator testable from
//! in-memory fixtures with plain unit tests — see `docs/architecture/components.md`.
//!
//! Implemented in M1. See `docs/milestones/m1-validator.md`.

pub mod finding;

pub use finding::{Finding, Severity, StackStatus};
