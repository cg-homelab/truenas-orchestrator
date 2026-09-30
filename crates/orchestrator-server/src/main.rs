use anyhow::Context as _;
use clap::Parser as _;
use orchestrator_server::{Config, app};
use tracing_subscriber::{EnvFilter, layer::SubscriberExt as _, util::SubscriberInitExt as _};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::registry()
        .with(EnvFilter::try_from_default_env().unwrap_or_else(|_| "info,tower_http=debug".into()))
        .with(tracing_subscriber::fmt::layer())
        .init();

    let config = Config::parse();
    let listener = tokio::net::TcpListener::bind(config.bind)
        .await
        .with_context(|| format!("binding {}", config.bind))?;

    tracing::info!(addr = %config.bind, "orchestrator listening");

    axum::serve(listener, app(&config))
        .with_graceful_shutdown(shutdown())
        .await
        .context("server error")
}

async fn shutdown() {
    let _ = tokio::signal::ctrl_c().await;
    tracing::info!("shutting down");
}
