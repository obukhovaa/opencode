-- +goose Up
-- +goose StatementBegin

-- Add project_id column (MySQL-specific with VARCHAR)
ALTER TABLE sessions ADD COLUMN project_id VARCHAR(512);

CREATE INDEX idx_sessions_project_id ON sessions(project_id);
CREATE INDEX idx_sessions_project_created ON sessions(project_id, created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX idx_sessions_project_created ON sessions;
DROP INDEX idx_sessions_project_id ON sessions;
ALTER TABLE sessions DROP COLUMN project_id;

-- +goose StatementEnd
