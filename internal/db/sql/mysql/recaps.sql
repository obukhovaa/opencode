-- name: GetRecapBySessionID :one
SELECT *
FROM session_recaps
WHERE session_id = ? LIMIT 1;

-- name: UpsertRecap :execresult
INSERT INTO session_recaps (
    id,
    session_id,
    content,
    message_count,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    UNIX_TIMESTAMP()
) ON DUPLICATE KEY UPDATE
    content = VALUES(content),
    message_count = VALUES(message_count),
    created_at = VALUES(created_at);

-- name: DeleteRecapBySessionID :exec
DELETE FROM session_recaps
WHERE session_id = ?;
