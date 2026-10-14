package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
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
		if got := toolCallLimit(config.AgentCoder, nil, &stuckTool{name: name}); got != 0 {
			t.Errorf("%s limit = %s, want exempt (0)", name, got)
		}
	}
	if got := toolCallLimit(config.AgentCoder, nil, &stuckTool{name: "view"}); got != def {
		t.Errorf("view limit = %s, want the default %s", got, def)
	}
	slow := &mcpTool{mcpName: "slow", tool: mcp.NewTool("query")}
	want := 1800*time.Second + toolCallTimeoutMargin
	if got := toolCallLimit(config.AgentCoder, nil, slow); got != want {
		t.Errorf("mcp limit = %s, want the server timeout plus margin %s", got, want)
	}
	if got := toolCallLimit(config.AgentCoder, nil, tools.WrapDeferred(slow, nil)); got != want {
		t.Errorf("deferred mcp limit = %s, want %s", got, want)
	}

	zero := 0
	config.Get().ToolCallTimeoutSeconds = &zero
	t.Cleanup(func() { config.Get().ToolCallTimeoutSeconds = nil })
	if got := toolCallLimit(config.AgentCoder, nil, &stuckTool{name: "view"}); got != 0 {
		t.Errorf("limit with toolCallTimeoutSeconds=0 = %s, want disabled", got)
	}
}

// permissionTool asks for a permission, then works for workFor (or until
// released when hangAfter is set).
type permissionTool struct {
	perms     permission.Service
	workFor   time.Duration
	hangAfter bool
	release   chan struct{}
}

func (p *permissionTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: "ask_then_work"} }
func (p *permissionTool) Run(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	if !p.perms.Request(ctx, permission.CreatePermissionRequest{SessionID: "s1", ToolName: "ask_then_work", Action: "run", Path: "/tmp/x"}) {
		return tools.NewTextErrorResponse("denied"), nil
	}
	if p.hangAfter {
		<-p.release
	} else {
		time.Sleep(p.workFor)
	}
	return tools.NewTextResponse("done"), nil
}
func (p *permissionTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (p *permissionTool) IsBaseline() bool                                       { return false }

func TestRunToolBoundedDoesNotCountPermissionWait(t *testing.T) {
	const (
		limit      = 150 * time.Millisecond
		promptWait = 400 * time.Millisecond
	)
	tests := []struct {
		name        string
		hangAfter   bool
		workFor     time.Duration
		wantTimeout bool
		minElapsed  time.Duration
	}{
		{name: "prompt wait longer than the limit does not trip it", workFor: 20 * time.Millisecond},
		{name: "work after the prompt is still bounded", hangAfter: true, wantTimeout: true, minElapsed: promptWait + limit - 20*time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perms := permission.NewPermissionService()
			events := perms.Subscribe(context.Background())
			go func() {
				ev := <-events
				time.Sleep(promptWait)
				perms.Grant(ev.Payload)
			}()
			tool := &permissionTool{perms: perms, workFor: tt.workFor, hangAfter: tt.hangAfter, release: make(chan struct{})}
			defer close(tool.release)

			start := time.Now()
			resp, err := runToolBounded(context.Background(), limit, tool, tools.ToolCall{ID: "1", Name: "ask_then_work"})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			timedOut := resp.IsError && strings.Contains(resp.Content, "exceeded")
			if timedOut != tt.wantTimeout {
				t.Fatalf("resp = %+v, timed out = %v, want %v", resp, timedOut, tt.wantTimeout)
			}
			if elapsed := time.Since(start); elapsed < tt.minElapsed {
				t.Errorf("returned after %s, want at least %s: the prompt wait was counted", elapsed, tt.minElapsed)
			}
		})
	}
}
