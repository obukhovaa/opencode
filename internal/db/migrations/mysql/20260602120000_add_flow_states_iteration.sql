-- +goose Up
-- +goose StatementBegin
ALTER TABLE flow_states
  ADD COLUMN iteration INT NOT NULL DEFAULT 1;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE flow_states
  DROP COLUMN iteration;

-- +goose StatementEnd
