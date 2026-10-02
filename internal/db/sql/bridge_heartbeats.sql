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
ON CONFLICT (project_id, channel, identity_id, peer_id) DO UPDATE SET
    state         = excluded.state,
    every_seconds = excluded.every_seconds,
    window_start  = excluded.window_start,
    window_end    = excluded.window_end,
    weekdays_only = excluded.weekdays_only,
    model         = excluded.model,
    agenda_file   = excluded.agenda_file,
    next_beat_at  = excluded.next_beat_at,
    last_beat_at  = excluded.last_beat_at,
    last_status   = excluded.last_status,
    last_error    = excluded.last_error,
    reminded_at   = excluded.reminded_at,
    updated_at    = excluded.updated_at;
