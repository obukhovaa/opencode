-- +goose Up
-- +goose StatementBegin
CREATE TABLE flow_states (
    session_id VARCHAR(255) PRIMARY KEY,
    root_session_id VARCHAR(255) NOT NULL,
    flow_id VARCHAR(255) NOT NULL,
    step_id VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'running',
    args LONGTEXT,
    output LONGTEXT,
    is_struct_output TINYINT(1) NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    updated_at BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP()),
    CONSTRAINT fk_flow_states_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_flow_states_root_session ON flow_states(root_session_id);

-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_flow_states_flow_id ON flow_states(flow_id);

-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_flow_states_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER update_flow_states_updated_at BEFORE
UPDATE ON flow_states FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_flow_states_updated_at;

-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS flow_states;

-- +goose StatementEnd
