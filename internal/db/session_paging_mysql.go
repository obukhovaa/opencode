package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

// sqlc's MySQL and SQLite expression types differ, so normalize only at this
// adapter boundary. All page filters remain parameterized SQL on both engines.
func sessionQueryString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case sql.NullString:
		return v.String
	default:
		return fmt.Sprint(v)
	}
}
func sessionQueryInt(value any) int64 {
	n, _ := strconv.ParseInt(sessionQueryString(value), 10, 64)
	return n
}

func (q *MySQLQuerier) ListSessionPage(ctx context.Context, arg ListSessionPageParams) ([]ListSessionPageRow, error) {
	rows, err := q.queries.ListSessionPage(ctx, mysqldb.ListSessionPageParams{
		ProjectID:       arg.ProjectID,
		WithFlow:        arg.WithFlow,
		OnlyID:          sessionQueryString(arg.OnlyID),
		ExcludeID:       sessionQueryString(arg.ExcludeID),
		TreeRoot:        sql.NullString{String: sessionQueryString(arg.TreeRoot), Valid: true},
		RootsOnly:       arg.RootsOnly,
		CurrentID:       arg.CurrentID,
		Search:          sessionQueryString(arg.Search),
		IDPrefix:        sessionQueryString(arg.IDPrefix),
		HideEmpty:       arg.HideEmpty,
		ExcludeExternal: arg.ExcludeExternal,
		HasCursor:       arg.HasCursor,
		BeforeCreated:   sessionQueryInt(arg.BeforeCreated),
		BeforeID:        arg.BeforeID,
		Limit:           int32(arg.PageLimit),
	})
	if err != nil {
		return nil, err
	}
	result := make([]ListSessionPageRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, ListSessionPageRow{
			ID:                    r.ID,
			ParentSessionID:       r.ParentSessionID,
			RootSessionID:         r.RootSessionID,
			Title:                 r.Title,
			MessageCount:          r.MessageCount,
			PromptTokens:          r.PromptTokens,
			CompletionTokens:      r.CompletionTokens,
			TotalPromptTokens:     r.TotalPromptTokens,
			TotalCompletionTokens: r.TotalCompletionTokens,
			Cost:                  r.Cost,
			UpdatedAt:             r.UpdatedAt,
			CreatedAt:             r.CreatedAt,
			SummaryMessageID:      r.SummaryMessageID,
			ProjectID:             r.ProjectID,
			UserSetTitle:          r.UserSetTitle,
			AgentName:             r.AgentName,
			FlowID:                sessionQueryString(r.FlowID),
			DynamicFlow:           r.DynamicFlow,
		})
	}
	return result, nil
}
func (q *MySQLQuerier) CountOldSessionTrees(ctx context.Context, arg CountOldSessionTreesParams) (int64, error) {
	return q.queries.CountOldSessionTrees(ctx, mysqldb.CountOldSessionTreesParams{ProjectID: arg.ProjectID, Cutoff: arg.Cutoff, ActiveRoot: arg.ActiveRoot})
}
func (q *MySQLQuerier) ListOldSessionTreePage(ctx context.Context, arg ListOldSessionTreePageParams) ([]ListOldSessionTreePageRow, error) {
	rows, err := q.queries.ListOldSessionTreePage(ctx, mysqldb.ListOldSessionTreePageParams{ProjectID: arg.ProjectID, Cutoff: arg.Cutoff, ActiveRoot: arg.ActiveRoot, AfterRoot: arg.AfterRoot, Limit: int32(arg.PageLimit)})
	if err != nil {
		return nil, err
	}
	result := make([]ListOldSessionTreePageRow, 0, len(rows))
	for _, r := range rows {
		result = append(result, ListOldSessionTreePageRow{SessionID: sessionQueryString(r.SessionID), TreeID: sessionQueryString(r.TreeID), IdleAt: r.IdleAt})
	}
	return result, nil
}

func (q *MySQLQuerier) ListVisibleSessionIDs(ctx context.Context, arg ListVisibleSessionIDsParams) ([]string, error) {
	return q.queries.ListVisibleSessionIDs(ctx, mysqldb.ListVisibleSessionIDsParams{ProjectID: arg.ProjectID, Ids: arg.Ids, CurrentID: arg.CurrentID})
}
