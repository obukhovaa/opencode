-- +goose Up
ALTER TABLE sessions ADD COLUMN total_prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN total_completion_tokens INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN total_prompt_tokens;
ALTER TABLE sessions DROP COLUMN total_completion_tokens;
