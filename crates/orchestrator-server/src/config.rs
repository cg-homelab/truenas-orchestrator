//! Server configuration.

use clap::Parser;
use std::net::SocketAddr;

/// truenas-orchestrator backend.
#[derive(Debug, Parser)]
#[command(name = "orchestrator", version)]
pub struct Config {
    /// Address to bind.
    #[arg(long, env = "ORCHESTRATOR_BIND", default_value = "127.0.0.1:8080")]
    pub bind: SocketAddr,

    /// Allowed CORS origin for a frontend served from somewhere else.
    ///
    /// Needed only for the external-client workflow, where `vite dev` runs on a laptop against this
    /// backend. When unset, no cross-origin requests are permitted.
    #[arg(long, env = "ORCHESTRATOR_CORS_ORIGIN")]
    pub cors_origin: Option<String>,
}
