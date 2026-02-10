CREATE TABLE IF NOT EXISTS sessions (
  id VARCHAR(255) PRIMARY KEY,
  parent_session_id VARCHAR(255),
  root_session_id VARCHAR(255),
  title VARCHAR(512) NOT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  prompt_tokens BIGINT NOT NULL DEFAULT 0,
  completion_tokens BIGINT NOT NULL DEFAULT 0,
  cost DOUBLE NOT NULL DEFAULT 0.0,
  updated_at BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  summary_message_id VARCHAR(255),
  project_id VARCHAR(512),
  KEY idx_sessions_project_id (project_id(255)),
  KEY idx_sessions_project_created (project_id(255), created_at DESC),
  KEY idx_sessions_root_session_id (root_session_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

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

CREATE TABLE IF NOT EXISTS messages (
  id VARCHAR(255) PRIMARY KEY,
  session_id VARCHAR(255) NOT NULL,
  role VARCHAR(50) NOT NULL,
  parts LONGTEXT NOT NULL,
  model VARCHAR(255),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  finished_at BIGINT,
  KEY idx_messages_session_id (session_id),
  CONSTRAINT fk_messages_session_id FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
