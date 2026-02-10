-- name: CreateSession :execresult
INSERT INTO sessions (
    id,
    project_id,
    parent_session_id,
    root_session_id,
    title,
    message_count,
    prompt_tokens,
    completion_tokens,
    cost,
    summary_message_id,
    updated_at,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    null,
    UNIX_TIMESTAMP(),
    UNIX_TIMESTAMP()
);

-- name: GetSessionByID :one
SELECT *
FROM sessions
WHERE id = ? LIMIT 1;

-- name: ListSessions :many
SELECT *
FROM sessions
WHERE parent_session_id is NULL AND project_id = ?
ORDER BY created_at DESC;

-- name: UpdateSession :execresult
UPDATE sessions
SET
    title = ?,
    prompt_tokens = ?,
    completion_tokens = ?,
    summary_message_id = ?,
    cost = ?
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;

-- name: ListChildSessions :many
SELECT *
FROM sessions
WHERE root_session_id = ?
ORDER BY created_at ASC;
