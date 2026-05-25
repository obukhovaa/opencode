package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/session"
)

// resolveDirectory returns the session directory. If the request includes an
// x-opencode-directory header (set by OpenWork's proxy), that value is used so
// the session's directory matches the workspace the caller sees. Otherwise we
// fall back to the process working directory.
func resolveDirectory(r *http.Request) string {
	if dir := r.Header.Get("X-Opencode-Directory"); dir != "" {
		return dir
	}
	return config.WorkingDirectory()
}

// ConvertSessionWithDir converts an internal Session to the external API format
// using the provided directory.
func ConvertSessionWithDir(s session.Session, directory string) APISession {
	return APISession{
		ID:        s.ID,
		ParentID:  s.ParentSessionID,
		Title:     s.Title,
		Directory: directory,
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

// ConvertSession converts an internal Session using the default working directory.
func ConvertSession(s session.Session) APISession {
	return ConvertSessionWithDir(s, config.WorkingDirectory())
}

// ConvertSessionsWithDir converts a slice of internal Sessions using the provided directory.
func ConvertSessionsWithDir(sessions []session.Session, directory string) []APISession {
	result := make([]APISession, len(sessions))
	for i, s := range sessions {
		result[i] = ConvertSessionWithDir(s, directory)
	}
	return result
}

// ConvertSessions converts a slice of internal Sessions using the default working directory.
func ConvertSessions(sessions []session.Session) []APISession {
	return ConvertSessionsWithDir(sessions, config.WorkingDirectory())
}
