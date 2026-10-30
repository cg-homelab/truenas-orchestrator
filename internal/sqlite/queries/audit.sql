-- name: AppendAudit :exec
INSERT INTO audit_log (at, user, action, target, details_json) VALUES (?, ?, ?, ?, ?);

-- name: ListAudit :many
SELECT id, at, user, action, target, details_json FROM audit_log ORDER BY at DESC, id DESC LIMIT ?;
