-- Schema version marker. Real tables arrive in M1: users, sessions, api_tokens, jobs, audit_log,
-- schedules, app_provenance.
CREATE TABLE schema_meta (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    created_at  TEXT NOT NULL
);

INSERT INTO schema_meta (id, created_at) VALUES (1, datetime('now'));
