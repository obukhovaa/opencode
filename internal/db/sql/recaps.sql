-- name: GetRecapBySessionID :one
SELECT *
FROM session_recaps
WHERE session_id = ? LIMIT 1;

-- name: UpsertRecap :one
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
    strftime('%s', 'now')
) ON CONFLICT(session_id) DO UPDATE SET
    content = excluded.content,
    message_count = excluded.message_count,
    created_at = excluded.created_at
RETURNING *;

-- name: DeleteRecapBySessionID :exec
DELETE FROM session_recaps
WHERE session_id = ?;
