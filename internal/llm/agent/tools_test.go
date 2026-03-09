package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type mockTool struct {
	name     string
	baseline bool
}

func (m *mockTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        m.name,
		Description: "mock tool for testing",
		Parameters:  map[string]any{},
	}
}

func (m *mockTool) Run(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse(""), nil
}

func (m *mockTool) AllowParallelism(_ tools.ToolCall, _ []tools.ToolCall) bool {
	return true
}

func (m *mockTool) IsBaseline() bool { return m.baseline }

func newMock(name string, baseline bool) tools.BaseTool {
	return &mockTool{name: name, baseline: baseline}
}

func toolNames(tt []tools.BaseTool) []string {
	names := make([]string, len(tt))
	for i, t := range tt {
		names[i] = t.Info().Name
	}
	return names
}

func TestOrderTools(t *testing.T) {
	tests := []struct {
		name     string
		input    []tools.BaseTool
		expected []string
	}{
		{
			name:     "only baseline tools preserves order",
			input:    []tools.BaseTool{newMock("write", true), newMock("read", true), newMock("bash", true)},
			expected: []string{"write", "read", "bash"},
		},
		{
			name:     "only external tools sorted by name",
			input:    []tools.BaseTool{newMock("mcp_z", false), newMock("mcp_a", false), newMock("mcp_m", false)},
			expected: []string{"mcp_a", "mcp_m", "mcp_z"},
		},
		{
			name: "baseline first then external sorted",
			input: []tools.BaseTool{
				newMock("read", true), newMock("mcp_z", false), newMock("write", true),
				newMock("mcp_a", false), newMock("bash", true), newMock("mcp_m", false),
			},
			expected: []string{"read", "write", "bash", "mcp_a", "mcp_m", "mcp_z"},
		},
		{
			name:     "empty input",
			input:    []tools.BaseTool{},
			expected: []string{},
		},
		{
			name:     "single baseline tool",
			input:    []tools.BaseTool{newMock("read", true)},
			expected: []string{"read"},
		},
		{
			name:     "single external tool",
			input:    []tools.BaseTool{newMock("mcp_only", false)},
			expected: []string{"mcp_only"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := OrderTools(tt.input)
			got := toolNames(result)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d tools, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("tool[%d] = %q, want %q (full: %v)", i, got[i], tt.expected[i], got)
				}
			}
		})
	}
}

func TestOrderToolsDeterminism(t *testing.T) {
	input := []tools.BaseTool{
		newMock("mcp_z", false), newMock("read", true), newMock("mcp_a", false),
		newMock("write", true), newMock("mcp_m", false), newMock("bash", true),
	}

	first := toolNames(OrderTools(input))
	for i := 0; i < 50; i++ {
		got := toolNames(OrderTools(input))
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("iteration %d: tool[%d] = %q, want %q", i, j, got[j], first[j])
			}
		}
	}
}
