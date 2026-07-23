-- +goose Up
ALTER TABLE sessions ADD COLUMN user_set_title BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE sessions DROP COLUMN user_set_title;
