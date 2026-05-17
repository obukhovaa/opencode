package api

import (
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/session"
)

// ConvertSession converts an internal Session to the external API format.
func ConvertSession(s session.Session) APISession {
	return APISession{
		ID:        s.ID,
		ParentID:  s.ParentSessionID,
		Title:     s.Title,
		Directory: config.WorkingDirectory(),
		Time: APISessionTime{
			Created: s.CreatedAt,
			Updated: s.UpdatedAt,
		},
		Token: APISessionToken{
			Input:  s.PromptTokens,
			Output: s.CompletionTokens,
		},
		Cost: s.Cost,
	}
}

// ConvertSessions converts a slice of internal Sessions to the external API format.
func ConvertSessions(sessions []session.Session) []APISession {
	result := make([]APISession, len(sessions))
	for i, s := range sessions {
		result[i] = ConvertSession(s)
	}
	return result
}
