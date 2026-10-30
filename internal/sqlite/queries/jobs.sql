-- name: CreateJob :one
INSERT INTO jobs (id, kind, state, created_at)
VALUES (?, ?, 'queued', ?)
RETURNING id, kind, state, created_at, started_at, finished_at, error, log_json;

-- name: StartJob :exec
UPDATE jobs SET state = 'running', started_at = ? WHERE id = ?;

-- name: FinishJob :exec
UPDATE jobs SET state = ?, finished_at = ?, error = ?, log_json = ? WHERE id = ?;

-- name: GetJob :one
SELECT id, kind, state, created_at, started_at, finished_at, error, log_json FROM jobs WHERE id = ?;

-- name: ListUnfinishedJobs :many
SELECT id FROM jobs WHERE state IN ('queued', 'running') ORDER BY created_at, id;
