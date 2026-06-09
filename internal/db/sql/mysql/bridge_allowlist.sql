-- name: AddBridgeAllowlistEntry :execresult
INSERT IGNORE INTO bridge_allowlist (
    project_id,
    channel,
    identity_id,
    peer_id,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    UNIX_TIMESTAMP()
);

-- name: IsBridgeAllowlisted :one
SELECT EXISTS(
    SELECT 1 FROM bridge_allowlist
    WHERE project_id  = ?
      AND channel     = ?
      AND identity_id = ?
      AND peer_id     = ?
) AS allowed;

-- name: ListBridgeAllowlist :many
SELECT *
FROM bridge_allowlist
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
ORDER BY created_at ASC;

-- name: RemoveBridgeAllowlistEntry :exec
DELETE FROM bridge_allowlist
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;
