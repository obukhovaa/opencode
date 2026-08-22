-- +goose Up
-- +goose StatementBegin
ALTER TABLE flow_states
  ADD COLUMN job_id VARCHAR(255) NOT NULL DEFAULT '';

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_flow_states_job_id ON flow_states(job_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_flow_states_job_id ON flow_states;

-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE flow_states
  DROP COLUMN job_id;

-- +goose StatementEnd
