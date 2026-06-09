-- +goose Up
-- +goose StatementBegin
-- Bridge composite PK column sizes are chosen so the PK fits within
-- MySQL InnoDB's 3072-byte (utf8mb4 → 4 bytes/char) key-length cap:
--   project_id  VARCHAR(255)  →  1020 bytes
--   channel     VARCHAR(32)   →   128 bytes  (values: "slack" | "telegram" | "mattermost")
--   identity_id VARCHAR(64)   →   256 bytes  (operator-chosen ID, typically "default")
--   peer_id     VARCHAR(128)  →   512 bytes  (longest real form: "C0ABC|1700000000.000100")
--                              total ≈ 1916 bytes ✓
-- The same shrinking is mirrored on the SQLite path (SQLite has no
-- key-length limit but consistency matters for sqlc-generated structs).
CREATE TABLE IF NOT EXISTS bridge_sessions (
    project_id          VARCHAR(255) NOT NULL,
    channel             VARCHAR(32)  NOT NULL,
    identity_id         VARCHAR(64)  NOT NULL,
    peer_id             VARCHAR(128) NOT NULL,
    session_id          VARCHAR(255) NULL,
    mention_handle      VARCHAR(255) NULL,
    mention_consumed_at BIGINT NULL,
    created_at          BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    updated_at          BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    PRIMARY KEY (project_id, channel, identity_id, peer_id),
    KEY idx_bridge_sessions_session_id (session_id),
    CONSTRAINT fk_bridge_sessions_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_bridge_sessions_updated_at
BEFORE UPDATE ON bridge_sessions
FOR EACH ROW
SET NEW.updated_at = UNIX_TIMESTAMP();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS bridge_allowlist (
    project_id  VARCHAR(255) NOT NULL,
    channel     VARCHAR(32)  NOT NULL,
    identity_id VARCHAR(64)  NOT NULL,
    peer_id     VARCHAR(128) NOT NULL,
    created_at  BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    PRIMARY KEY (project_id, channel, identity_id, peer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_bridge_sessions_updated_at;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS bridge_allowlist;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS bridge_sessions;
-- +goose StatementEnd
