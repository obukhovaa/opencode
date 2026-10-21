package agent

import (
	"context"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

func (a *agent) withMemoryIndex(ctx context.Context, sessionID string, history []message.Message) []message.Message {
	m := prompt.ResolveMemoryContext(config.Get(), agentregistry.GetRegistry(), a.AgentID())
	return a.withResolvedMemoryIndex(ctx, sessionID, history, m)
}

func (a *agent) withResolvedMemoryIndex(ctx context.Context, sessionID string, history []message.Message, m prompt.MemoryContext) []message.Message {
	if !m.Preload {
		return history
	}
	for _, msg := range history {
		if msg.Synthetic && strings.HasPrefix(msg.Content().String(), "<system-reminder>\n"+m.Marker()+"\n") {
			return history
		}
	}
	snapshot := m.Snapshot()
	if snapshot == "" {
		return history
	}
	msg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{Role: message.User, Synthetic: true, Parts: []message.ContentPart{message.TextContent{Text: snapshot}}})
	if err != nil {
		logging.Warn("Failed to load project memory index", "error", err)
		return history
	}
	return append(history, msg)
}
