package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

func TestCompositionOuterPolicyDenyWinsHookAllow(t *testing.T) {
	registry, leaf := compositionTestSetup(t, permission.ActionAllow, func(context.Context, string) tools.ToolResponse {
		t.Error("child ran despite outer denial")
		return compositionText(`{"id":1}`)
	})
	registry.outer = permission.ActionDeny
	dir := t.TempDir()
	script := writeScript(t, dir, "allow.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\"}}'\n")
	hookRegistry := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {{Matcher: "mcp_compose", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}}}}), dir)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(message.ToolCall{ID: "c", Name: tools.MCPComposeToolName, Input: compositionPlan(1), Finished: true})
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
	a.reg = registry
	a.factory = &fakeFactoryWithHooks{reg: hookRegistry}
	result := a.processGeneration(compositionTestContext("s-outer-denied"), "s-outer-denied", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	results := toolResultsOf(t, a, "s-outer-denied")
	if len(results) != 1 || !results[0].IsError || !strings.Contains(results[0].Content, "configured permission rule") {
		t.Fatalf("outer permission bypass: %+v", results)
	}
}

func TestCompositionOuterAskRejectedBeforeEngine(t *testing.T) {
	registry, leaf := compositionTestSetup(t, permission.ActionAllow, func(context.Context, string) tools.ToolResponse {
		t.Error("child ran despite outer rejection")
		return compositionText(`{"id":1}`)
	})
	registry.outer = permission.ActionAsk
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(message.ToolCall{ID: "c", Name: tools.MCPComposeToolName, Input: compositionPlan(1), Finished: true})
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
	a.reg = registry
	a.permissions = permission.NewPermissionService()
	ctx, cancel := context.WithTimeout(compositionTestContext("s-outer-ask"), 2*time.Second)
	defer cancel()
	events := a.permissions.Subscribe(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case e := <-events:
			a.permissions.Deny(e.Payload)
		case <-ctx.Done():
		}
	}()
	result := a.processGeneration(ctx, "s-outer-ask", "read", 0, nil, RunOptions{NonInteractive: true})
	<-done
	if result.Error != nil || result.Message.FinishReason() != message.FinishReasonPermissionDenied || p.callCount() != 1 {
		t.Fatalf("outer rejection lost: %+v turns=%d", result, p.callCount())
	}
}

func sameNameMCP(t *testing.T, run func(context.Context, string) tools.ToolResponse) tools.BaseTool {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{"mcp": {Type: config.MCPStdio, Command: "unused"}})
	cfg := config.Get()
	old := cfg.MCPComposition
	cfg.MCPComposition = nil
	t.Cleanup(func() { cfg.MCPComposition = old })
	return newMCPTool("mcp", mcp.NewTool("compose"), permission.NewPermissionService(), &dispatchRegistry{action: permission.ActionAllow}, dispatchMCPRegistry{run: run})
}

func TestSameNamedMCPRetainsWatchdog(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	leaf := sameNameMCP(t, func(context.Context, string) tools.ToolResponse { <-release; return tools.NewTextResponse("late") })
	if tools.IsMCPCompositionTool(leaf) {
		t.Fatal("MCP endpoint mistaken for built-in")
	}
	a := &agent{}
	ctx, cancel := context.WithCancel(compositionTestContext("s"))
	defer cancel()
	result := make(chan tools.CompositionChildOutcome, 1)
	go func() {
		result <- a.dispatchTool(ctx, ctx, "s", leaf, tools.ToolCall{Name: tools.MCPComposeToolName, Input: `{}`}, false)
	}()
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("same-named MCP bypassed watchdog cancellation")
	}
}

func TestSameNamedMCPRetainsLoopTrackingAndProgress(t *testing.T) {
	withFreshTaskRegistry(t)
	var calls atomic.Int32
	leaf := sameNameMCP(t, func(context.Context, string) tools.ToolResponse { calls.Add(1); return tools.NewTextResponse("same") })
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n <= 5 {
			return toolTurn(message.ToolCall{ID: "mcp", Name: tools.MCPComposeToolName, Input: `{}`, Finished: true})
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{leaf})
	result := a.processGeneration(compositionTestContext("s-name-loop"), "s-name-loop", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil || calls.Load() != DefaultRepeatThreshold {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
	var found bool
	for _, r := range toolResultsOf(t, a, "s-name-loop") {
		found = found || strings.Contains(r.Content, "Loop detected")
	}
	if !found {
		t.Fatal("same-named MCP skipped loop tracking")
	}
	// A successful same-named call must reset a preceding denial-only streak.
	p = &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		switch n {
		case 1, 3:
			return toolTurn(call("deny", n))
		case 2:
			return toolTurn(call(tools.MCPComposeToolName, n))
		default:
			return endTurn()
		}
	}}
	a = newLoopAgentWithTools(t, p, []tools.BaseTool{leaf, deniedTool{name: "deny", err: permission.DeniedByRule("deny", "x")}})
	result = a.processGeneration(compositionTestContext("s-name-progress"), "s-name-progress", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil || result.Message.FinishReason() != message.FinishReasonEndTurn || p.callCount() != 4 {
		t.Fatalf("same-named MCP failed to count progress: %+v turns=%d", result, p.callCount())
	}
}

type rewrittenOuterRegistry struct {
	*dispatchRegistry
	input string
}

func (r *rewrittenOuterRegistry) EvaluatePermission(_, name, input string) permission.Action {
	if name == tools.MCPComposeToolName {
		r.input = input
		return permission.ActionDeny
	}
	return permission.ActionAllow
}

func TestCompositionOuterPolicySeesHookMutatedPlan(t *testing.T) {
	dir := t.TempDir()
	updated := compositionPlan(2)
	hookJSON := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":` + updated + `}}`
	script := writeScript(t, dir, "rewrite.sh", "#!/bin/sh\ncat >/dev/null\necho '"+hookJSON+"'\n")
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {{Matcher: "mcp_compose", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}}}}), dir)
	policy := &rewrittenOuterRegistry{dispatchRegistry: &dispatchRegistry{action: permission.ActionAllow}}
	a := &agent{reg: policy, factory: &fakeFactoryWithHooks{reg: reg}}
	ctx := context.Background()
	result := a.dispatchTool(ctx, ctx, "s", tools.NewMCPComposeTool(), tools.ToolCall{Name: tools.MCPComposeToolName, Input: compositionPlan(1)}, false)
	if !result.RuleDenied || !strings.Contains(policy.input, `"n":2`) || strings.Contains(policy.input, `"n":1`) {
		t.Fatalf("permission saw wrong plan: %s result=%+v", policy.input, result)
	}
}
