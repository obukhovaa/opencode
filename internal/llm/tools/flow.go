package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

const FlowToolName = "flow"

type FlowRequest struct {
	Operation  string `json:"operation"`
	Definition string `json:"definition"`
	Complexity string `json:"complexity"`
}
type FlowSubmitter func(context.Context, string, FlowRequest) error
type flowSubmitKey struct{}

func WithFlowSubmitter(ctx context.Context, submit FlowSubmitter) context.Context {
	return context.WithValue(ctx, flowSubmitKey{}, submit)
}

type flowTool struct{ fallback FlowSubmitter }

func NewFlowTool(submit ...FlowSubmitter) BaseTool {
	t := &flowTool{}
	if len(submit) > 0 {
		t.fallback = submit[0]
	}
	return t
}
func (*flowTool) Info() ToolInfo {
	return ToolInfo{Name: FlowToolName, Description: "Submit a bounded dynamic workflow definition as YAML. Use operation create during /flow planning, or update to replace only unexecuted downstream steps. Finish your turn after submitting; execution starts after the turn releases its session. Never invoke recursively in a flow step.", Parameters: map[string]any{
		"operation":  map[string]any{"type": "string", "enum": []string{"create", "update"}},
		"definition": map[string]any{"type": "string", "description": "Complete flow YAML with name and flow.steps. Prefer OpenSpec propose, review, implement, review, test, archive; size the plan to the task."},
		"complexity": map[string]any{"type": "string", "enum": []string{"XS", "S", "M", "L", "XL", "XXL"}},
	}, Required: []string{"operation", "definition", "complexity"}}
}
func (t *flowTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var req FlowRequest
	if err := json.Unmarshal([]byte(call.Input), &req); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	submit, _ := ctx.Value(flowSubmitKey{}).(FlowSubmitter)
	if submit == nil {
		submit = t.fallback
	}
	if submit == nil {
		return NewTextErrorResponse("Start dynamic planning with /flow before submitting a definition."), nil
	}
	id, _ := GetContextValues(ctx)
	if err := submit(ctx, id, req); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Flow rejected: %v", err)), nil
	}
	return NewTextResponse("Flow saved. Finish this turn to begin execution."), nil
}
func (*flowTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }
func (*flowTool) IsBaseline() bool                           { return true }

// FlowTaskLimiter bounds concurrently running task subagents throughout a flow.
// Acquisition is nonblocking: waiting while every parent holds a slot can deadlock.
type FlowTaskLimiter struct{ slots chan struct{} }
type flowTaskLimitKey struct{}

func WithFlowTaskLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, flowTaskLimitKey{}, &FlowTaskLimiter{make(chan struct{}, max(1, n))})
}
func AcquireFlowTask(ctx context.Context) (func(), bool) {
	l, _ := ctx.Value(flowTaskLimitKey{}).(*FlowTaskLimiter)
	if l == nil {
		return func() {}, true
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, true
	default:
		return nil, false
	}
}

type flowTaskTurnsKey struct{}

func WithFlowTaskTurns(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, flowTaskTurnsKey{}, n)
}
func FlowTaskTurns(ctx context.Context) int { n, _ := ctx.Value(flowTaskTurnsKey{}).(int); return n }
func InheritFlowLimits(dst, src context.Context) context.Context {
	if v := src.Value(flowTaskLimitKey{}); v != nil {
		dst = context.WithValue(dst, flowTaskLimitKey{}, v)
	}
	return WithFlowTaskTurns(dst, FlowTaskTurns(src))
}

type flowAgentScopeKey struct{}

func WithFlowAgentScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, flowAgentScopeKey{}, scope)
}
func FlowAgentScope(ctx context.Context) string {
	s, _ := ctx.Value(flowAgentScopeKey{}).(string)
	return s
}
