-- +goose Up
-- +goose StatementBegin
-- One row per chat binding (same key and column sizes as bridge_sessions).
-- Settings columns are NULL when the default applies; the defaults live in
-- internal/heartbeat. Times are unix milliseconds.
CREATE TABLE IF NOT EXISTS bridge_heartbeats (
    project_id    VARCHAR(255) NOT NULL,
    channel       VARCHAR(32)  NOT NULL,
    identity_id   VARCHAR(64)  NOT NULL,
    peer_id       VARCHAR(128) NOT NULL,
    state         VARCHAR(16)  NOT NULL DEFAULT 'unset',
    every_seconds INT NULL,
    window_start  INT NULL,
    window_end    INT NULL,
    weekdays_only TINYINT(1) NOT NULL DEFAULT 0,
    model         VARCHAR(255) NULL,
    agenda_file   VARCHAR(1024) NULL,
    next_beat_at  BIGINT NULL,
    last_beat_at  BIGINT NULL,
    last_status   VARCHAR(16)  NULL,
    last_error    TEXT NULL,
    reminded_at   BIGINT NULL,
    updated_at    BIGINT NOT NULL,
    PRIMARY KEY (project_id, channel, identity_id, peer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS bridge_heartbeats;
