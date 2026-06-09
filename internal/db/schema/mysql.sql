CREATE TABLE IF NOT EXISTS sessions (
  id VARCHAR(255) PRIMARY KEY,
  parent_session_id VARCHAR(255),
  root_session_id VARCHAR(255),
  title VARCHAR(512) NOT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  prompt_tokens BIGINT NOT NULL DEFAULT 0,
  completion_tokens BIGINT NOT NULL DEFAULT 0,
  cost DOUBLE NOT NULL DEFAULT 0.0,
  total_prompt_tokens BIGINT NOT NULL DEFAULT 0,
  total_completion_tokens BIGINT NOT NULL DEFAULT 0,
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
  seq BIGINT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  finished_at BIGINT,
  KEY idx_messages_session_id (session_id),

  CONSTRAINT fk_messages_session_id FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS flow_states (
  session_id VARCHAR(255) PRIMARY KEY,
  root_session_id VARCHAR(255) NOT NULL,
  flow_id VARCHAR(255) NOT NULL,
  step_id VARCHAR(255) NOT NULL,
  status VARCHAR(50) NOT NULL DEFAULT 'running',
  args LONGTEXT,
  output LONGTEXT,
  is_struct_output TINYINT(1) NOT NULL DEFAULT 0,
  iteration INT NOT NULL DEFAULT 1,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_flow_states_root_session (root_session_id),
  KEY idx_flow_states_flow_id (flow_id),
  CONSTRAINT fk_flow_states_session FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

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

CREATE TABLE IF NOT EXISTS cron_jobs (
  id VARCHAR(255) PRIMARY KEY,
  session_id VARCHAR(255) NOT NULL,
  schedule VARCHAR(255) NOT NULL,
  prompt LONGTEXT NOT NULL,
  subagent_type VARCHAR(255) NOT NULL,
  task_title VARCHAR(512) NOT NULL,
  task_id VARCHAR(255) NOT NULL,
  is_recurring TINYINT(1) NOT NULL DEFAULT 1,
  source VARCHAR(50) NOT NULL DEFAULT 'agent',
  status VARCHAR(50) NOT NULL DEFAULT 'active',
  firing TINYINT(1) NOT NULL DEFAULT 0,
  last_run_at BIGINT,
  next_run_at BIGINT,
  run_count BIGINT NOT NULL DEFAULT 0,
  last_result LONGTEXT,
  error LONGTEXT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_cron_jobs_session_id (session_id),
  KEY idx_cron_jobs_due (status, firing, next_run_at),
  CONSTRAINT fk_cron_jobs_session FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- Bridge composite PK column sizes are chosen so the PK fits within
-- MySQL InnoDB's 3072-byte (utf8mb4 → 4 bytes/char) key-length cap;
-- see the goose migration of the same date for the size derivation.
CREATE TABLE IF NOT EXISTS bridge_sessions (
  project_id VARCHAR(255) NOT NULL,
  channel VARCHAR(32) NOT NULL,
  identity_id VARCHAR(64) NOT NULL,
  peer_id VARCHAR(128) NOT NULL,
  session_id VARCHAR(255) NULL,
  mention_handle VARCHAR(255) NULL,
  mention_consumed_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (project_id, channel, identity_id, peer_id),
  KEY idx_bridge_sessions_session_id (session_id),
  CONSTRAINT fk_bridge_sessions_session FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS bridge_allowlist (
  project_id VARCHAR(255) NOT NULL,
  channel VARCHAR(32) NOT NULL,
  identity_id VARCHAR(64) NOT NULL,
  peer_id VARCHAR(128) NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (project_id, channel, identity_id, peer_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
