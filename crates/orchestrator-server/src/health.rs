//! Health endpoint. Unauthenticated by design — it is the liveness probe.

use axum::Json;
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;

/// Build and version information.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct Health {
    /// Always `"ok"` when the server is serving.
    pub status: &'static str,
    /// Crate version of the running binary.
    pub version: &'static str,
    /// Milestone this build implements, so the UI can say what it is.
    pub milestone: &'static str,
}

/// Liveness and build info.
#[utoipa::path(
    get,
    path = "/api/health",
    responses((status = 200, description = "Server is healthy", body = Health)),
)]
pub async fn health() -> Json<Health> {
    Json(Health {
        status: "ok",
        version: env!("CARGO_PKG_VERSION"),
        milestone: "M0",
    })
}
