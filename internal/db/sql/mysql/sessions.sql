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

-- name: SetSessionAgentName :exec
UPDATE sessions
SET agent_name = ?
WHERE id = ?;

-- name: SetSessionAgentNameIf :execrows
UPDATE sessions
SET agent_name = ?
WHERE id = ? AND agent_name = sqlc.arg(expected_agent_name);

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

-- name: ListSessionPage :many
SELECT s.*,
    CAST(CASE WHEN sqlc.arg(with_flow) = 1 THEN COALESCE(
        (SELECT JSON_UNQUOTE(JSON_EXTRACT(r.snapshot, '$.manifest.definition.id')) FROM conversation_flow_runs r
         WHERE r.project_id = s.project_id AND r.conversation_id = s.id
         ORDER BY (r.active_conversation IS NOT NULL) DESC, r.created_at DESC, r.id DESC LIMIT 1),
        (SELECT f.flow_id FROM flow_states f WHERE f.root_session_id = s.id LIMIT 1), '') ELSE '' END AS CHAR) AS flow_id,
    CAST(CASE WHEN sqlc.arg(with_flow) = 1 THEN COALESCE(
        (SELECT CASE WHEN r.kind = 'generated' THEN 1 ELSE 0 END FROM conversation_flow_runs r
         WHERE r.project_id = s.project_id AND r.conversation_id = s.id
         ORDER BY (r.active_conversation IS NOT NULL) DESC, r.created_at DESC, r.id DESC LIMIT 1), 0) ELSE 0 END AS SIGNED) AS dynamic_flow
FROM sessions s
WHERE s.project_id = sqlc.arg(project_id)
  AND (s.parent_session_id IS NULL OR sqlc.arg(only_id) <> '' OR sqlc.narg(tree_root) <> '')
  AND (sqlc.arg(roots_only) = 0 OR s.root_session_id IS NULL OR s.root_session_id = '' OR s.root_session_id = s.id OR s.id = sqlc.arg(current_id))
  AND (sqlc.arg(only_id) = '' OR s.id = sqlc.arg(only_id))
  AND (sqlc.arg(exclude_id) = '' OR s.id <> sqlc.arg(exclude_id))
  AND (sqlc.narg(tree_root) = '' OR s.id = sqlc.narg(tree_root) OR COALESCE(s.root_session_id, '') = sqlc.narg(tree_root))
  AND (sqlc.arg(search) = '' OR INSTR(LOWER(s.title), LOWER(sqlc.arg(search))) > 0 OR INSTR(LOWER(s.id), LOWER(sqlc.arg(search))) > 0)
  AND (sqlc.arg(id_prefix) = '' OR INSTR(s.id, sqlc.arg(id_prefix)) = 1)
  AND (sqlc.arg(hide_empty) = 0 OR s.title <> 'chat-bridge' OR s.message_count > 0 OR s.id = sqlc.arg(current_id))
  AND (sqlc.arg(exclude_external) = 0 OR s.id = sqlc.arg(current_id) OR NOT EXISTS (
      SELECT 1 FROM bridge_sessions b JOIN sessions e ON e.id = b.session_id
      WHERE b.project_id = s.project_id AND b.channel = 'external'
        AND (e.id = s.id OR COALESCE(NULLIF(e.root_session_id, ''), e.id) = COALESCE(NULLIF(s.root_session_id, ''), s.id))))
  AND (sqlc.arg(has_cursor) = 0 OR s.created_at < sqlc.arg(before_created)
       OR (s.created_at = sqlc.arg(before_created) AND s.id < sqlc.arg(before_id)))
ORDER BY s.created_at DESC, s.id DESC
LIMIT ?;

-- name: CountOldSessionTrees :one
SELECT COUNT(*) FROM (
SELECT CAST(COALESCE(MAX(CASE WHEN s.id = COALESCE(NULLIF(s.root_session_id, ''), s.id) THEN s.id END), MIN(s.id)) AS CHAR) AS session_id,
       CAST(COALESCE(NULLIF(s.root_session_id, ''), s.id) AS CHAR) AS tree_id,
       COALESCE(MAX(CASE WHEN s.id = COALESCE(NULLIF(s.root_session_id, ''), s.id) THEN s.updated_at END), MAX(s.updated_at)) AS idle_at
FROM sessions s
WHERE s.project_id = sqlc.narg(project_id) AND s.parent_session_id IS NULL
 AND COALESCE(NULLIF(s.root_session_id, ''), s.id) <> sqlc.narg(active_root)
 AND COALESCE(NULLIF(s.root_session_id, ''), s.id) NOT IN (SELECT r.id FROM conversation_flow_runs r WHERE r.project_id = sqlc.narg(project_id) AND r.conversation_id = sqlc.narg(active_root))
GROUP BY CAST(COALESCE(NULLIF(s.root_session_id, ''), s.id) AS CHAR)
HAVING idle_at < CAST(sqlc.arg(cutoff) AS SIGNED)
) old_trees;

-- name: ListOldSessionTreePage :many
SELECT CAST(COALESCE(MAX(CASE WHEN s.id = COALESCE(NULLIF(s.root_session_id, ''), s.id) THEN s.id END), MIN(s.id)) AS CHAR) AS session_id,
       CAST(COALESCE(NULLIF(s.root_session_id, ''), s.id) AS CHAR) AS tree_id,
       COALESCE(MAX(CASE WHEN s.id = COALESCE(NULLIF(s.root_session_id, ''), s.id) THEN s.updated_at END), MAX(s.updated_at)) AS idle_at
FROM sessions s
WHERE s.project_id = sqlc.narg(project_id) AND s.parent_session_id IS NULL
 AND COALESCE(NULLIF(s.root_session_id, ''), s.id) <> sqlc.narg(active_root)
 AND COALESCE(NULLIF(s.root_session_id, ''), s.id) NOT IN (SELECT r.id FROM conversation_flow_runs r WHERE r.project_id = sqlc.narg(project_id) AND r.conversation_id = sqlc.narg(active_root))
 AND COALESCE(NULLIF(s.root_session_id, ''), s.id) > sqlc.narg(after_root)
GROUP BY CAST(COALESCE(NULLIF(s.root_session_id, ''), s.id) AS CHAR)
HAVING idle_at < CAST(sqlc.arg(cutoff) AS SIGNED)
ORDER BY tree_id ASC
LIMIT ?;

-- name: ListVisibleSessionIDs :many
SELECT s.id FROM sessions s
WHERE s.project_id = sqlc.arg(project_id)
AND (s.id = sqlc.arg(current_id) OR NOT EXISTS (
 SELECT 1 FROM bridge_sessions b JOIN sessions e ON e.id = b.session_id
 WHERE b.project_id = s.project_id AND b.channel = 'external'
 AND (e.id = s.id OR COALESCE(NULLIF(e.root_session_id, ''), e.id) = COALESCE(NULLIF(s.root_session_id, ''), s.id))))
AND s.id IN (sqlc.slice('ids'));
