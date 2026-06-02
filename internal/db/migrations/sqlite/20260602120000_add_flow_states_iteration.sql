-- +goose Up
ALTER TABLE flow_states ADD COLUMN iteration INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE flow_states DROP COLUMN iteration;
