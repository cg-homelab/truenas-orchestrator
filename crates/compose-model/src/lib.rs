//! Parsing and rendering of `compose.yaml`.
//!
//! Two responsibilities, both implemented in M1:
//!
//! 1. Extract every `${VAR}` reference **tagged with the scope of the position it appears in**.
//!    Interpolation scope and container scope are separate and do not share values — see
//!    `docs/architecture/env-scopes.md`, which is required reading before touching this crate.
//! 2. Render deploy manifests and `compose.override.yaml` overlays (M4).
//!
//! `compose.yaml` is never rewritten. Customization is emitted as a separate overlay file so the
//! hand-written comments in these files survive.
//!
//! The YAML parser is not yet chosen; it must preserve line numbers, because every finding anchors
//! to a line.

/// Which of Compose's two environment scopes a variable reference sits in.
///
/// See `docs/architecture/env-scopes.md`. Conflating these is the defect class this project exists
/// to catch.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Scope {
    /// Fills `${VAR}` inside `compose.yaml` itself: `volumes:`, `labels:`, `image:`,
    /// `container_name:`, `command:`, `ports:`, `secrets: file:`.
    ///
    /// Sourced from the compose project environment, **never** from an `env_file:` key.
    Interpolation,
    /// Injected into the running container by `env_file:` and `environment:`.
    ///
    /// The compose file itself never sees these values.
    Container,
}
