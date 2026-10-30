-- +goose Up
-- Timestamps are Unix milliseconds (UTC).
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL, -- argon2id PHC string
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY, -- SHA-256 hex of the raw session token; the raw token is only in the cookie
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL -- absolute lifetime; the idle timeout is checked against last_seen_at
);

CREATE INDEX sessions_user_id ON sessions (user_id);

CREATE TABLE api_tokens (
    id           TEXT    PRIMARY KEY, -- random public identifier, not the token
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE, -- SHA-256 hex of the raw token, which is shown once
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE INDEX api_tokens_user_id ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
