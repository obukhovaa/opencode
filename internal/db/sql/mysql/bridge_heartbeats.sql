-- name: GetBridgeHeartbeat :one
SELECT *
FROM bridge_heartbeats
WHERE project_id  = ?
  AND channel     = ?
  AND identity_id = ?
  AND peer_id     = ?;

-- name: ListBridgeHeartbeats :many
SELECT *
FROM bridge_heartbeats
WHERE project_id = ?;

-- name: UpsertBridgeHeartbeat :exec
INSERT INTO bridge_heartbeats (
    project_id, channel, identity_id, peer_id, state, every_seconds, window_start, window_end, weekdays_only, model, agenda_file, next_beat_at, last_beat_at, last_status, last_error, reminded_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON DUPLICATE KEY UPDATE
    state         = VALUES(state),
    every_seconds = VALUES(every_seconds),
    window_start  = VALUES(window_start),
    window_end    = VALUES(window_end),
    weekdays_only = VALUES(weekdays_only),
    model         = VALUES(model),
    agenda_file   = VALUES(agenda_file),
    next_beat_at  = VALUES(next_beat_at),
    last_beat_at  = VALUES(last_beat_at),
    last_status   = VALUES(last_status),
    last_error    = VALUES(last_error),
    reminded_at   = VALUES(reminded_at),
    updated_at    = VALUES(updated_at);
