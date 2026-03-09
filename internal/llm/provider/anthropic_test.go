package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
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
			name: "baseline plus external — two breakpoints",
			tools: []tools.BaseTool{
				newTestTool("read", true), newTestTool("write", true),
				newTestTool("mcp_a", false), newTestTool("mcp_b", false),
			},
			expectedBreakpoints: []int{1, 3},
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
