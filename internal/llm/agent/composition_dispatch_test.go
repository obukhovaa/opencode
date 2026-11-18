package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

type dispatchRegistry struct {
	agentregistry.Registry
	action   permission.Action
	evaluate func(string) permission.Action
	outer    permission.Action
}

func (r *dispatchRegistry) Get(string) (agentregistry.AgentInfo, bool) {
	return agentregistry.AgentInfo{ID: "coder", AllowTools: []string{tools.MCPComposeToolName, "srv_read"}}, true
}
func (r *dispatchRegistry) IsToolEnabled(string, string) bool { return true }
func (r *dispatchRegistry) EvaluatePermission(_, name string, input string) permission.Action {
	if name == tools.MCPComposeToolName {
		if r.outer != "" {
			return r.outer
		}
		return permission.ActionAllow
	}
	if r.evaluate != nil {
		return r.evaluate(input)
	}
	return r.action
}

type dispatchMCPRegistry struct {
	MCPRegistry
	run func(context.Context, string) tools.ToolResponse
}

func (r dispatchMCPRegistry) CallTool(ctx context.Context, _, _ string, input string) tools.ToolResponse {
	return r.run(ctx, input)
}

func compositionTestSetup(t *testing.T, action permission.Action, fn func(context.Context, string) tools.ToolResponse) (*dispatchRegistry, tools.BaseTool) {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{"srv": {Type: config.MCPSse, URL: "http://localhost/mcp"}})
	cfg := config.Get()
	old := cfg.MCPComposition
	cfg.MCPComposition = &config.MCPCompositionConfig{ReadTools: []string{"srv_read"}}
	t.Cleanup(func() { cfg.MCPComposition = old })
	registry := &dispatchRegistry{action: action}
	return registry, newMCPTool("srv", mcp.NewTool("read", mcp.WithReadOnlyHintAnnotation(true)), permission.NewPermissionService(), registry, dispatchMCPRegistry{run: fn})
}

func compositionPlan(n int) string {
	return fmt.Sprintf(`{"steps":[{"id":"read","tool":"srv_read","arguments":{"n":%d},"select":{"fields":{"id":"/id"}}}],"return":["read"]}`, n)
}
func compositionTestContext(session string) context.Context {
	ctx := context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("coder"))
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, session)
	return context.WithValue(ctx, tools.MessageIDContextKey, "m")
}
func compositionText(text string) tools.ToolResponse {
	r := tools.NewTextResponse(text)
	r.MCPOutput = &tools.MCPOutputMetadata{TextOnly: true, Complete: true}
	return r
}

func TestCompositionChildPolicyDenyWinsHookApproval(t *testing.T) {
	registry, leaf := compositionTestSetup(t, permission.ActionDeny, func(context.Context, string) tools.ToolResponse {
		t.Fatal("policy deny bypassed")
		return tools.NewEmptyResponse()
	})
	dir := t.TempDir()
	pre := writeScript(t, dir, "allow.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\"}}'\n")
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: pre}}}}}), dir)
	a := newLoopAgentWithTools(t, &scriptedProvider{}, []tools.BaseTool{leaf})
	a.reg = registry
	a.factory = &fakeFactoryWithHooks{reg: reg}
	out := a.dispatchCompositionChild(compositionTestContext("s"), "s", tools.ToolCall{Name: "srv_read", Input: `{"secret":"RAW_SENTINEL"}`}, newCallTracker(), &sync.Mutex{})
	if !out.RuleDenied || strings.Contains(out.Response.Content, "RAW_SENTINEL") {
		t.Fatalf("%+v", out)
	}
}

