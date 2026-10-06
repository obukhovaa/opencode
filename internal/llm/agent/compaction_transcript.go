package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/message"
)

// summarizerToolPayloadMaxTokens caps each tool call input and each tool
// result in the summarizer transcript, in estimated tokens. Tool output is
// most of an agentic session's history, and the summary needs what a tool was
// asked and roughly what it answered, not every byte of it.
const summarizerToolPayloadMaxTokens = 2000

// transcriptStats counts what rendering a history as a transcript changed.
type transcriptStats struct {
	truncatedToolPayloads int
}

// summarizerTranscript renders each message as a text-only message with the
// same ID, role and synthetic flag, so the trim and its pairing rules still
// apply. The summarizer is sent no tools, and a request that replays native
// tool_use / tool_result blocks, server tool-search blocks or another model's
// signed thinking blocks without declaring them can be rejected upstream; a
// proxy in front of the model may answer that with a reset stream instead of a
// 4xx. Text carries everything a summary needs:
//   - tool calls render as "[tool_call <name> id=<id>] <input>";
//   - tool results render as "[tool_result <name> id=<id>] <content>";
//   - a server tool search renders as one line naming the tools it found;
//   - reasoning is dropped;
//   - tool inputs and results over summarizerToolPayloadMaxTokens keep their
//     head and tail around an omission marker.
//
// A message that renders to nothing (reasoning only) is left out.
func summarizerTranscript(msgs []message.Message) ([]message.Message, transcriptStats) {
	var stats transcriptStats
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		var lines []string
		for _, part := range m.Parts {
			switch p := part.(type) {
			case message.TextContent:
				if strings.TrimSpace(p.Text) != "" {
					lines = append(lines, p.Text)
				}
			case message.ToolCall:
				input, cut := capPayload(p.Input, summarizerToolPayloadMaxTokens)
				if cut {
					stats.truncatedToolPayloads++
				}
				lines = append(lines, fmt.Sprintf("[tool_call %s id=%s] %s", p.Name, p.ID, input))
			case message.ToolResult:
				header := fmt.Sprintf("[tool_result %s id=%s]", p.Name, p.ToolCallID)
				if p.IsError {
					header += " (error)"
				}
				content := p.Content
				if p.IsImageToolResponse() {
					content = "(image omitted)"
				}
				content, cut := capPayload(content, summarizerToolPayloadMaxTokens)
				if cut {
					stats.truncatedToolPayloads++
				}
				lines = append(lines, header+" "+content)
			case message.ToolSearchContent:
				switch {
				case p.ErrorCode != "":
					lines = append(lines, fmt.Sprintf("[tool_search] failed: %s", p.ErrorCode))
				case len(p.References) > 0:
					lines = append(lines, "[tool_search] found tools: "+strings.Join(p.References, ", "))
				default:
					lines = append(lines, "[tool_search] found no tools")
				}
			case message.ImageURLContent:
				lines = append(lines, "[image omitted]")
			case message.BinaryContent:
				lines = append(lines, fmt.Sprintf("[attachment %s (%s) omitted]", p.Path, p.MIMEType))
			}
		}
		if len(lines) == 0 {
			continue
		}
		out = append(out, message.Message{
			ID:        m.ID,
			Role:      m.Role,
			SessionID: m.SessionID,
			Model:     m.Model,
			Synthetic: m.Synthetic,
			CreatedAt: m.CreatedAt,
			Parts:     []message.ContentPart{message.TextContent{Text: strings.Join(lines, "\n")}},
		})
	}
	return out, stats
}

// capPayload keeps the head and tail of s when it is over maxTokens estimated
// tokens, with a marker saying how much was left out. Cuts land on rune
// boundaries.
func capPayload(s string, maxTokens int) (string, bool) {
	maxBytes := maxTokens * message.BytesPerTokenEta
	if len(s) <= maxBytes {
		return s, false
	}
	headEnd := maxBytes / 2
	for headEnd > 0 && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - maxBytes/2
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	omitted := (tailStart - headEnd) / message.BytesPerTokenEta
	return fmt.Sprintf("%s\n[... %d tokens omitted ...]\n%s", s[:headEnd], omitted, s[tailStart:]), true
}

// summarizerRequest is what the summarizer is sent: one user message holding
// the transcript and then the compaction prompt. A single message keeps the
// request valid on every provider, whatever the roles of the transcript
// entries (a tool result rendered as text has no role of its own to keep).
func summarizerRequest(transcript []message.Message, prompt message.Message) []message.Message {
	var b strings.Builder
	b.WriteString("The conversation so far, as a transcript. Tool calls and their results appear inline.\n\n<transcript>\n")
	for _, m := range transcript {
		fmt.Fprintf(&b, "\n## %s\n%s\n", m.Role, m.Content().Text)
	}
	b.WriteString("</transcript>\n\n")
	b.WriteString(prompt.Content().Text)
	return []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: b.String()}},
	}}
}
