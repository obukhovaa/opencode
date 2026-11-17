package message

// PartEvent is the payload published on the parts pubsub broker whenever a
// single ContentPart transitions state mid-stream (tool pending → running →
// completed/error). Subscribers translate this into SSE `message.part.updated`
// frames. The broker is independent of the message-level broker so per-part
// emission does not serialize against whole-message updates.
//
// Synthetic mirrors the owning Assistant message's `synthetic` flag — set
// when the part comes from a system-injected completion (cron, background
// bash/task/monitor). The chat bridge reads this to skip tool-update
// indicator emission for system-injected messages, so a cron-fired tool
// call doesn't surface a spurious 🔧 in Slack.
type PartEvent struct {
	SessionID string
	MessageID string
	Part      ContentPart
	Synthetic bool
	Time      int64 // unix millis
}

// clonePart snapshots a ContentPart so a subscriber reading later from a
// buffered channel cannot observe in-flight mutation. Most parts contain
// value fields; pointer metadata is copied separately.
func clonePart(p ContentPart) ContentPart {
	switch v := p.(type) {
	case ToolCall:
		return v
	case ToolResult:
		return v
	case TextContent:
		return v
	case ReasoningContent:
		if v.Scalar != nil {
			origin := *v.Scalar
			v.Scalar = &origin
		}
		return v
	case BinaryContent:
		// Data is a []byte — share the underlying array. Producers never mutate
		// existing binary content in place; they replace the part instead.
		return v
	case ImageURLContent:
		return v
	case ToolSearchContent:
		// References is a []string — shared, never mutated in place.
		return v
	case Finish:
		return v
	default:
		return p
	}
}