func TestCompositionDenialCountsChildrenWithoutWrapperProgress(t *testing.T) {
	withFreshTaskRegistry(t)
	registry, leaf := compositionTestSetup(t, permission.ActionDeny, func(context.Context, string) tools.ToolResponse {
		t.Fatal("denied child ran")
		return tools.NewEmptyResponse()
	})
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		return toolTurn(message.ToolCall{ID: fmt.Sprint(n), Name: tools.MCPComposeToolName, Input: compositionPlan(n), Finished: true})
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
	a.reg = registry
	result := a.processGeneration(compositionTestContext("s-compose-denied"), "s-compose-denied", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil || result.Message.FinishReason() != message.FinishReasonPermissionDenied || p.callCount() != maxConsecutiveRuleDeniedTurns {
		t.Fatalf("result=%+v turns=%d", result, p.callCount())
	}
}

func TestCompositionChildrenShareOrdinaryLoopTracker(t *testing.T) {
	var calls atomic.Int32
	registry, leaf := compositionTestSetup(t, permission.ActionAllow, func(context.Context, string) tools.ToolResponse { calls.Add(1); return compositionText(`{"id":1}`) })
	a := newLoopAgentWithTools(t, &scriptedProvider{}, []tools.BaseTool{leaf})
	a.reg = registry
	tracker := newCallTracker()
	tracker.threshold = 2
	tracker.Track("srv_read", `{"n":1}`, message.ToolResult{Type: message.ToolResultTypeText, Content: `{"id":1}`})
	out := a.dispatchCompositionChild(compositionTestContext("s"), "s", tools.ToolCall{Name: "srv_read", Input: `{"n":1}`}, tracker, &sync.Mutex{})
	if out.FailureCode != "loop_detected" || calls.Load() != 1 {
		t.Fatalf("out=%+v calls=%d", out, calls.Load())
	}
	if !tracker.Blocked("srv_read", `{"n":1}`) || strings.Contains(tracker.lastCall["srv_read"], `"n"`) {
		t.Fatalf("tracker does not share hashed observations: %+v", tracker)
	}
}

func TestCompositionPartialEnvelopeSurvivesControlError(t *testing.T) {
	withFreshTaskRegistry(t)
	for _, rejected := range []bool{true, false} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
				if n == 1 {
					return toolTurn(call(tools.MCPComposeToolName, n))
				}
				return endTurn()
			}}
			tool := dispatchProbe{name: tools.MCPComposeToolName, run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
				r := tools.NewTextErrorResponse(`{"complete":false,"results":{"first":{"id":1}}}`)
				r.Composition = &tools.CompositionOutcome{HookContext: "retained context", Progressed: 1}
				if rejected {
					return r, permission.ErrorPermissionDenied
				}
				return r, context.Canceled
			}}
			a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
			result := a.processGeneration(context.Background(), "s-partial", "read", 0, nil, RunOptions{NonInteractive: true})
			want := message.FinishReasonCanceled
			if rejected {
				want = message.FinishReasonPermissionDenied
			}
			if result.Message.FinishReason() != want || p.callCount() != 1 {
				t.Fatalf("finish=%s turns=%d err=%v", result.Message.FinishReason(), p.callCount(), result.Error)
			}
			responses := toolResultsOf(t, a, "s-partial")
			if len(responses) != 1 || !strings.Contains(responses[0].Content, `"first":{"id":1}`) || !strings.Contains(responses[0].Content, "retained context") {
				t.Fatalf("partial envelope lost: %+v", responses)
			}
		})
	}
}

func TestCompositionOriginalInvalidInputBeforeHooks(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/hook-ran"
	pre := writeScript(t, dir, "rewrite.sh", "#!/bin/sh\ncat >/dev/null\ntouch '"+marker+"'\necho '{}'\n")
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {{Matcher: tools.MCPComposeToolName, Hooks: []hooks.HookEntry{{Type: "command", Command: pre}}}}}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}
	input := strings.Replace(compositionPlan(1), `"n":1`, `"n":1,"n":2`, 1)
	ctx := context.Background()
	out := a.dispatchTool(ctx, ctx, "s", tools.NewMCPComposeTool(), tools.ToolCall{Name: tools.MCPComposeToolName, Input: input}, false)
	if out.Executed || out.FailureCode != "invalid_input" {
		t.Fatalf("invalid original dispatched: %+v", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("outer pre-hook normalized invalid original")
	}
}

type dispatchMessages struct {
	*memMessages
	partsMu sync.Mutex
	parts   []message.ContentPart
}

