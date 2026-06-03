package message

// PartEvent is the payload published on the parts pubsub broker whenever a
// single ContentPart transitions state mid-stream (tool pending → running →
// completed/error). Subscribers translate this into SSE `message.part.updated`
// frames. The broker is independent of the message-level broker so per-part
// emission does not serialize against whole-message updates.
type PartEvent struct {
	SessionID string
	MessageID string
	Part      ContentPart
	Time      int64 // unix millis
}

// clonePart snapshots a ContentPart so a subscriber reading later from a
// buffered channel cannot observe in-flight mutation. Our part types are
// pure value structs (ToolCall has string/bool fields; ToolResult has
// string/bool fields and Metadata is a string), so a struct copy via the
// type switch is sufficient — no map/slice/pointer fields need deep copy.
func clonePart(p ContentPart) ContentPart {
	switch v := p.(type) {
	case ToolCall:
		return v
	case ToolResult:
		return v
	case TextContent:
		return v
	case ReasoningContent:
		return v
	case BinaryContent:
		// Data is a []byte — share the underlying array. Producers never mutate
		// existing binary content in place; they replace the part instead.
		return v
	case ImageURLContent:
		return v
	case Finish:
		return v
	default:
		return p
	}
}
