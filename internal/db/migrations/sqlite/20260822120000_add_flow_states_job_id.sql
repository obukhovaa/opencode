-- +goose Up
ALTER TABLE flow_states ADD COLUMN job_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_flow_states_job_id ON flow_states(job_id);

-- +goose Down
DROP INDEX idx_flow_states_job_id;
ALTER TABLE flow_states DROP COLUMN job_id;
