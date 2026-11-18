package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

const replayComposeProjection = `{"complete":true,"results":{"read":{"id":123}}}`

func replayToolCall(id, name string, synthetic bool) message.Message {
	return message.Message{
		ID: id + "-call", SessionID: "session", Role: message.Assistant, Synthetic: synthetic,
		Parts: []message.ContentPart{message.ToolCall{ID: id, Name: name, Input: `{}`, Finished: true}},
	}
}

func replayToolResult(id, name, content string, synthetic bool) message.Message {
	return message.Message{
		ID: id + "-result", SessionID: "session", Role: message.Tool, Synthetic: synthetic,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: name, Content: content, Metadata: `{"retained":true}`}},
	}
}

func replayInterruption(id, name string) message.Message {
	return message.Message{
		SessionID: "session", Role: message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: name, Content: "Tool execution was interrupted", IsError: true}},
	}
}

func TestSanitizeToolPairsBackgroundReplay(t *testing.T) {
	compose := replayToolCall("compose", "mcp_compose", false)
	projection := replayToolResult("compose", "mcp_compose", replayComposeProjection, false)
	read := replayToolCall("read", "mcp_compose", false) // Same name, different ID.
	readResult := replayToolResult("read", "mcp_compose", "second projection", false)
	batch := compose
	batch.Parts = append(append([]message.ContentPart(nil), compose.Parts...), read.Parts...)
	combined := projection
	combined.Parts = []message.ContentPart{projection.Parts[0], readResult.Parts[0]}
	partial := projection
	partial.Parts = []message.ContentPart{projection.Parts[0], replayInterruption("read", "mcp_compose").Parts[0]}
	monitor := replayToolCall("event-1", "monitor", true)
	event := replayToolResult("event-1", "monitor", "event during-compose", true)
	monitor2 := replayToolCall("event-2", "monitor", true)
	event2 := replayToolResult("event-2", "monitor", "second monitor event", true)
	yield := replayToolCall("wait", "yield", false)
	yieldResult := replayToolResult("wait", "yield", "Waiting for a background report", false)
	user := message.Message{Role: message.User, SessionID: "session", Parts: []message.ContentPart{message.TextContent{Text: "continue"}}}
	assistant := message.Message{Role: message.Assistant, SessionID: "session", Parts: []message.ContentPart{message.TextContent{Text: "interrupted turn"}}}
	failed := projection
	failed.Parts = []message.ContentPart{message.ToolResult{ToolCallID: "compose", Name: "mcp_compose", Content: "actual tool failure", IsError: true}}

	tests := []struct {
		name string
		in   []message.Message
		want []message.Message
	}{
		{
			name: "persisted compose projection survives interactive yield resume",
			in:   []message.Message{user, compose, monitor, event, projection, yield, yieldResult, monitor2, event2, user},
			want: []message.Message{user, compose, projection, monitor, event, yield, yieldResult, monitor2, event2, user},
		},
		{
			name: "consecutive notifications stay complete and ordered",
			in:   []message.Message{compose, monitor, event, monitor2, event2, projection},
			want: []message.Message{compose, projection, monitor, event, monitor2, event2},
		},
		{
			name: "notification before split regular results waits for whole batch",
			in:   []message.Message{batch, monitor, event, projection, readResult},
			want: []message.Message{batch, combined, monitor, event},
		},
		{
			name: "notification between regular results waits for whole batch",
			in:   []message.Message{batch, projection, monitor, event, readResult},
			want: []message.Message{batch, combined, monitor, event},
		},
		{
			name: "several notifications interleaved with results",
			in:   []message.Message{batch, monitor, event, projection, monitor2, event2, readResult},
			want: []message.Message{batch, combined, monitor, event, monitor2, event2},
		},
		{
			name: "out of order results matched by ID",
			in:   []message.Message{batch, readResult, monitor, event, projection},
			want: []message.Message{batch, {ID: readResult.ID, SessionID: "session", Role: message.Tool, Parts: []message.ContentPart{readResult.Parts[0], projection.Parts[0]}}, monitor, event},
		},
		{
			name: "separate tool batches keep their own results and notifications",
			in:   []message.Message{compose, monitor, event, projection, read, monitor2, event2, readResult},
			want: []message.Message{compose, projection, monitor, event, read, readResult, monitor2, event2},
		},
		{
			name: "actual tool error is preserved",
			in:   []message.Message{compose, monitor, event, failed},
			want: []message.Message{compose, failed, monitor, event},
		},
		{
			name: "actual missing result remains interrupted",
			in:   []message.Message{compose, monitor, event},
			want: []message.Message{compose, replayInterruption("compose", "mcp_compose"), monitor, event},
		},
		{
			name: "partially complete batch preserves success and exposes missing result",
			in:   []message.Message{batch, monitor, event, projection},
			want: []message.Message{batch, partial, monitor, event},
		},
		{
			name: "user interruption cannot be crossed to find a late result",
			in:   []message.Message{compose, monitor, event, user, projection},
			want: []message.Message{compose, replayInterruption("compose", "mcp_compose"), monitor, event, user},
		},
		{
			name: "real assistant text cannot be crossed to find a late result",
			in:   []message.Message{compose, monitor, event, assistant, projection},
			want: []message.Message{compose, replayInterruption("compose", "mcp_compose"), monitor, event, assistant},
		},
		{
			name: "new regular batch does not complete an interrupted batch",
			in:   []message.Message{compose, monitor, event, read, readResult, projection},
			want: []message.Message{compose, replayInterruption("compose", "mcp_compose"), monitor, event, read, readResult},
		},
		{
			name: "matching tool name with unrelated ID cannot hide interruption",
			in:   []message.Message{compose, monitor, event, readResult},
			want: []message.Message{compose, replayInterruption("compose", "mcp_compose"), monitor, event},
		},
		{
			name: "partial result before user interruption stays partial",
			in:   []message.Message{batch, monitor, event, projection, user, readResult},
			want: []message.Message{batch, partial, monitor, event, user},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			p := newTestProvider()
			got := p.sanitizeToolPairs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("replayed history:\n%+v\nwant:\n%+v", got, tt.want)
			}
			if again := p.sanitizeToolPairs(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("sanitization is not idempotent:\n%+v\nthen:\n%+v", got, again)
			}
			after, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("replay modified persisted input history")
			}
		})
	}
}

