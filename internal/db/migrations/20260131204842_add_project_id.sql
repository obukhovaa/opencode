-- +goose Up
-- +goose StatementBegin

-- Add project_id column as nullable initially (for SQLite backfill compatibility)
-- SQLite treats TEXT and VARCHAR the same, MySQL needs VARCHAR for indexing
ALTER TABLE sessions ADD COLUMN project_id VARCHAR(512);

CREATE INDEX idx_sessions_project_id ON sessions(project_id);
CREATE INDEX idx_sessions_project_created ON sessions(project_id, created_at DESC);

-- Note: project_id will be backfilled by application code for SQLite
-- For MySQL, this migration runs on fresh databases, so all new sessions will have project_id set

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX idx_sessions_project_created;
DROP INDEX idx_sessions_project_id;
ALTER TABLE sessions DROP COLUMN project_id;

-- +goose StatementEnd
