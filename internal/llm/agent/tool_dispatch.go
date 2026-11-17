package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

type compositionChildKey struct{}

// dispatchTool owns the single-call hook, permission and watchdog boundary.
// Publication and run-wide accounting remain with the caller.
func (a *agent) dispatchTool(ctx, hookCtx context.Context, sessionID string, tool tools.BaseTool, call tools.ToolCall, hidden bool) (out tools.CompositionChildOutcome) {
	start := time.Now()
	originalInput := call.Input
	if hidden {
		ctx = context.WithValue(ctx, permission.HookAllowKey, false)
		ctx = context.WithValue(ctx, compositionChildKey{}, true)
		hookCtx = ctx
	}
	var span *langfuse.Span
	if client := langfuse.Get(); client != nil && client.Enabled() {
		var input any
		if !hidden && shouldLogToolInput(call.Name) {
			input = call.Input
		}
		span = client.ToolStart(ctx, langfuse.ToolParams{Name: call.Name, Input: input})
		defer func() {
			if hidden {
				status := out.FailureCode
				if status == "" {
					status = "ok"
				}
				span.SetOutput(map[string]any{"status": status, "inputBytes": len(call.Input), "outputBytes": len(out.Response.Content), "durationMs": time.Since(start).Milliseconds()})
			}
			if out.Response.IsError || out.ControlErr != nil || (hidden && out.FailureCode != "") {
				if hidden {
					span.SetError(fmt.Errorf("%s", out.FailureCode))
				} else {
					span.SetError(fmt.Errorf("%s", out.Response.Content))
				}
			} else if !hidden && shouldLogToolOutput(call.Name) {
				span.SetOutput(out.Response.Content)
			}
			span.End()
		}()
	}
	defer func() {
		if hidden {
			logging.Debug("Composition child completed", "tool", call.Name, "status", out.FailureCode, "gauge", time.Since(start).Milliseconds(), "inputBytes", len(call.Input), "outputBytes", len(out.Response.Content))
		} else {
			logging.Debug("Tool call completed", "tool", call.Name, "ID", call.ID, "input", originalInput, "successful", !out.Response.IsError, "gauge", time.Since(start).Milliseconds())
		}
	}()
	if hidden {
		if err := ctx.Err(); err != nil {
			out.ControlErr = err
			out.FailureCode = "cancelled"
			out.Response = dispatchError("Tool execution canceled")
			return
		}
		if err := tools.CheckFlowOwnership(ctx); err != nil {
			out.ControlErr = err
			out.FailureCode = "ownership_lost"
			out.Response = dispatchError("Flow ownership lost")
			return
		}
	}
	inner := tool
	if wrapped, ok := inner.(*tools.DeferredWrapper); ok {
		inner = wrapped.Unwrap()
	}
	if validator, ok := inner.(tools.PreHookInputValidator); ok {
		if err := validator.ValidateInput(call.Input); err != nil {
			out.Response = dispatchError("Invalid composition plan")
			out.FailureCode = "invalid_input"
			return
		}
	}
	mutated, pre := a.firePreTool(hookCtx, sessionID, call.Name, call.Input)
	out.HookContext = pre.decision.AdditionalContext
	if hidden {
		tools.PublishCompositionHookContext(ctx, out.HookContext)
	}
	if pre.decision.Block {
		reason := pre.decision.BlockReason
		if reason == "" {
			reason = "Tool call blocked by PreToolUse hook"
		}
		out.Response = dispatchError(reason)
		if !hidden {
			logging.Info("Tool call blocked by hook", "tool", call.Name, "ID", call.ID, "reason", reason)
		}
		if hidden {
			out.HookContext = joinHookContext(out.HookContext, reason)
			tools.PublishCompositionHookContext(ctx, out.HookContext)
		}
		out.FailureCode = "hook_blocked"
		return
	}
	if pre.decision.ExplicitAllow {
		ctx = context.WithValue(ctx, permission.HookAllowKey, true)
	}
	call.Input = mutated
	out.Executed = true
	var response tools.ToolResponse
	var err error
	limit := toolCallLimit(a.agentID, a.toolCallTimeoutSeconds, tool)
	if tools.IsMCPCompositionTool(tool) {
		err = a.checkCompositionPermission(ctx, sessionID, call)
		if err == nil {
			response, err = tool.Run(tools.WithCompositionLimit(ctx, limit), call)
		}
	} else {
		response, err = runToolBounded(ctx, limit, tool, call)
	}
	out.Response = response
	if response.Composition != nil {
		out.HookContext = joinHookContext(out.HookContext, response.Composition.HookContext)
	}
	// MCP transport failures are ToolResponses; the parent cancellation must
	// still stop the plan rather than becoming a normal failed read.
	if ctx.Err() != nil && response.Composition == nil {
		err = ctx.Err()
	}
	if err != nil {
		if hidden && ctx.Err() == nil {
			if ownershipErr := tools.CheckFlowOwnership(ctx); ownershipErr != nil {
				out.ControlErr = ownershipErr
				out.FailureCode = "ownership_lost"
				out.Response = dispatchError("Flow ownership lost")
				return
			}
		}
		if rd, ok := permission.AsRuleDenied(err); ok {
			out.RuleDenied = true
			if !hidden {
				logging.Warn("Tool call denied by permission rule", "tool", call.Name, "ID", call.ID, "input", rd.Input, "gauge", time.Since(start).Milliseconds())
			}
			out.FailureCode = "rule_denied"
			if hidden {
				out.Response = dispatchError("Permission denied by a configured rule")
			} else {
				out.Response = dispatchError(ruleDeniedMessage(rd))
			}
			return
		}
		if errors.Is(err, permission.ErrorPermissionDenied) {
			out.Executed = false
			if !hidden {
				logging.Warn("Tool call denied", "tool", call.Name, "ID", call.ID, "input", originalInput, "gauge", time.Since(start).Milliseconds())
			}
			out.ControlErr = err
			out.FailureCode = "permission_denied"
			if response.Composition == nil {
				out.Response = dispatchError("Permission denied")
			}
			return
		}
		if hidden || response.Composition != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || response.Composition != nil {
				out.ControlErr = err
			}
			out.FailureCode = "child_failed"
			if response.Composition == nil {
				out.Response = dispatchError("Tool execution failed")
			}
		} else {
			logging.Error("Tool call failed", "tool", call.Name, "ID", call.ID, "input", originalInput, "error", err.Error(), "gauge", time.Since(start).Milliseconds())
			out.Response = dispatchError(fmt.Sprintf("Tool returned error: %s", err))
		}
		return
	}
	if response.IsError {
		out.FailureCode = "child_failed"
		return
	}
	var post string
	out.Response.Content, post, out.PostBlocked = a.firePostToolDetailed(hookCtx, sessionID, call.Name, mutated, response.Content)
	out.HookContext = joinHookContext(out.HookContext, post)
	if hidden {
		tools.PublishCompositionHookContext(ctx, out.HookContext)
	}
	if out.PostBlocked {
		if hidden {
			out.HookContext = joinHookContext(out.HookContext, out.Response.Content)
			tools.PublishCompositionHookContext(ctx, out.HookContext)
		}
		out.FailureCode = "hook_blocked"
	}
	return
}

