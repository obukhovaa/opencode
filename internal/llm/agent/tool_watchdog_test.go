package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// stuckTool ignores its ctx and never returns until released.
type stuckTool struct {
	name    string
	release chan struct{}
}

func (s *stuckTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: s.name} }
func (s *stuckTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	<-s.release
	return tools.NewTextResponse("late"), nil
}
func (s *stuckTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (s *stuckTool) IsBaseline() bool                                       { return false }

func TestRunToolBoundedAbandonsAToolIgnoringItsCtx(t *testing.T) {
	tool := &stuckTool{name: "datadog_search_logs", release: make(chan struct{})}
	defer close(tool.release)

	start := time.Now()
	resp, err := runToolBounded(context.Background(), 50*time.Millisecond, tool, tools.ToolCall{ID: "1", Name: tool.name})
	if err != nil {
		t.Fatalf("err = %v, want an error result instead", err)
	}
	if !resp.IsError || !strings.Contains(resp.Content, "exceeded") {
		t.Fatalf("resp = %+v, want an 'exceeded' error result", resp)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("watchdog took %s", elapsed)
	}
}

func TestRunToolBoundedReturnsOnParentCancel(t *testing.T) {
	tool := &stuckTool{name: "datadog_search_logs", release: make(chan struct{})}
	defer close(tool.release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err := runToolBounded(ctx, time.Hour, tool, tools.ToolCall{ID: "1", Name: tool.name})
	if err == nil {
		t.Fatal("err = nil, want the parent's cancellation")
	}
}

func TestRunToolBoundedPassesThroughFastResults(t *testing.T) {
	tool := &stuckTool{name: "view", release: make(chan struct{})}
	close(tool.release)
	resp, err := runToolBounded(context.Background(), time.Second, tool, tools.ToolCall{ID: "1", Name: "view"})
	if err != nil || resp.Content != "late" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestToolCallLimit(t *testing.T) {
	seedMCPServers(t, map[string]config.MCPServer{
		"slow": {Type: config.MCPStdio, Command: "true", CallToolTimeoutSeconds: 1800},
	})
	def := time.Duration(config.DefaultToolCallTimeoutSeconds) * time.Second

	for _, name := range []string{tools.BashToolName, tools.QuestionToolName, TaskToolName} {
		if got := toolCallLimit(config.AgentCoder, &stuckTool{name: name}); got != 0 {
			t.Errorf("%s limit = %s, want exempt (0)", name, got)
		}
	}
	if got := toolCallLimit(config.AgentCoder, &stuckTool{name: "view"}); got != def {
		t.Errorf("view limit = %s, want the default %s", got, def)
	}
	slow := &mcpTool{mcpName: "slow", tool: mcp.NewTool("query")}
	want := 1800*time.Second + toolCallTimeoutMargin
	if got := toolCallLimit(config.AgentCoder, slow); got != want {
		t.Errorf("mcp limit = %s, want the server timeout plus margin %s", got, want)
	}
	if got := toolCallLimit(config.AgentCoder, tools.WrapDeferred(slow, nil)); got != want {
		t.Errorf("deferred mcp limit = %s, want %s", got, want)
	}

	zero := 0
	config.Get().ToolCallTimeoutSeconds = &zero
	t.Cleanup(func() { config.Get().ToolCallTimeoutSeconds = nil })
	if got := toolCallLimit(config.AgentCoder, &stuckTool{name: "view"}); got != 0 {
		t.Errorf("limit with toolCallTimeoutSeconds=0 = %s, want disabled", got)
	}
}
