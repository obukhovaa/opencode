-- +goose Up
-- +goose StatementBegin
ALTER TABLE messages ADD COLUMN seq BIGINT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE messages DROP COLUMN seq;
-- +goose StatementEnd
