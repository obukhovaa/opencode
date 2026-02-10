-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN root_session_id TEXT;
CREATE INDEX idx_sessions_root_session_id ON sessions(root_session_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_root_session_id;
ALTER TABLE sessions DROP COLUMN root_session_id;
-- +goose StatementEnd
