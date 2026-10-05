-- +goose Up
-- +goose StatementBegin
-- One row per chat binding (same key as bridge_sessions). Settings columns
-- are NULL when the default applies; the defaults live in
-- internal/heartbeat. Times are unix milliseconds.
CREATE TABLE IF NOT EXISTS bridge_heartbeats (
    project_id    TEXT NOT NULL,
    channel       TEXT NOT NULL,
    identity_id   TEXT NOT NULL,
    peer_id       TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'unset',
    every_seconds INTEGER,
    window_start  INTEGER,
    window_end    INTEGER,
    weekdays_only INTEGER NOT NULL DEFAULT 0,
    model         TEXT,
    agenda_file   TEXT,
    next_beat_at  INTEGER,
    last_beat_at  INTEGER,
    last_status   TEXT,
    last_error    TEXT,
    reminded_at   INTEGER,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (project_id, channel, identity_id, peer_id)
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS bridge_heartbeats;
