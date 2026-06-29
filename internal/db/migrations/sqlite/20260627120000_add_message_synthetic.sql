-- +goose Up
ALTER TABLE messages ADD COLUMN synthetic BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE messages DROP COLUMN synthetic;
