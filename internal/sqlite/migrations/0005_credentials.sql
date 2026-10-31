-- +goose Up
-- Credentials the orchestrator uses to reach other systems (D34). One row per system; today only
-- name = 'truenas', the TrueNAS API key. secret is plaintext, like the .env files (D5), on the state
-- dataset; it is never logged, returned by the API or written to the audit log. updated_at is Unix
-- milliseconds (UTC).
CREATE TABLE credentials (
    name       TEXT    PRIMARY KEY,
    username   TEXT    NOT NULL,
    secret     TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE credentials;
