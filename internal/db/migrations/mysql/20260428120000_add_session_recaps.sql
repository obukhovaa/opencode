-- +goose Up
CREATE TABLE IF NOT EXISTS session_recaps (
    id VARCHAR(255) PRIMARY KEY,
    session_id VARCHAR(255) NOT NULL,
    content LONGTEXT NOT NULL,
    message_count BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    UNIQUE KEY idx_session_recaps_session_unique (session_id),
    KEY idx_session_recaps_session_id (session_id),
    CONSTRAINT fk_session_recaps_session_id FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS session_recaps;
