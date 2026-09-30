//! Writes the OpenAPI description to stdout.
//!
//! Lets `just gen-api` regenerate the frontend's typed client without starting a server, so the
//! generated client cannot drift from the routes in this binary.

use orchestrator_server::ApiDoc;
use utoipa::OpenApi as _;

fn main() -> anyhow::Result<()> {
    println!("{}", ApiDoc::openapi().to_pretty_json()?);
    Ok(())
}
