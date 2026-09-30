//! Serves the embedded SvelteKit SPA.
//!
//! The frontend is built to `frontend/build` and embedded with `rust-embed`, so the release binary
//! serves the UI with no `frontend/` directory present at runtime — the TrueNAS app is one
//! container (`docs/decisions.md` D2).
//!
//! Unknown paths fall back to `index.html` so client-side routing works on a hard refresh. Unknown
//! paths under `/api` do not: they must 404 as API calls, not return HTML.

use axum::{
    http::{StatusCode, Uri, header},
    response::{IntoResponse, Response},
};
use rust_embed::Embed;

#[derive(Embed)]
#[folder = "$CARGO_MANIFEST_DIR/../../frontend/build"]
struct Assets;

/// Serve an embedded asset, falling back to `index.html` for client-side routes.
pub async fn serve(uri: Uri) -> Response {
    let path = uri.path().trim_start_matches('/');

    // An unmatched /api path is a missing route, not a client-side one.
    if path == "api" || path.starts_with("api/") {
        return (StatusCode::NOT_FOUND, "no such API route").into_response();
    }

    let path = if path.is_empty() { "index.html" } else { path };

    match Assets::get(path).or_else(|| Assets::get("index.html")) {
        Some(file) => {
            let mime = mime_guess::from_path(path).first_or_octet_stream();
            ([(header::CONTENT_TYPE, mime.as_ref())], file.data).into_response()
        }
        // Only reachable when the frontend was never built into the binary.
        None => (
            StatusCode::NOT_FOUND,
            "frontend not built into this binary; run `just build`",
        )
            .into_response(),
    }
}
