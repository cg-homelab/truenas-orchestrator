-- name: GetCredential :one
SELECT name, username, secret, updated_at FROM credentials WHERE name = ?;

-- name: PutCredential :exec
INSERT INTO credentials (name, username, secret, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET username = excluded.username, secret = excluded.secret, updated_at = excluded.updated_at;

-- name: DeleteCredential :execrows
DELETE FROM credentials WHERE name = ?;
