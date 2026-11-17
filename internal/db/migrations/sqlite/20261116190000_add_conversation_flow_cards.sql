-- +goose Up
CREATE TABLE IF NOT EXISTS conversation_flow_cards (
 run_id VARCHAR(255) NOT NULL,
 recipient VARCHAR(255) NOT NULL,
 message_token TEXT NOT NULL,
 render_hash VARCHAR(128) NOT NULL,
 sequence BIGINT NOT NULL,
 state TEXT NOT NULL,
 updated_at BIGINT NOT NULL,
 PRIMARY KEY(run_id,recipient),
 FOREIGN KEY(run_id) REFERENCES conversation_flow_runs(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS conversation_flow_cards;
