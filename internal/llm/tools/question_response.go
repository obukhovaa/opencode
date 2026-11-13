package tools

import "github.com/opencode-ai/opencode/internal/question"

// FormatQuestionResponse is shared by live question calls and durable recovery.
func FormatQuestionResponse(prompts []question.Prompt, answers [][]string) ToolResponse {
	return (&questionTool{}).formatResponse(prompts, answers)
}
