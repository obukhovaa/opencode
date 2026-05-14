-- +goose Up
-- +goose StatementBegin
CREATE TABLE cron_jobs (
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
    created_at BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    updated_at BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    CONSTRAINT fk_cron_jobs_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_cron_jobs_session_id ON cron_jobs(session_id);

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_cron_jobs_due ON cron_jobs(status, firing, next_run_at);

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_cron_jobs_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_cron_jobs_updated_at BEFORE
UPDATE ON cron_jobs FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_cron_jobs_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS cron_jobs;

-- +goose StatementEnd
