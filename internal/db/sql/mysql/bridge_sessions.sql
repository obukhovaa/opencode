-- name: UpsertBridgeSession :execresult
INSERT INTO bridge_sessions (
    project_id,
    channel,
    identity_id,
    peer_id,
    session_id,
    mention_handle,
    mention_consumed_at,
    created_at,
    updated_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    NULL,
    UNIX_TIMESTAMP(),
    UNIX_TIMESTAMP()
)
ON DUPLICATE KEY UPDATE
    session_id          = VALUES(session_id),
    mention_handle      = VALUES(mention_handle),
    mention_consumed_at = NULL,
    updated_at          = UNIX_TIMESTAMP();

-- name: GetBridgeSession :one
SELECT *
FROM bridge_sessions
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?
LIMIT 1;

-- name: ListBridgeSessionsBySession :many
SELECT *
FROM bridge_sessions
WHERE project_id = ? AND session_id = ?;

-- name: ListBridgeSessionsByIdentity :many
SELECT *
FROM bridge_sessions
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?;

-- name: UpdateBridgeSessionPeerID :exec
UPDATE bridge_sessions
SET peer_id = ?
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;

-- name: UpdateBridgeSessionSessionID :exec
UPDATE bridge_sessions
SET session_id = ?
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;

-- name: MarkBridgeSessionMentionConsumed :exec
UPDATE bridge_sessions
SET mention_consumed_at = UNIX_TIMESTAMP()
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;

-- name: DeleteBridgeSessionByPeer :exec
DELETE FROM bridge_sessions
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;

-- name: DeleteBridgeSessionsBySession :exec
DELETE FROM bridge_sessions
WHERE project_id = ? AND session_id = ?;

-- name: DeleteBridgeSessionsByIdentity :exec
DELETE FROM bridge_sessions
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?;

-- name: CountBridgeSessionsByIdentity :one
SELECT COUNT(*)
FROM bridge_sessions
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?;
