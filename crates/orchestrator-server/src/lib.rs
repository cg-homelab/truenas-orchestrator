//! HTTP surface for the orchestrator.
//!
//! This crate owns HTTP, auth (M1) and job running (M1) — and no domain logic. Validation lives in
//! `homelab-model`, compose handling in `compose-model`, TrueNAS access behind the
//! `truenas-client` trait. See `docs/architecture/components.md`.

pub mod config;
pub mod health;
pub mod spa;

use axum::{Router, routing::get};
use tower_http::{compression::CompressionLayer, cors::CorsLayer, trace::TraceLayer};
use utoipa::OpenApi;

pub use config::Config;

/// OpenAPI description, served at `/api/openapi.json`.
///
/// The frontend's typed client is generated from this, so a route change that the frontend has not
/// caught up with is a build error rather than a runtime 404.
#[derive(OpenApi)]
#[openapi(
    paths(health::health),
    components(schemas(health::Health)),
    info(
        title = "truenas-orchestrator",
        description = "Homelab orchestration for TrueNAS"
    )
)]
pub struct ApiDoc;

/// Build the application router.
pub fn app(config: &Config) -> Router {
    let mut api = Router::new().route("/health", get(health::health)).route(
        "/openapi.json",
        get(|| async { axum::Json(ApiDoc::openapi()) }),
    );

    // Cross-origin is needed only for the external-client workflow, where the frontend runs on a
    // laptop against this backend. Default is no cross-origin access at all.
    if let Some(origin) = &config.cors_origin {
        match origin.parse::<axum::http::HeaderValue>() {
            Ok(origin) => {
                api = api.layer(
                    CorsLayer::new()
                        .allow_origin(origin)
                        .allow_credentials(true)
                        .allow_methods(tower_http::cors::Any)
                        .allow_headers(tower_http::cors::Any),
                );
            }
            Err(_) => tracing::warn!(%origin, "ignoring unparseable CORS origin"),
        }
    }

    Router::new()
        .nest("/api", api)
        .fallback(spa::serve)
        .layer(CompressionLayer::new())
        .layer(TraceLayer::new_for_http())
}
