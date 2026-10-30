-- +goose Up
CREATE TABLE schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO schema_meta (key, value) VALUES ('created_by', 'orchestrator');

-- +goose Down
DROP TABLE schema_meta;
