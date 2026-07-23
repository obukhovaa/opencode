-- +goose Up
ALTER TABLE sessions ADD COLUMN user_set_title TINYINT(1) NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN user_set_title;
