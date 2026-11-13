package db

import (
	"context"

	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

func flowRunFromMySQL(r mysqldb.ConversationFlowRun) ConversationFlowRun {
	return ConversationFlowRun{ID: r.ID, ProjectID: r.ProjectID, ConversationID: r.ConversationID, InvocationID: r.InvocationID, InvocationHash: r.InvocationHash, ActiveConversation: r.ActiveConversation, Kind: r.Kind, Status: r.Status, Snapshot: r.Snapshot, Origin: r.Origin, Version: r.Version, Deadline: r.Deadline, LeaseOwner: r.LeaseOwner, LeaseToken: r.LeaseToken, LeaseExpiresAt: r.LeaseExpiresAt, GuardSerial: r.GuardSerial, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func (q *MySQLQuerier) GetConversationFlowRun(ctx context.Context, arg GetConversationFlowRunParams) (ConversationFlowRun, error) {
	r, err := q.queries.GetConversationFlowRun(ctx, mysqldb.GetConversationFlowRunParams{ProjectID: arg.ProjectID, ID: arg.ID})
	return flowRunFromMySQL(r), err
}
func (q *MySQLQuerier) CurrentConversationFlowRun(ctx context.Context, arg CurrentConversationFlowRunParams) (ConversationFlowRun, error) {
	r, err := q.queries.CurrentConversationFlowRun(ctx, mysqldb.CurrentConversationFlowRunParams{ProjectID: arg.ProjectID, ActiveConversation: arg.ActiveConversation})
	return flowRunFromMySQL(r), err
}
func (q *MySQLQuerier) ListConversationFlowRuns(ctx context.Context, arg ListConversationFlowRunsParams) ([]ConversationFlowRun, error) {
	rows, err := q.queries.ListConversationFlowRuns(ctx, mysqldb.ListConversationFlowRunsParams{ProjectID: arg.ProjectID, ConversationID: arg.ConversationID})
	if err != nil {
		return nil, err
	}
	out := make([]ConversationFlowRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, flowRunFromMySQL(r))
	}
	return out, nil
}
