-- +goose Up
-- +goose StatementBegin
ALTER TABLE messages ADD COLUMN synthetic TINYINT(1) NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE messages DROP COLUMN synthetic;
-- +goose StatementEnd
