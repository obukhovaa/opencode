-- name: GetConversationFlowRun :one
SELECT * FROM conversation_flow_runs WHERE project_id = ? AND id = ?;

-- name: CurrentConversationFlowRun :one
SELECT * FROM conversation_flow_runs WHERE project_id = ? AND active_conversation = ?;

-- name: ListConversationFlowRuns :many
SELECT * FROM conversation_flow_runs WHERE project_id = ? AND conversation_id = ? ORDER BY created_at DESC, id DESC;
