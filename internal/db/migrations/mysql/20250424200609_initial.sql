-- +goose Up
-- +goose StatementBegin
-- Sessions
CREATE TABLE IF NOT EXISTS sessions (
    id VARCHAR(255) PRIMARY KEY,
    parent_session_id VARCHAR(255),
    title VARCHAR(512) NOT NULL,
    message_count BIGINT NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    prompt_tokens BIGINT NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens BIGINT NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    cost DOUBLE NOT NULL DEFAULT 0.0 CHECK (cost >= 0.0),
    updated_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL
);

CREATE TRIGGER update_sessions_updated_at
BEFORE UPDATE ON sessions
FOR EACH ROW
SET NEW.updated_at = UNIX_TIMESTAMP();

-- Files
CREATE TABLE IF NOT EXISTS files (
    id VARCHAR(255) PRIMARY KEY,
    session_id VARCHAR(255) NOT NULL,
    path VARCHAR(1024) NOT NULL,
    path_hash VARCHAR(64) GENERATED ALWAYS AS (SHA2(CONCAT(path, ':', session_id, ':', version), 256)) STORED,
    content LONGTEXT NOT NULL,
    version VARCHAR(255) NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE,
    UNIQUE(path_hash)
);

CREATE INDEX idx_files_session_id ON files (session_id);
CREATE INDEX idx_files_path ON files (path(255));

CREATE TRIGGER update_files_updated_at
BEFORE UPDATE ON files
FOR EACH ROW
SET NEW.updated_at = UNIX_TIMESTAMP();

-- Messages
CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR(255) PRIMARY KEY,
    session_id VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL,
    parts LONGTEXT NOT NULL DEFAULT '[]',
    model VARCHAR(255),
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    finished_at BIGINT,
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
);

CREATE INDEX idx_messages_session_id ON messages (session_id);

CREATE TRIGGER update_messages_updated_at
BEFORE UPDATE ON messages
FOR EACH ROW
SET NEW.updated_at = UNIX_TIMESTAMP();

CREATE TRIGGER update_session_message_count_on_insert
AFTER INSERT ON messages
FOR EACH ROW
UPDATE sessions SET
    message_count = message_count + 1
WHERE id = NEW.session_id;

CREATE TRIGGER update_session_message_count_on_delete
AFTER DELETE ON messages
FOR EACH ROW
UPDATE sessions SET
    message_count = message_count - 1
WHERE id = OLD.session_id;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;
DROP TRIGGER IF EXISTS update_files_updated_at;

DROP TRIGGER IF EXISTS update_session_message_count_on_delete;
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;

DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS sessions;
-- +goose StatementEnd
