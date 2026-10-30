-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CreateUser :one
INSERT INTO users (username, password_hash, created_at)
VALUES (?, ?, ?)
RETURNING id, username, password_hash, created_at;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, created_at FROM users WHERE username = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT
    s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at,
    u.username, u.password_hash, u.created_at AS user_created_at
FROM sessions AS s
JOIN users AS u ON u.id = s.user_id
WHERE s.id = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ? WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= @now OR last_seen_at <= @idle_cutoff;

-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, user_id, name, token_hash, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id, user_id, name, created_at, last_used_at;

-- name: ListAPITokens :many
SELECT id, user_id, name, created_at, last_used_at
FROM api_tokens
WHERE user_id = ?
ORDER BY created_at, id;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = ? AND user_id = ?;

-- name: GetAPITokenByHash :one
SELECT
    t.id, t.user_id, t.name, t.created_at, t.last_used_at,
    u.username, u.password_hash, u.created_at AS user_created_at
FROM api_tokens AS t
JOIN users AS u ON u.id = t.user_id
WHERE t.token_hash = ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;
