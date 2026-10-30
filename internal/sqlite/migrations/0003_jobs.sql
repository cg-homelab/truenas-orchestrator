-- +goose Up
-- Timestamps are Unix milliseconds (UTC).
CREATE TABLE jobs (
    id          TEXT    PRIMARY KEY,
    kind        TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER,
    error       TEXT,
    log_json    TEXT    NOT NULL DEFAULT '[]' -- JSON array of log lines, written when the job finishes
);

CREATE INDEX jobs_state ON jobs (state);

-- +goose Down
DROP TABLE jobs;
