package provider

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

func newTestProvider() *baseProvider[AnthropicClient] {
	return &baseProvider[AnthropicClient]{}
}

func TestSanitizeToolPairs(t *testing.T) {
	tests := []struct {
		name           string
		messages       []message.Message
		wantMsgCount   int
		wantToolCount  func([]message.Message) int
		wantToolResult func([]message.Message, *testing.T)
	}{
		{
			name: "valid pair passes through unchanged",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: "calling tool"},
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "tc-1", Name: "bash", Content: "ok"},
					},
				},
			},
			wantMsgCount: 3,
		},
		{
			name: "orphaned tool_use with no following tool message gets synthetic results",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "next message"},
					},
				},
			},
			wantMsgCount: 4,
			wantToolResult: func(msgs []message.Message, t *testing.T) {
				toolMsg := msgs[2]
				if toolMsg.Role != message.Tool {
					t.Errorf("expected synthesized tool message at index 2, got role %s", toolMsg.Role)
				}
				results := toolMsg.ToolResults()
				if len(results) != 1 {
					t.Fatalf("expected 1 tool result, got %d", len(results))
				}
				if results[0].ToolCallID != "tc-1" {
					t.Errorf("expected tool call ID tc-1, got %s", results[0].ToolCallID)
				}
				if !results[0].IsError {
					t.Error("expected synthesized tool result to be an error")
				}
			},
		},
		{
			name: "incomplete tool results - missing results for some tool_use IDs get synthesized",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
						message.ToolCall{ID: "tc-2", Name: "view", Input: "{}", Finished: true},
						message.ToolCall{ID: "tc-3", Name: "grep", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "tc-1", Name: "bash", Content: "ok"},
					},
				},
			},
			wantMsgCount: 3,
			wantToolResult: func(msgs []message.Message, t *testing.T) {
				toolMsg := msgs[2]
				if toolMsg.Role != message.Tool {
					t.Fatalf("expected tool message at index 2, got role %s", toolMsg.Role)
				}
				results := toolMsg.ToolResults()
				if len(results) != 3 {
					t.Fatalf("expected 3 tool results (1 real + 2 synthesized), got %d", len(results))
				}
				resultByID := make(map[string]message.ToolResult, len(results))
				for _, r := range results {
					resultByID[r.ToolCallID] = r
				}
				if r, ok := resultByID["tc-1"]; !ok {
					t.Error("missing result for tc-1")
				} else if r.IsError {
					t.Error("tc-1 should not be an error")
				}
				if r, ok := resultByID["tc-2"]; !ok {
					t.Error("missing synthesized result for tc-2")
				} else if !r.IsError {
					t.Error("tc-2 should be an error (synthesized)")
				}
				if r, ok := resultByID["tc-3"]; !ok {
					t.Error("missing synthesized result for tc-3")
				} else if !r.IsError {
					t.Error("tc-3 should be an error (synthesized)")
				}
			},
		},
		{
			name: "tool results with only finish parts and no tool_result parts get synthesized",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
						message.ToolCall{ID: "tc-2", Name: "view", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.Finish{Reason: message.FinishReasonEndTurn},
					},
				},
			},
			wantMsgCount: 3,
			wantToolResult: func(msgs []message.Message, t *testing.T) {
				toolMsg := msgs[2]
				results := toolMsg.ToolResults()
				if len(results) != 2 {
					t.Fatalf("expected 2 synthesized tool results, got %d", len(results))
				}
				for _, r := range results {
					if !r.IsError {
						t.Errorf("expected synthesized result for %s to be error", r.ToolCallID)
					}
				}
			},
		},
		{
			name: "mismatched tool_result IDs get fixed positionally",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "wrong-id", Name: "bash", Content: "ok"},
					},
				},
			},
			wantMsgCount: 3,
			wantToolResult: func(msgs []message.Message, t *testing.T) {
				toolMsg := msgs[2]
				results := toolMsg.ToolResults()
				if len(results) != 1 {
					t.Fatalf("expected 1 tool result, got %d", len(results))
				}
				if results[0].ToolCallID != "tc-1" {
					t.Errorf("expected tool call ID to be fixed to tc-1, got %s", results[0].ToolCallID)
				}
			},
		},
		{
			name: "orphaned tool result without preceding assistant is skipped",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "tc-1", Name: "bash", Content: "ok"},
					},
				},
			},
			wantMsgCount: 1,
		},
		{
			name: "multiple tool use cycles with incomplete second pair",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "tc-1", Name: "bash", Content: "ok"},
					},
				},
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.ToolCall{ID: "tc-2", Name: "view", Input: "{}", Finished: true},
						message.ToolCall{ID: "tc-3", Name: "grep", Input: "{}", Finished: true},
					},
				},
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "tc-2", Name: "view", Content: "file contents"},
					},
				},
			},
			wantMsgCount: 5,
			wantToolResult: func(msgs []message.Message, t *testing.T) {
				// First pair should be unchanged
				firstToolMsg := msgs[2]
				firstResults := firstToolMsg.ToolResults()
				if len(firstResults) != 1 || firstResults[0].ToolCallID != "tc-1" {
					t.Error("first tool pair should be unchanged")
				}

				// Second pair should have synthesized result for tc-3
				secondToolMsg := msgs[4]
				secondResults := secondToolMsg.ToolResults()
				if len(secondResults) != 2 {
					t.Fatalf("expected 2 tool results in second pair (1 real + 1 synthesized), got %d", len(secondResults))
				}
				resultByID := make(map[string]message.ToolResult, len(secondResults))
				for _, r := range secondResults {
					resultByID[r.ToolCallID] = r
				}
				if r, ok := resultByID["tc-2"]; !ok || r.IsError {
					t.Error("tc-2 result should exist and not be an error")
				}
				if r, ok := resultByID["tc-3"]; !ok || !r.IsError {
					t.Error("tc-3 should have a synthesized error result")
				}
			},
		},
		{
			name:         "empty messages returns empty",
			messages:     []message.Message{},
			wantMsgCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider()
			result := p.sanitizeToolPairs(tt.messages)
			if len(result) != tt.wantMsgCount {
				t.Errorf("expected %d messages, got %d", tt.wantMsgCount, len(result))
				for i, m := range result {
					t.Logf("  msg[%d]: role=%s parts=%d toolCalls=%d toolResults=%d", i, m.Role, len(m.Parts), len(m.ToolCalls()), len(m.ToolResults()))
				}
			}
			if tt.wantToolResult != nil {
				tt.wantToolResult(result, t)
			}
		})
	}
}

func TestCleanMessages(t *testing.T) {
	tests := []struct {
		name         string
		messages     []message.Message
		wantMsgCount int
	}{
		{
			name: "removes empty messages",
			messages: []message.Message{
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
				{Role: message.Assistant, Parts: []message.ContentPart{}},
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "world"}}},
			},
			wantMsgCount: 2,
		},
		{
			name: "removes assistant with only finish part",
			messages: []message.Message{
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
				{Role: message.Assistant, Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonCanceled}}},
			},
			wantMsgCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider()
			result := p.cleanMessages(tt.messages)
			if len(result) != tt.wantMsgCount {
				t.Errorf("expected %d messages, got %d", tt.wantMsgCount, len(result))
			}
		})
	}
}
