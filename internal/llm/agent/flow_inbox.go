package agent

import (
	"context"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// WithFlowInboxDrain injects durable interaction-boundary delivery without
// coupling the generic agent loop to the conversation-run controller.
func WithFlowInboxDrain(ctx context.Context, drain func(context.Context, string) ([]message.Message, error)) context.Context {
	return tools.WithFlowInboxHandler(ctx, drain)
}
