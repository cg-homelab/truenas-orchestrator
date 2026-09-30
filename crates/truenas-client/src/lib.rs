//! TrueNAS middleware client.
//!
//! [`TrueNasApi`] is a trait from day one, and nothing else in the workspace depends on a concrete
//! implementation. That is what lets the whole system run and test locally with no TrueNAS — see
//! `docs/decisions.md` D12.
//!
//! The real websocket JSON-RPC implementation and the recorded-fixture [`FakeTrueNas`] both arrive
//! in M3. M0 defines the boundary only.

pub mod fake;

pub use fake::FakeTrueNas;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

/// Errors from any TrueNAS operation.
#[derive(Debug, thiserror::Error)]
pub enum TrueNasError {
    /// The middleware API version is outside the range this client supports.
    ///
    /// Reported explicitly rather than surfacing as a deserialization failure, so the user learns
    /// what to do about it.
    #[error("unsupported TrueNAS middleware version {detected}, supported: {supported}")]
    UnsupportedVersion { detected: String, supported: String },

    #[error("not implemented until M3: {0}")]
    NotImplemented(&'static str),
}

/// A TrueNAS ZFS dataset.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Dataset {
    pub name: String,
    pub mountpoint: Option<String>,
}

/// A docker network known to TrueNAS.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Network {
    pub name: String,
}

/// Operations the orchestrator performs against a TrueNAS server.
///
/// Every mutating method is expected to be preceded by a dry-run preview and an explicit user
/// confirm at the call site — the API key this client holds can damage storage
/// (`docs/decisions.md` D9).
#[async_trait]
pub trait TrueNasApi: Send + Sync {
    /// Middleware version string, used for the compatibility gate.
    async fn system_version(&self) -> Result<String, TrueNasError>;

    async fn list_datasets(&self) -> Result<Vec<Dataset>, TrueNasError>;

    async fn list_networks(&self) -> Result<Vec<Network>, TrueNasError>;
}