func dispatchResult(call message.ToolCall, out tools.CompositionChildOutcome) message.ToolResult {
	return message.ToolResult{Type: message.ToolResultType(out.Response.Type), Name: call.Name, ToolCallID: call.ID,
		Content: appendHookContext(out.Response.Content, out.HookContext), Metadata: out.Response.Metadata, IsError: out.Response.IsError}
}

func decodeToolJSON(input string, target any) error {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func dispatchError(content string) tools.ToolResponse {
	return tools.ToolResponse{Content: content, IsError: true}
}

func (a *agent) checkCompositionPermission(ctx context.Context, sessionID string, call tools.ToolCall) error {
	if a.reg == nil {
		return fmt.Errorf("composition permission context is unavailable")
	}
	switch a.reg.EvaluatePermission(string(a.agentID), tools.MCPComposeToolName, call.Input) {
	case permission.ActionAllow:
		return nil
	case permission.ActionDeny:
		return permission.DeniedByRule(tools.MCPComposeToolName, call.Input)
	default:
		if a.permissions != nil && a.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID: sessionID, Path: config.WorkingDirectory(), ToolName: tools.MCPComposeToolName,
			Action: "execute", Description: "execute mcp_compose with the following parameters: " + call.Input, Params: call.Input,
		}) {
			return nil
		}
		return permission.ErrorPermissionDenied
	}
}
