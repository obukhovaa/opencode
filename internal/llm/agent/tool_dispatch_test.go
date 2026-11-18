package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
)

type dispatchProbe struct {
	name string
	run  func(context.Context, tools.ToolCall) (tools.ToolResponse, error)
}

func (p dispatchProbe) Info() tools.ToolInfo                                   { return tools.ToolInfo{Name: p.name} }
func (p dispatchProbe) IsBaseline() bool                                       { return false }
func (p dispatchProbe) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (p dispatchProbe) Run(ctx context.Context, c tools.ToolCall) (tools.ToolResponse, error) {
	return p.run(ctx, c)
}

type numericMCPClient struct {
	fakeMCPClient
	input string
}

func (c *numericMCPClient) CallTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(req.Params.Arguments)
	c.input = string(b)
	return mcp.NewToolResultText(`{"ok":true}`), err
}

func TestDispatchNumericHookMCPWire(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "noop", true: "rewrite"}[rewrite], func(t *testing.T) {
			dir := t.TempDir()
			preCapture := dir + "/pre-input.json"
			body := "#!/bin/sh\ncat > '" + preCapture + "'\n"
			if rewrite {
				body += `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"id":9007199254740993}}}'` + "\n"
			}
			pre := writeScript(t, dir, "pre.sh", body)
			capture := dir + "/post-input.json"
			post := writeScript(t, dir, "post.sh", "#!/bin/sh\ncat > '"+capture+"'\n")
			reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
				"PreToolUse":  {{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: pre}}}},
				"PostToolUse": {{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: post}}}},
			}), dir)
			a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}
			client := &numericMCPClient{}
			tool := dispatchProbe{name: "srv_read", run: func(ctx context.Context, c tools.ToolCall) (tools.ToolResponse, error) {
				return callMCPTool(ctx, client, "read", c.Input, time.Second, -1)
			}}
			out := a.dispatchTool(context.Background(), context.Background(), "s", tool, tools.ToolCall{Name: tool.name, Input: `{"id":9007199254740993}`}, true)
			if out.Response.IsError || client.input != `{"id":9007199254740993}` {
				t.Fatalf("out=%+v wire=%s", out, client.input)
			}
			rawPre, err := os.ReadFile(preCapture)
			if err != nil || !strings.Contains(string(rawPre), `"id":9007199254740993`) {
				t.Fatalf("pre-hook numeric value changed: %s %v", rawPre, err)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			if !strings.Contains(got, `"id":9007199254740993`) {
				t.Fatalf("post-hook numeric value changed: %s", got)
			}
		})
	}
}

func TestDispatchJSONPostBlockAndContext(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "post.sh", "#!/bin/sh\ncat > /dev/null\necho '{\"secret\":true}' >&2\nexit 2\n")
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{"PostToolUse": {{Matcher: "srv_read", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}}}}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}
	tool := dispatchProbe{name: "srv_read", run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		return tools.NewTextResponse(`{"data":1}`), nil
	}}
	out := a.dispatchTool(context.Background(), context.Background(), "s", tool, tools.ToolCall{Name: tool.name, Input: `{}`}, true)
	if !out.PostBlocked || out.Response.Content != `{"secret":true}` || out.FailureCode != "hook_blocked" {
		t.Fatalf("%+v", out)
	}
}

func TestDispatchClearsInheritedApproval(t *testing.T) {
	a := &agent{}
	tool := dispatchProbe{name: "srv_read", run: func(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
		if allowed, _ := ctx.Value(permission.HookAllowKey).(bool); allowed {
			t.Error("inherited approval reached child")
		}
		return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
	}}
	ctx := context.WithValue(context.Background(), permission.HookAllowKey, true)
	out := a.dispatchTool(ctx, ctx, "s", tool, tools.ToolCall{Name: tool.name, Input: `{}`}, true)
	if !errors.Is(out.ControlErr, permission.ErrorPermissionDenied) {
		t.Fatalf("%+v", out)
	}
}

func TestDispatchHiddenErrorsDoNotEchoInput(t *testing.T) {
	for _, err := range []error{errors.New("RAW_SENTINEL"), permission.DeniedByRule("srv_read", "RAW_SENTINEL")} {
		a := &agent{}
		tool := dispatchProbe{name: "srv_read", run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
			return tools.NewEmptyResponse(), err
		}}
		out := a.dispatchTool(context.Background(), context.Background(), "s", tool, tools.ToolCall{Name: tool.name, Input: `{"id":"RAW_SENTINEL"}`}, true)
		if strings.Contains(out.Response.Content, "RAW_SENTINEL") || out.ControlErr != nil {
			t.Fatalf("unsafe hidden failure: %+v", out)
		}
	}
}

