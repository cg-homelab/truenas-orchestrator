//! SQLite state: pool, migrations, queries.
//!
//! Lives on a dedicated orchestrator dataset, separate from the homelab repo, so state survives
//! container replacement and can never be committed by accident (`docs/decisions.md` D10).
//!
//! Tables arrive in M1: users, sessions, api_tokens, jobs, audit_log, schedules, app_provenance.

use sqlx::{
    SqlitePool,
    sqlite::{SqliteConnectOptions, SqliteJournalMode, SqlitePoolOptions},
};
use std::{path::Path, str::FromStr as _};

/// Migrations are embedded in the binary rather than read from disk, so a deployed container
/// migrates itself and no `sqlx-cli` is needed to run the application.
static MIGRATIONS: sqlx::migrate::Migrator = sqlx::migrate!("./migrations");

#[derive(Debug, thiserror::Error)]
pub enum DbError {
    #[error("database: {0}")]
    Sqlx(#[from] sqlx::Error),
    #[error("migration: {0}")]
    Migrate(#[from] sqlx::migrate::MigrateError),
}

/// Open the database at `path`, creating it if absent, and apply any outstanding migrations.
pub async fn connect(path: &Path) -> Result<SqlitePool, DbError> {
    let options = SqliteConnectOptions::from_str(&format!("sqlite://{}", path.display()))?
        .create_if_missing(true)
        // WAL keeps readers from blocking the writer, which matters once background jobs write
        // progress while the UI polls.
        .journal_mode(SqliteJournalMode::Wal)
        .foreign_keys(true);

    let pool = SqlitePoolOptions::new().connect_with(options).await?;
    MIGRATIONS.run(&pool).await?;
    Ok(pool)
}

/// Open an in-memory database with migrations applied. For tests.
pub async fn connect_in_memory() -> Result<SqlitePool, DbError> {
    let pool = SqlitePoolOptions::new()
        .connect_with(SqliteConnectOptions::from_str("sqlite::memory:")?)
        .await?;
    MIGRATIONS.run(&pool).await?;
    Ok(pool)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn migrations_apply_to_a_fresh_database() {
        let pool = connect_in_memory().await.expect("connect");
        let version: i64 = sqlx::query_scalar("SELECT id FROM schema_meta")
            .fetch_one(&pool)
            .await
            .expect("schema_meta should exist after migration");
        assert_eq!(version, 1);
    }

    #[tokio::test]
    async fn migrations_are_idempotent() {
        // Running the migrator twice against the same pool must be a no-op, which is what makes
        // migrate-on-startup safe.
        let pool = connect_in_memory().await.expect("connect");
        MIGRATIONS.run(&pool).await.expect("second run");
    }
}
