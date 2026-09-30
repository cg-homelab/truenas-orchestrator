//! Validator findings.
//!
//! Finding codes are a stable contract with the UI and with users. They are registered in
//! `docs/validator-checks.md` and must never be renumbered or reused.

use serde::{Deserialize, Serialize};

/// How badly a finding affects the stack.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Severity {
    /// Informational only.
    Info,
    /// The stack may work but is wrong or unportable.
    Warn,
    /// The stack cannot work.
    Error,
}

/// Whether a stack is deployable.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "status")]
pub enum StackStatus {
    /// No errors. Deployable.
    Ready,
    /// No errors, but setup steps are outstanding.
    NeedsSetup,
    /// One or more error-severity findings.
    Broken,
}

/// A single validator finding.
///
/// Every finding names a location and suggests a fix. A finding a user cannot act on is a bug.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Finding {
    /// Stable code from `docs/validator-checks.md`, e.g. `ENV003`.
    pub code: String,
    pub severity: Severity,
    /// Path relative to the homelab repo root.
    pub file: String,
    /// 1-indexed line, when the finding anchors to one.
    pub line: Option<u32>,
    pub message: String,
    pub suggested_fix: String,
}
