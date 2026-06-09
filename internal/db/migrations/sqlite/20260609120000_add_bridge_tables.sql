-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS bridge_sessions (
    project_id          TEXT NOT NULL,
    channel             TEXT NOT NULL,
    identity_id         TEXT NOT NULL,
    peer_id             TEXT NOT NULL,
    session_id          TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    mention_handle      TEXT,
    mention_consumed_at INTEGER,
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (project_id, channel, identity_id, peer_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Required: the FK ON DELETE SET NULL above triggers a child-row scan when an
-- opencode session is deleted. Without this index the scan is a full table
-- walk of bridge_sessions on every session GC.
CREATE INDEX IF NOT EXISTS idx_bridge_sessions_session_id
    ON bridge_sessions(session_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS trg_bridge_sessions_updated_at
AFTER UPDATE ON bridge_sessions
FOR EACH ROW
BEGIN
    UPDATE bridge_sessions
       SET updated_at = strftime('%s','now')
     WHERE project_id  = OLD.project_id
       AND channel     = OLD.channel
       AND identity_id = OLD.identity_id
       AND peer_id     = OLD.peer_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS bridge_allowlist (
    project_id  TEXT NOT NULL,
    channel     TEXT NOT NULL,
    identity_id TEXT NOT NULL,
    peer_id     TEXT NOT NULL,
    created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (project_id, channel, identity_id, peer_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_bridge_sessions_updated_at;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_bridge_sessions_session_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS bridge_allowlist;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS bridge_sessions;
-- +goose StatementEnd
