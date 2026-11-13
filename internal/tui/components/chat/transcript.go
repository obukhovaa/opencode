package chat

import (
	"github.com/opencode-ai/opencode/internal/message"
	"strings"
)

// RenderTranscript reuses chat's message and tool presentation for a bounded
// subagent snapshot. Callers load the snapshot outside the UI event loop.
func RenderTranscript(messages []message.Message, width int) string {
	var sections []string
	for i, msg := range messages {
		switch msg.Role {
		case message.User:
			sections = append(sections, renderUserMessage(msg, false, width, 0, true).content)
		case message.Assistant:
			for _, part := range renderAssistantMessage(msg, i, messages, nil, "", false, width, 0, true) {
				sections = append(sections, part.content)
			}
		}
	}
	return strings.Join(sections, "\n\n")
}
