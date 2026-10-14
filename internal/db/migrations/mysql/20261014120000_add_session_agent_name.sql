-- +goose Up
ALTER TABLE sessions ADD COLUMN agent_name VARCHAR(128) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN agent_name;
