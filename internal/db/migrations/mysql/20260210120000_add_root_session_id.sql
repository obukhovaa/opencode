-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN root_session_id VARCHAR(255);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_sessions_root_session_id ON sessions (root_session_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_sessions_root_session_id ON sessions;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN root_session_id;
-- +goose StatementEnd
