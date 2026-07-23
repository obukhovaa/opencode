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
    total_prompt_tokens = ?,
    total_completion_tokens = ?,
    summary_message_id = ?,
    cost = ?
WHERE id = ?;

-- name: RenameSession :execresult
UPDATE sessions
SET
    title = ?,
    user_set_title = 1
WHERE id = ?;

-- name: SetGeneratedTitle :execrows
UPDATE sessions
SET title = ?
WHERE id = ? AND user_set_title = 0;

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;

-- name: DeleteSessionTree :exec
DELETE FROM sessions
WHERE id = ? OR root_session_id = ?;

-- name: ListChildSessions :many
SELECT *
FROM sessions
WHERE root_session_id = ?
ORDER BY created_at ASC;
