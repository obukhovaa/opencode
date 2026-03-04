-- +goose Up
ALTER TABLE messages ADD COLUMN seq INTEGER;

-- +goose Down
ALTER TABLE messages DROP COLUMN seq;
