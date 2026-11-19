package session

import "github.com/opencode-ai/opencode/internal/db"

func (s *service) fromPageRow(row db.ListSessionPageRow) Session {
	result := s.fromDBItem(db.Session{
		ID:                    row.ID,
		ParentSessionID:       row.ParentSessionID,
		RootSessionID:         row.RootSessionID,
		Title:                 row.Title,
		MessageCount:          row.MessageCount,
		PromptTokens:          row.PromptTokens,
		CompletionTokens:      row.CompletionTokens,
		TotalPromptTokens:     row.TotalPromptTokens,
		TotalCompletionTokens: row.TotalCompletionTokens,
		Cost:                  row.Cost,
		UpdatedAt:             row.UpdatedAt,
		CreatedAt:             row.CreatedAt,
		SummaryMessageID:      row.SummaryMessageID,
		ProjectID:             row.ProjectID,
		UserSetTitle:          row.UserSetTitle,
		AgentName:             row.AgentName,
	})
	result.FlowID = row.FlowID
	result.DynamicFlow = row.DynamicFlow != 0
	return result
}
