package api

import (
	"github.com/opencode-ai/opencode/internal/question"
)

// ConvertQuestionRequest converts an internal question.Request to the API representation.
func ConvertQuestionRequest(r question.Request) APIQuestionRequest {
	prompts := make([]APIQuestionPrompt, len(r.Questions))
	for i, q := range r.Questions {
		opts := make([]APIQuestionOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = APIQuestionOption{
				Label:       o.Label,
				Description: o.Description,
			}
		}
		prompts[i] = APIQuestionPrompt{
			Question: q.Question,
			Options:  opts,
			Multiple: q.Multiple,
			Custom:   q.Custom,
		}
	}
	return APIQuestionRequest{
		ID:        r.ID,
		SessionID: r.SessionID,
		Questions: prompts,
	}
}