func (m *dispatchMessages) PublishPart(_, _ string, part message.ContentPart) {
	m.partsMu.Lock()
	defer m.partsMu.Unlock()
	m.parts = append(m.parts, part)
}

func TestCompositionDoesNotPublishHiddenBoundValues(t *testing.T) {
	withFreshTaskRegistry(t)
	var calls atomic.Int32
	registry, leaf := compositionTestSetup(t, permission.ActionAllow, func(_ context.Context, input string) tools.ToolResponse {
		if calls.Add(1) == 1 {
			return compositionText(`{"id":"RAW_SENTINEL"}`)
		}
		if input != `{"id":"RAW_SENTINEL"}` {
			t.Errorf("binding not applied: %s", input)
		}
		return tools.NewTextErrorResponse("server error echoed RAW_SENTINEL")
	})
	plan := `{"steps":[{"id":"first","tool":"srv_read","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"second","tool":"srv_read","arguments":{"id":null},"bindings":[{"target":"/id","step":"first","pointer":"/id"}],"select":{"fields":{"id":"/id"}}}],"return":["second"]}`
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(message.ToolCall{ID: "compose", Name: tools.MCPComposeToolName, Input: plan, Finished: true})
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
	a.reg = registry
	messages := &dispatchMessages{memMessages: newMemMessages()}
	a.messages = messages
	result := a.processGeneration(compositionTestContext("s-private"), "s-private", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
	stored, err := messages.List(context.Background(), "s-private")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored)
	if strings.Contains(string(encoded), "RAW_SENTINEL") {
		t.Fatalf("hidden value entered history: %s", encoded)
	}
	messages.partsMu.Lock()
	defer messages.partsMu.Unlock()
	encoded, _ = json.Marshal(messages.parts)
	if strings.Contains(string(encoded), "RAW_SENTINEL") {
		t.Fatalf("hidden value entered SSE: %s", encoded)
	}
	for _, part := range messages.parts {
		if response, ok := part.(message.ToolResult); ok && response.Name != "mcp_compose" {
			t.Fatalf("child result published: %+v", response)
		}
	}
}

func TestCompositionCancellationRetainsCompletedPreHookContext(t *testing.T) {
	dir := t.TempDir()
	pre := writeScript(t, dir, "pre.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"additionalContext\":\"completed pre-hook context\"}}'\n")
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: pre}}}}}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}
	child := dispatchProbe{name: "srv_read", run: func(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
		<-ctx.Done()
		return tools.NewEmptyResponse(), ctx.Err()
	}}
	ctx := tools.WithCompositionRuntime(context.Background(), tools.CompositionRuntime{
		Check: func(context.Context, string) error { return nil },
		Dispatch: func(ctx context.Context, call tools.ToolCall) tools.CompositionChildOutcome {
			return a.dispatchTool(ctx, ctx, "s", child, call, true)
		},
	})
	ctx = tools.WithCompositionLimit(ctx, 100*time.Millisecond)
	result, err := tools.NewMCPComposeTool().Run(ctx, tools.ToolCall{Name: tools.MCPComposeToolName, Input: compositionPlan(1)})
	if err != nil || result.Composition == nil || !strings.Contains(result.Composition.HookContext, "completed pre-hook context") || !strings.Contains(result.Content, "active_timeout") {
		t.Fatalf("completed context lost: %+v %v", result, err)
	}
}

