-- name: GetMeta :one
SELECT value FROM schema_meta WHERE key = ?;
