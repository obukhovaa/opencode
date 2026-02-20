-- +goose Up
CREATE TABLE flow_states (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    root_session_id TEXT NOT NULL,
    flow_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    args TEXT,
    output TEXT,
    is_struct_output BOOLEAN NOT NULL DEFAULT FALSE,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX idx_flow_states_root_session ON flow_states(root_session_id);
CREATE INDEX idx_flow_states_flow_id ON flow_states(flow_id);
CREATE TRIGGER IF NOT EXISTS update_flow_states_updated_at
AFTER UPDATE ON flow_states
BEGIN
UPDATE flow_states SET updated_at = strftime('%s', 'now')
WHERE session_id = new.session_id;
END;

-- +goose Down
DROP TABLE IF EXISTS flow_states;