func TestNormalizeToolBatchesLeavesOtherHistoryUnchanged(t *testing.T) {
	compose := replayToolCall("compose", "mcp_compose", false)
	projection := replayToolResult("compose", "mcp_compose", replayComposeProjection, false)
	monitor := replayToolCall("event", "monitor", true)
	event := replayToolResult("event", "monitor", "background report", true)
	wrongID := replayToolResult("different-event", "monitor", "unrelated report", true)
	realMonitor, realEvent := monitor, event
	realMonitor.Synthetic, realEvent.Synthetic = false, false
	otherSession := replayToolResult("compose", "mcp_compose", "another session", false)
	otherSession.SessionID = "other-session"
	otherMonitor, otherEvent := monitor, event
	otherMonitor.SessionID, otherEvent.SessionID = "other-session", "other-session"
	tests := []struct {
		name string
		in   []message.Message
	}{
		{"already ordered", []message.Message{compose, projection, monitor, event}},
		{"ordinary pair is not a notification", []message.Message{compose, realMonitor, realEvent, projection}},
		{"both synthetic flags required", []message.Message{compose, monitor, realEvent, projection}},
		{"synthetic tool alone is not a pair", []message.Message{compose, event, projection}},
		{"incomplete notification", []message.Message{compose, monitor, projection}},
		{"notification IDs must match", []message.Message{compose, monitor, wrongID, projection}},
		{"unrelated tool result is a boundary", []message.Message{compose, monitor, event, replayToolResult("other", "mcp_compose", "unrelated", false), projection}},
		{"session IDs must match", []message.Message{compose, monitor, event, otherSession}},
		{"notification from another session is a boundary", []message.Message{compose, otherMonitor, otherEvent, projection}},
		{"ordinary missing result", []message.Message{compose}},
		{"ordinary proxy ID recovery", []message.Message{compose, replayToolResult("proxy-id", "mcp_compose", replayComposeProjection, false)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeToolBatches(tt.in); !reflect.DeepEqual(got, tt.in) {
				t.Fatalf("changed history outside a complete background pair:\n%+v\nwant:\n%+v", got, tt.in)
			}
		})
	}
}

// Observe the shared provider boundary so the regression covers both request
// paths independently of a particular vendor's message serializer.
type historyReplayClient struct {
	fakeCountClient
	messages []message.Message
}

func (c *historyReplayClient) send(_ context.Context, messages []message.Message, _ []tools.BaseTool) (*ProviderResponse, error) {
	c.messages = messages
	return &ProviderResponse{}, nil
}

func (c *historyReplayClient) stream(_ context.Context, messages []message.Message, _ []tools.BaseTool) <-chan ProviderEvent {
	c.messages = messages
	events := make(chan ProviderEvent, 1)
	events <- ProviderEvent{Type: EventComplete, Response: &ProviderResponse{}}
	close(events)
	return events
}

func TestProviderBackgroundReplaySendAndStream(t *testing.T) {
	for _, name := range []string{"monitor", "bash", "task"} {
		for _, stream := range []bool{false, true} {
			method := "send"
			if stream {
				method = "stream"
			}
			t.Run(name+"/"+method, func(t *testing.T) {
				compose := replayToolCall("compose", "mcp_compose", false)
				projection := replayToolResult("compose", "mcp_compose", replayComposeProjection, false)
				notification := replayToolCall("background", name, true)
				report := replayToolResult("background", name, "background report", true)
				history := []message.Message{compose, notification, report, projection}
				want := []message.Message{compose, projection, notification, report}
				client := &historyReplayClient{}
				p := &baseProvider[*historyReplayClient]{client: client}
				var err error
				if stream {
					_, err = StreamToResponse(p.StreamResponse(context.Background(), history, nil))
				} else {
					_, err = p.SendMessages(context.Background(), history, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(client.messages, want) {
					t.Fatalf("provider received:\n%+v\nwant:\n%+v", client.messages, want)
				}
			})
		}
	}
}
