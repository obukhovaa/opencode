package provider

import (
	"fmt"

	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/message"
)

// genChatMessage is the Langfuse-facing rendering of one request message.
// The shape mirrors the common chat format (role/content/tool_calls) so the
// Langfuse UI renders the generation input as a conversation.
type genChatMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []genToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type genToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// genOutput is the Langfuse-facing rendering of the LLM response.
type genOutput struct {
	Role         string        `json:"role"`
	Content      string        `json:"content,omitempty"`
	Reasoning    string        `json:"reasoning,omitempty"`
	ToolCalls    []genToolCall `json:"tool_calls,omitempty"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

// generationInput renders the LLM request payload for the generation's
// telemetry input, or nil when the agent is not opted in via
// telemetry.generations.logInput. Returns `any` so an opted-out agent yields a
// genuinely nil interface — a typed nil slice would still be recorded.
func generationInput(agentID, system string, messages []message.Message) any {
	if !langfuse.ShouldLogGenerationInput(agentID) {
		return nil
	}
	return buildGenerationInput(system, messages)
}

// buildGenerationInput renders the exact request that goes to the LLM —
// system prompt plus the (cleaned) message history — as a chat-format
// message list for the generation's Langfuse input. Binary payloads are
// summarized, never embedded.
func buildGenerationInput(system string, messages []message.Message) []genChatMessage {
	out := make([]genChatMessage, 0, len(messages)+1)
	if system != "" {
		out = append(out, genChatMessage{Role: "system", Content: system})
	}
	for i := range messages {
		out = append(out, renderMessage(&messages[i])...)
	}
	return out
}

// renderMessage converts one internal message into its chat-format
// rendering. A Tool message fans out into one entry per tool result so
// each result keeps its tool_call_id pairing.
func renderMessage(msg *message.Message) []genChatMessage {
	if msg.Role == message.Tool {
		results := msg.ToolResults()
		out := make([]genChatMessage, 0, len(results))
		for _, tr := range results {
			content := tr.Content
			if tr.IsError {
				content = "[error] " + content
			}
			out = append(out, genChatMessage{
				Role:       "tool",
				ToolCallID: tr.ToolCallID,
				Name:       tr.Name,
				Content:    content,
			})
		}
		return out
	}

	m := genChatMessage{Role: string(msg.Role)}
	appendContent := func(s string) {
		if m.Content != "" {
			m.Content += "\n"
		}
		m.Content += s
	}
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case message.TextContent:
			appendContent(p.Text)
		case message.ImageURLContent:
			appendContent(fmt.Sprintf("[image: %s]", p.URL))
		case message.BinaryContent:
			appendContent(fmt.Sprintf("[binary attachment: %s, %d bytes]", p.MIMEType, len(p.Data)))
		case message.ToolCall:
			m.ToolCalls = append(m.ToolCalls, genToolCall{ID: p.ID, Name: p.Name, Arguments: p.Input})
		}
	}
	return []genChatMessage{m}
}

// buildGenerationOutput renders the LLM response for the generation's
// Langfuse output.
func buildGenerationOutput(resp *ProviderResponse) *genOutput {
	if resp == nil {
		return nil
	}
	out := &genOutput{
		Role:         "assistant",
		Content:      resp.Content,
		FinishReason: string(resp.FinishReason),
	}
	for _, r := range resp.Reasoning {
		if r.Thinking == "" {
			continue
		}
		if out.Reasoning != "" {
			out.Reasoning += "\n"
		}
		out.Reasoning += r.Thinking
	}
	for _, tc := range resp.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, genToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Input})
	}
	return out
}