func TestMCPResultEligibilityAndErrors(t *testing.T) {
	cases := []struct {
		name               string
		result             *mcp.CallToolResult
		cap                int
		textOnly, complete bool
	}{
		{"text", mcp.NewToolResultText(`{"id":1}`), -1, true, true},
		{"error", mcp.NewToolResultError(`{"id":1}`), -1, true, true},
		{"structured-only", &mcp.CallToolResult{}, -1, false, true},
		{"media", &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"id":1}`}, mcp.ImageContent{Type: "image", Data: "YWJj", MIMEType: "image/png"}}}, -1, false, true},
		{"server-cap", mcp.NewToolResultText(strings.Repeat("x", 4096)), 100, true, false},
		{"global-cap", mcp.NewToolResultText(strings.Repeat("x", 2*1024*1024)), -1, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := callMCPTool(context.Background(), &fakeMCPClient{result: tc.result}, "read", `{}`, time.Second, tc.cap)
			if err != nil || resp.IsError != tc.result.IsError || resp.MCPOutput == nil || resp.MCPOutput.TextOnly != tc.textOnly || resp.MCPOutput.Complete != tc.complete {
				t.Fatalf("response=%+v meta=%+v err=%v", resp, resp.MCPOutput, err)
			}
		})
	}
}

func TestDispatchCancellationAfterMCPErrorResponse(t *testing.T) {
	zero := 0
	a := &agent{toolCallTimeoutSeconds: &zero}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := dispatchProbe{name: "srv_read", run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		cancel()
		return tools.NewTextErrorResponse("RAW_SENTINEL"), nil
	}}
	out := a.dispatchTool(ctx, ctx, "s", tool, tools.ToolCall{Name: tool.name, Input: `{}`}, true)
	if !errors.Is(out.ControlErr, context.Canceled) || strings.Contains(out.Response.Content, "RAW_SENTINEL") {
		t.Fatalf("%+v", out)
	}
}

func TestDispatchOwnershipAdmission(t *testing.T) {
	a := &agent{}
	ctx := tools.WithFlowOwnershipGuard(context.Background(), func(context.Context) error { return errors.New("lost") })
	tool := dispatchProbe{name: "srv_read", run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		t.Fatal("executed after ownership lost")
		return tools.NewEmptyResponse(), nil
	}}
	out := a.dispatchTool(ctx, ctx, "s", tool, tools.ToolCall{Name: tool.name, Input: `{}`}, true)
	if out.FailureCode != "ownership_lost" || out.ControlErr == nil || out.Executed {
		t.Fatalf("%+v", out)
	}
}

func TestHiddenDispatchDiagnosticsExcludeRawPayloads(t *testing.T) {
	recorder := setLangfuseClient(t, true)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	a := &agent{}
	for _, failure := range []string{"transport", "server", "policy"} {
		tool := dispatchProbe{name: "srv_read", run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
			switch failure {
			case "transport":
				return tools.NewEmptyResponse(), fmt.Errorf("transport RAW_SENTINEL")
			case "policy":
				return tools.NewEmptyResponse(), permission.DeniedByRule("srv_read", "RAW_SENTINEL")
			default:
				return tools.NewTextErrorResponse(`{"raw":"RAW_SENTINEL"}`), nil
			}
		}}
		a.dispatchTool(context.Background(), context.Background(), "s", tool, tools.ToolCall{Name: tool.name, Input: `{"bound":"RAW_SENTINEL"}`}, true)
	}
	if strings.Contains(logs.String(), "RAW_SENTINEL") {
		t.Fatalf("raw child data logged: %s", logs.String())
	}
	count := 0
	for _, span := range recorder.Ended() {
		if span.Name() != "srv_read" {
			continue
		}
		count++
		diagnostic := fmt.Sprintf("%v %v %v", span.Attributes(), span.Status(), span.Events())
		if strings.Contains(diagnostic, "RAW_SENTINEL") {
			t.Fatalf("raw child data traced: %s", diagnostic)
		}
	}
	if count != 3 {
		t.Fatalf("traced %d calls, want 3", count)
	}
}

func TestMCPArgumentDecoderRejectsTrailingValues(t *testing.T) {
	client := &fakeMCPClient{result: mcp.NewToolResultText(`{}`)}
	response, err := callMCPTool(context.Background(), client, "read", `{} {}`, time.Second, -1)
	if err != nil || !response.IsError || client.callCalls.Load() != 0 {
		t.Fatalf("accepted trailing arguments: %+v %v", response, err)
	}
}
