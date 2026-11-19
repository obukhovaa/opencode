-- +goose Up
CREATE INDEX idx_sessions_project_parent_page ON sessions(project_id(191), parent_session_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX idx_sessions_project_parent_page ON sessions;
