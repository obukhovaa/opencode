-- +goose Up
ALTER TABLE sessions ADD COLUMN agent_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN agent_name;
