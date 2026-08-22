-- name: CreateFlowState :execresult
INSERT INTO flow_states (
    session_id,
    root_session_id,
    flow_id,
    step_id,
    status,
    args,
    output,
    is_struct_output,
    iteration,
    job_id,
    created_at,
    updated_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    UNIX_TIMESTAMP(),
    UNIX_TIMESTAMP()
);

-- name: GetFlowState :one
SELECT * FROM flow_states WHERE session_id = ? LIMIT 1;

-- name: ListFlowStatesByRootSession :many
SELECT * FROM flow_states WHERE root_session_id = ? ORDER BY created_at ASC;

-- name: ListFlowStatesByFlowID :many
SELECT * FROM flow_states WHERE flow_id = ? ORDER BY created_at ASC;

-- name: ListFlowStatesByJobID :many
SELECT * FROM flow_states WHERE job_id = ? ORDER BY updated_at ASC;

-- name: UpdateFlowState :execresult
UPDATE flow_states
SET status = ?,
    args = ?,
    output = ?,
    is_struct_output = ?,
    iteration = ?,
    job_id = ?
WHERE session_id = ?;

-- name: DeleteFlowStatesByRootSession :exec
DELETE FROM flow_states WHERE root_session_id = ?;
