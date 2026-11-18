package agent

import (
	"context"
	"sync"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func (a *agent) dispatchCompositionChild(ctx context.Context, sessionID string, call tools.ToolCall, tracker *callTracker, accounting *sync.Mutex) tools.CompositionChildOutcome {
	if err := ctx.Err(); err != nil {
		return tools.CompositionChildOutcome{FailureCode: "cancelled", ControlErr: err}
	}
	if err := tools.CheckFlowOwnership(ctx); err != nil {
		return tools.CompositionChildOutcome{FailureCode: "ownership_lost", ControlErr: err}
	}
	tool, err := a.compositionChildTool(ctx, call.Name)
	if err != nil {
		return tools.CompositionChildOutcome{FailureCode: "ineligible_tool"}
	}
	accounting.Lock()
	blocked := tracker.Blocked(call.Name, call.Input)
	accounting.Unlock()
	if blocked {
		return tools.CompositionChildOutcome{FailureCode: "loop_detected"}
	}
	out := a.dispatchTool(ctx, ctx, sessionID, tool, call, true)
	if out.Executed && ctx.Err() == nil {
		accounting.Lock()
		repeated := tracker.Track(call.Name, call.Input, message.ToolResult{Type: message.ToolResultType(out.Response.Type),
			Content: appendHookContext(out.Response.Content, out.HookContext), IsError: out.Response.IsError})
		accounting.Unlock()
		if repeated {
			out.FailureCode = "loop_detected"
			out.Response = tools.NewTextErrorResponse("Repeated child call detected")
		}
	}
	return out
}
