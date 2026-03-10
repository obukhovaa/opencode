package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type testTool struct {
	name     string
	baseline bool
}

func (m *testTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        m.name,
		Description: "test",
		Parameters:  map[string]any{},
	}
}

func (m *testTool) Run(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse(""), nil
}

func (m *testTool) AllowParallelism(_ tools.ToolCall, _ []tools.ToolCall) bool {
	return true
}

func (m *testTool) IsBaseline() bool { return m.baseline }

func newTestTool(name string, baseline bool) tools.BaseTool {
	return &testTool{name: name, baseline: baseline}
}

func TestConvertToolsCacheBreakpoints(t *testing.T) {
	tests := []struct {
		name                string
		tools               []tools.BaseTool
		disableCache        bool
		expectedBreakpoints []int
	}{
		{
			name:                "only baseline tools — single breakpoint on last",
			tools:               []tools.BaseTool{newTestTool("read", true), newTestTool("write", true), newTestTool("bash", true)},
			expectedBreakpoints: []int{2},
		},
		{
			name: "baseline plus external — single breakpoint on last",
			tools: []tools.BaseTool{
				newTestTool("read", true), newTestTool("write", true),
				newTestTool("mcp_a", false), newTestTool("mcp_b", false),
			},
			expectedBreakpoints: []int{3},
		},
		{
			name:                "only external tools — single breakpoint on last",
			tools:               []tools.BaseTool{newTestTool("mcp_a", false), newTestTool("mcp_b", false)},
			expectedBreakpoints: []int{1},
		},
		{
			name:                "single tool — breakpoint on it",
			tools:               []tools.BaseTool{newTestTool("read", true)},
			expectedBreakpoints: []int{0},
		},
		{
			name: "cache disabled — no breakpoints",
			tools: []tools.BaseTool{
				newTestTool("read", true), newTestTool("mcp_a", false),
			},
			disableCache:        true,
			expectedBreakpoints: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &anthropicClient{
				options: anthropicOptions{disableCache: tt.disableCache},
			}

			result := client.convertTools(tt.tools)

			breakpointSet := make(map[int]bool)
			for _, idx := range tt.expectedBreakpoints {
				breakpointSet[idx] = true
			}

			for i, tool := range result {
				hasBreakpoint := tool.OfTool != nil && tool.OfTool.CacheControl.Type != ""
				if breakpointSet[i] && !hasBreakpoint {
					t.Errorf("tool[%d] (%s): expected cache breakpoint but none found", i, tt.tools[i].Info().Name)
				}
				if !breakpointSet[i] && hasBreakpoint {
					t.Errorf("tool[%d] (%s): unexpected cache breakpoint", i, tt.tools[i].Info().Name)
				}
			}
		})
	}
}

// newMsg creates a test message with the given role and parts.
func newMsg(role message.MessageRole, parts ...message.ContentPart) message.Message {
	return message.Message{
		Role:  role,
		Parts: parts,
	}
}

func TestConvertMessagesCacheBreakpoints(t *testing.T) {
	tests := []struct {
		name         string
		messages     []message.Message
		disableCache bool
		// expectedCache maps converted-message index → true if any block in that message should have cache_control
		expectedCache map[int]bool
	}{
		{
			name: "user messages — last 2 cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "first"}),
				newMsg(message.User, message.TextContent{Text: "second"}),
				newMsg(message.User, message.TextContent{Text: "third"}),
			},
			expectedCache: map[int]bool{0: false, 1: true, 2: true},
		},
		{
			name: "tool loop — assistant tool_use and tool result both cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "find files"}),
				newMsg(message.Assistant, message.ToolCall{ID: "tc1", Name: "grep", Input: `{}`, Finished: true}),
				newMsg(message.Tool, message.ToolResult{ToolCallID: "tc1", Name: "grep", Content: "result1"}),
				newMsg(message.Assistant, message.ToolCall{ID: "tc2", Name: "read", Input: `{}`, Finished: true}),
				newMsg(message.Tool, message.ToolResult{ToolCallID: "tc2", Name: "read", Content: "result2"}),
			},
			expectedCache: map[int]bool{0: false, 1: false, 2: false, 3: true, 4: true},
		},
		{
			name: "assistant with text and tool_use — cache on last block",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "hello"}),
				newMsg(message.Assistant, message.TextContent{Text: "thinking"}, message.ToolCall{ID: "tc1", Name: "read", Input: `{}`, Finished: true}),
			},
			expectedCache: map[int]bool{0: true, 1: true},
		},
		{
			name: "cache disabled — no markers anywhere",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "first"}),
				newMsg(message.Assistant, message.TextContent{Text: "response"}),
			},
			disableCache:  true,
			expectedCache: map[int]bool{0: false, 1: false},
		},
		{
			name: "single user message — cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "hello"}),
			},
			expectedCache: map[int]bool{0: true},
		},
		{
			name: "multiple tool results in one message — cache on last result",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "do stuff"}),
				newMsg(message.Assistant,
					message.ToolCall{ID: "tc1", Name: "read", Input: `{}`, Finished: true},
					message.ToolCall{ID: "tc2", Name: "grep", Input: `{}`, Finished: true},
				),
				newMsg(message.Tool,
					message.ToolResult{ToolCallID: "tc1", Name: "read", Content: "file contents"},
					message.ToolResult{ToolCallID: "tc2", Name: "grep", Content: "grep results"},
				),
			},
			expectedCache: map[int]bool{0: false, 1: true, 2: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &anthropicClient{
				options: anthropicOptions{disableCache: tt.disableCache},
			}

			result := client.convertMessages(tt.messages)

			if len(result) > len(tt.messages) {
				t.Fatalf("got %d converted messages, expected at most %d", len(result), len(tt.messages))
			}

			for i, mp := range result {
				expectCached, exists := tt.expectedCache[i]
				if !exists {
					continue
				}

				hasCached := false
				for _, block := range mp.Content {
					if block.OfText != nil && block.OfText.CacheControl.Type != "" {
						hasCached = true
					}
					if block.OfToolUse != nil && block.OfToolUse.CacheControl.Type != "" {
						hasCached = true
					}
					if block.OfToolResult != nil && block.OfToolResult.CacheControl.Type != "" {
						hasCached = true
					}
				}

				if expectCached && !hasCached {
					t.Errorf("message[%d]: expected cache breakpoint but none found", i)
				}
				if !expectCached && hasCached {
					t.Errorf("message[%d]: unexpected cache breakpoint", i)
				}
			}
		})
	}
}
