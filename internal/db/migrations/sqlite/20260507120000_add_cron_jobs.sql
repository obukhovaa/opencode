-- +goose Up
CREATE TABLE cron_jobs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    schedule TEXT NOT NULL,
    prompt TEXT NOT NULL,
    subagent_type TEXT NOT NULL,
    task_title TEXT NOT NULL,
    task_id TEXT NOT NULL,
    is_recurring BOOLEAN NOT NULL DEFAULT TRUE,
    source TEXT NOT NULL DEFAULT 'agent',
    status TEXT NOT NULL DEFAULT 'active',
    firing BOOLEAN NOT NULL DEFAULT FALSE,
    last_run_at INTEGER,
    next_run_at INTEGER,
    run_count INTEGER NOT NULL DEFAULT 0,
    last_result TEXT,
    error TEXT,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX idx_cron_jobs_session_id ON cron_jobs(session_id);
CREATE INDEX idx_cron_jobs_due ON cron_jobs(status, firing, next_run_at);
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS update_cron_jobs_updated_at
AFTER UPDATE ON cron_jobs
BEGIN
UPDATE cron_jobs SET updated_at = strftime('%s', 'now')
WHERE id = new.id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_cron_jobs_updated_at;
-- +goose StatementEnd
DROP TABLE IF EXISTS cron_jobs;
