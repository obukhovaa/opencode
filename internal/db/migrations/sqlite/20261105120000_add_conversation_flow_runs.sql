-- +goose Up
CREATE TABLE IF NOT EXISTS conversation_flow_runs (
 id VARCHAR(255) PRIMARY KEY,
 project_id VARCHAR(255) NOT NULL,
 conversation_id VARCHAR(255) NOT NULL,
 invocation_id VARCHAR(255) NOT NULL,
 invocation_hash VARCHAR(64) NOT NULL,
 active_conversation VARCHAR(255),
 kind VARCHAR(32) NOT NULL,
 status VARCHAR(32) NOT NULL,
 snapshot TEXT NOT NULL,
 origin TEXT NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 deadline BIGINT NOT NULL DEFAULT 0,
 lease_owner VARCHAR(255) NOT NULL DEFAULT '',
 lease_token BIGINT NOT NULL DEFAULT 0,
 lease_expires_at BIGINT NOT NULL DEFAULT 0,
 guard_serial BIGINT NOT NULL DEFAULT 0,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL,
 UNIQUE(project_id,conversation_id,invocation_id),
 UNIQUE(project_id,active_conversation)
);
CREATE INDEX IF NOT EXISTS idx_conversation_flows ON conversation_flow_runs(project_id,conversation_id,created_at);
CREATE TABLE IF NOT EXISTS conversation_flow_inbox (
 run_id VARCHAR(255) NOT NULL,
 id VARCHAR(255) NOT NULL,
 sequence BIGINT NOT NULL,
 payload TEXT NOT NULL,
 delivered BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,id),
 UNIQUE(run_id,sequence),
 FOREIGN KEY(run_id) REFERENCES conversation_flow_runs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS conversation_flow_events (
 run_id VARCHAR(255) NOT NULL,
 id VARCHAR(255) NOT NULL,
 sequence BIGINT NOT NULL,
 kind VARCHAR(64) NOT NULL,
 payload TEXT NOT NULL,
 PRIMARY KEY(run_id,id),
 UNIQUE(run_id,sequence),
 FOREIGN KEY(run_id) REFERENCES conversation_flow_runs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS conversation_flow_receipts (
 run_id VARCHAR(255) NOT NULL,
 event_id VARCHAR(255) NOT NULL,
 recipient VARCHAR(255) NOT NULL,
 PRIMARY KEY(run_id,event_id,recipient),
 FOREIGN KEY(run_id,event_id) REFERENCES conversation_flow_events(run_id,id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS conversation_flow_questions (
 run_id VARCHAR(255) NOT NULL,
 id VARCHAR(255) NOT NULL,
 session_id VARCHAR(255) NOT NULL,
 step_id VARCHAR(64) NOT NULL,
 request_id VARCHAR(255) NOT NULL,
 descriptor TEXT NOT NULL,
 answer TEXT NOT NULL,
 answer_id VARCHAR(255) NOT NULL DEFAULT '',
 status VARCHAR(32) NOT NULL,
 PRIMARY KEY(run_id,id),
 FOREIGN KEY(run_id) REFERENCES conversation_flow_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS conversation_flow_question_requests (
 run_id VARCHAR(255) NOT NULL,
 question_id VARCHAR(255) NOT NULL,
 request_id VARCHAR(255) NOT NULL,
 PRIMARY KEY(run_id,request_id),
 FOREIGN KEY(run_id,question_id) REFERENCES conversation_flow_questions(run_id,id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS conversation_flow_prompts (
 run_id VARCHAR(255) NOT NULL,
 step_id VARCHAR(64) NOT NULL,
 payload TEXT NOT NULL,
 PRIMARY KEY(run_id,step_id),
 FOREIGN KEY(run_id) REFERENCES conversation_flow_runs(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS conversation_flow_prompts;
DROP TABLE IF EXISTS conversation_flow_question_requests;
DROP TABLE IF EXISTS conversation_flow_questions;
DROP TABLE IF EXISTS conversation_flow_receipts;
DROP TABLE IF EXISTS conversation_flow_events;
DROP TABLE IF EXISTS conversation_flow_inbox;
DROP TABLE IF EXISTS conversation_flow_runs;
