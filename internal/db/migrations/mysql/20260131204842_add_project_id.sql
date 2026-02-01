-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions
ADD COLUMN project_id VARCHAR(512);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_sessions_project_id ON sessions (project_id(255));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_sessions_project_created ON sessions (project_id(255), created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_sessions_project_created ON sessions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX idx_sessions_project_id ON sessions;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sessions
DROP COLUMN project_id;
-- +goose StatementEnd
