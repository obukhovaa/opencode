-- +goose Up
-- Sessions
-- +goose StatementBegin
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
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_sessions_updated_at BEFORE
UPDATE ON sessions FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();

-- +goose StatementEnd
-- Files
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
  id VARCHAR(255) PRIMARY KEY,
  session_id VARCHAR(255) NOT NULL,
  path VARCHAR(1024) NOT NULL,
  version VARCHAR(255) NOT NULL,
  content LONGTEXT NOT NULL,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  UNIQUE KEY idx_path_version (path(255), session_id, version),
  KEY idx_files_session_id (session_id),
  KEY idx_files_path (path(255)),
  CONSTRAINT fk_files_session_id FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_files_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_files_updated_at BEFORE
UPDATE ON files FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();

-- +goose StatementEnd
-- Messages
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS messages (
  id VARCHAR(255) PRIMARY KEY,
  session_id VARCHAR(255) NOT NULL,
  role VARCHAR(50) NOT NULL,
  parts LONGTEXT NOT NULL,
  model VARCHAR(255),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  finished_at BIGINT,
  FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_messages_session_id ON messages (session_id);

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_messages_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_messages_updated_at BEFORE
UPDATE ON messages FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert AFTER INSERT ON messages FOR EACH ROW
UPDATE sessions
SET
  message_count = message_count + 1
WHERE
  id = NEW.session_id;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete AFTER DELETE ON messages FOR EACH ROW
UPDATE sessions
SET
  message_count = message_count - 1
WHERE
  id = OLD.session_id;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_messages_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_files_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS messages;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS files;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS sessions;

-- +goose StatementEnd
