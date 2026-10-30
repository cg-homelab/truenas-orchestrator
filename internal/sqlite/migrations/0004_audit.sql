-- +goose Up
-- The audit log of every write the orchestrator makes on the user's behalf (D9). It records that a
-- secret was written, never its value: details_json holds key names, paths and counts only.
-- Timestamps are Unix milliseconds (UTC).
CREATE TABLE audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           INTEGER NOT NULL,
    user         TEXT    NOT NULL, -- username of the caller, or of who started the job
    action       TEXT    NOT NULL, -- e.g. env.write, secret.write, setup.run
    target       TEXT    NOT NULL, -- repo-relative or absolute path the action wrote
    details_json TEXT    NOT NULL DEFAULT '{}' -- JSON object of string to string
);

CREATE INDEX audit_log_at ON audit_log (at);

-- +goose Down
DROP TABLE audit_log;
