package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestResolveCallToolMaxOutputBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  int
		want int
	}{
		{"unset uses default", 0, mcpCallToolMaxOutputBytes},
		{"positive overrides", 4096, 4096},
		{"negative disables (unlimited)", -5, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCallToolMaxOutputBytes(config.MCPServer{CallToolMaxOutputBytes: tt.cfg})
			if got != tt.want {
				t.Errorf("resolveCallToolMaxOutputBytes(%d) = %d, want %d", tt.cfg, got, tt.want)
			}
		})
	}
}

type fakeMCPClient struct {
	result *mcp.CallToolResult
}

func (f *fakeMCPClient) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	return &mcp.InitializeResult{}, nil
}
func (f *fakeMCPClient) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}
func (f *fakeMCPClient) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return f.result, nil
}
func (f *fakeMCPClient) Close() error { return nil }

func textResult(blocks ...string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	for _, b := range blocks {
		r.Content = append(r.Content, mcp.TextContent{Type: "text", Text: b})
	}
	return r
}

func TestRunToolOutputCap(t *testing.T) {
	t.Cleanup(tools.CleanupTempDir)
	ctx := context.Background()

	t.Run("small output is returned unchanged", func(t *testing.T) {
		c := &fakeMCPClient{result: textResult("hello world")}
		resp, err := runTool(ctx, c, "some_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if resp.Content != "hello world" {
			t.Errorf("small output altered: %q", resp.Content)
		}
	})

	t.Run("oversized output is capped and spilled to a file", func(t *testing.T) {
		big := strings.Repeat("X", 200_000) // ~200KB, over the 50KB default
		c := &fakeMCPClient{result: textResult(big)}
		resp, err := runTool(ctx, c, "big_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if len(resp.Content) >= len(big) {
			t.Errorf("output not capped: %d bytes (input %d)", len(resp.Content), len(big))
		}
		if !strings.Contains(resp.Content, "output truncated") || !strings.Contains(resp.Content, "Full output saved to:") {
			t.Errorf("capped output missing overflow header; got:\n%s", resp.Content[:min(300, len(resp.Content))])
		}
		if resp.IsError {
			t.Error("capping should not mark the response as an error")
		}
	})

	t.Run("multi-block output is concatenated before capping", func(t *testing.T) {
		// With the cap disabled, both blocks must survive (regression guard for the
		// old loop that kept only the last block).
		c := &fakeMCPClient{result: textResult("AAAA", "BBBB")}
		resp, err := runTool(ctx, c, "multi_tool", "{}", mcpCallToolTimeout, -1)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if resp.Content != "AAAABBBB" {
			t.Errorf("multi-block not concatenated: got %q, want %q", resp.Content, "AAAABBBB")
		}
	})
}
