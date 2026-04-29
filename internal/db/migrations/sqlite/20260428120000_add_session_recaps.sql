-- +goose Up
CREATE TABLE IF NOT EXISTS session_recaps (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_session_recaps_session_id ON session_recaps (session_id);

-- +goose Down
DROP INDEX IF EXISTS idx_session_recaps_session_id;
DROP TABLE IF EXISTS session_recaps;