func TestOrdinaryDispatchHookParitySequentialAndParallel(t *testing.T) {
	withFreshTaskRegistry(t)
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			dir := t.TempDir()
			pre := writeScript(t, dir, "pre.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"updatedInput\":{\"id\":9007199254740993},\"additionalContext\":\"pre context\"}}'\n")
			post := writeScript(t, dir, "post.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"updatedToolOutput\":\"filtered result\",\"additionalContext\":\"post context\"}}'\n")
			reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
				"PreToolUse":  {{Matcher: "*", Hooks: []hooks.HookEntry{{Type: "command", Command: pre}}}},
				"PostToolUse": {{Matcher: "*", Hooks: []hooks.HookEntry{{Type: "command", Command: post}}}},
			}), dir)
			var calls atomic.Int32
			var toolset []tools.BaseTool
			for _, name := range []string{"read_a", "read_b"} {
				toolset = append(toolset, &fakeTool{name: name, parallel: true, runFn: func(_ context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
					if call.Input != `{"id":9007199254740993}` {
						t.Errorf("mutated input lost: %s", call.Input)
					}
					calls.Add(1)
					return tools.NewTextResponse("raw result"), nil
				}})
			}
			p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
				if n == 1 {
					return toolTurn(call("read_a", 1), call("read_b", 2))
				}
				return endTurn()
			}}
			a := newLoopAgentWithTools(t, p, toolset)
			a.allowParallelism = parallel
			a.factory = &fakeFactoryWithHooks{reg: reg}
			result := a.processGeneration(context.Background(), "s-hooks", "read", 0, nil, RunOptions{NonInteractive: true})
			if result.Error != nil || calls.Load() != 2 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
			responses := toolResultsOf(t, a, "s-hooks")
			if len(responses) != 2 {
				t.Fatalf("results=%+v", responses)
			}
			for _, response := range responses {
				if response.IsError || response.Content != "filtered result\n\n[hook context: pre context\npost context]" {
					t.Fatalf("hook parity broken: %+v", response)
				}
			}
		})
	}
}

func TestCompositionOuterApprovalCannotBypassChildRejection(t *testing.T) {
	withFreshTaskRegistry(t)
	var calls atomic.Int32
	registry, leaf := compositionTestSetup(t, permission.ActionAllow, func(context.Context, string) tools.ToolResponse { calls.Add(1); return compositionText(`{"id":1}`) })
	registry.evaluate = func(input string) permission.Action {
		if input == `{"n":2}` {
			return permission.ActionAsk
		}
		return permission.ActionAllow
	}
	dir := t.TempDir()
	approve := writeScript(t, dir, "outer.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\"}}'\n")
	childHook := writeScript(t, dir, "child.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"additionalContext\":\"child hook context\"}}'\n")
	hooksRegistry := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PreToolUse": {
		{Matcher: "mcp_compose", Hooks: []hooks.HookEntry{{Type: "command", Command: approve}}},
		{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: childHook}}},
	}}), dir)
	plan := `{"steps":[{"id":"first","tool":"srv_read","arguments":{"n":1},"select":{"fields":{"id":"/id"}}},{"id":"second","tool":"srv_read","arguments":{"n":2},"select":{"fields":{"id":"/id"}}}],"return":["first","second"]}`
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(message.ToolCall{ID: "c", Name: tools.MCPComposeToolName, Input: plan, Finished: true})
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
	a.reg = registry
	a.factory = &fakeFactoryWithHooks{reg: hooksRegistry}
	ctx, cancel := context.WithTimeout(compositionTestContext("s-reject"), 3*time.Second)
	defer cancel()
	permissions := leaf.(*mcpTool).permissions
	events := permissions.Subscribe(ctx)
	rejected := make(chan struct{})
	go func() {
		defer close(rejected)
		select {
		case event := <-events:
			permissions.Deny(event.Payload)
		case <-ctx.Done():
		}
	}()
	result := a.processGeneration(ctx, "s-reject", "read", 0, nil, RunOptions{NonInteractive: true})
	if result.Error != nil || result.Message.FinishReason() != message.FinishReasonPermissionDenied || calls.Load() != 1 || p.callCount() != 1 {
		t.Fatalf("result=%+v calls=%d turns=%d", result, calls.Load(), p.callCount())
	}
	<-rejected
	responses := toolResultsOf(t, a, "s-reject")
	if len(responses) != 1 || !strings.Contains(responses[0].Content, `"first":{"id":1}`) || !strings.Contains(responses[0].Content, "child hook context") || !strings.Contains(responses[0].Content, `"code":"permission_denied"`) {
		t.Fatalf("partial result lost: %+v", responses)
	}
}
