-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: CreateMessage :execresult
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, UNIX_TIMESTAMP(), UNIX_TIMESTAMP()
);

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    finished_at = ?,
    updated_at = UNIX_TIMESTAMP()
WHERE id = ?;

-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: DeleteSessionMessages :exec
DELETE FROM messages
WHERE session_id = ?;
