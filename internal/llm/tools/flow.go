package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/question"
)

const FlowToolName = "flow"

type FlowRequest struct {
	Operation    string         `json:"operation"`
	Definition   string         `json:"definition"`
	Complexity   string         `json:"complexity,omitempty"`
	Goal         string         `json:"goal,omitempty"`
	FlowID       string         `json:"flowId,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
	ArgsJSON     string         `json:"argsJson,omitempty"`
	Action       string         `json:"action,omitempty"`
	StepID       string         `json:"stepId,omitempty"`
	InvocationID string         `json:"-"`
}

type FlowListing struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
}
type FlowResult struct {
	Operation      string        `json:"operation"`
	RunID          string        `json:"runId,omitempty"`
	ConversationID string        `json:"conversationId,omitempty"`
	Status         string        `json:"status,omitempty"`
	FlowID         string        `json:"flowId,omitempty"`
	Kind           string        `json:"kind,omitempty"`
	Flows          []FlowListing `json:"flows,omitempty"`
	Snapshot       any           `json:"snapshot,omitempty"`
}
type flowResultKey struct{}

func SetFlowResult(ctx context.Context, result FlowResult) {
	if target, ok := ctx.Value(flowResultKey{}).(*FlowResult); ok {
		*target = result
	}
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
	return ToolInfo{Name: FlowToolName, Description: "Manage a top-level workflow in this ordinary primary-agent conversation. plan builds a bounded workflow from goal using bundled guidance; run executes an authorized existing YAML flow by flowId and args without changing its source. create submits generated YAML; update changes only unentered generated steps. list shows available flows; status inspects the current run; control accepts cancel/resume/skip/terminate. After plan/create/run succeeds, finish your turn so execution can start. Flow steps and subagents cannot start another flow. Named runs cannot be updated.", Parameters: map[string]any{
		"operation":  map[string]any{"type": "string", "enum": []string{"plan", "create", "update", "run", "list", "status", "control"}},
		"goal":       map[string]any{"type": "string", "description": "User goal for plan."},
		"flowId":     map[string]any{"type": "string", "description": "Discovered flow ID for run; use list to inspect available argument schemas."},
		"argsJson":   map[string]any{"type": "string", "description": "JSON object of arguments validated against the existing flow schema; e.g. {\"goal\":\"review\"}."},
		"definition": map[string]any{"type": "string", "description": "Minimal self-contained YAML for create/update: flow contains steps; optional name/description belong at the document root, not inside flow. Omit unnecessary descriptions and comments."},
		"complexity": map[string]any{"type": "string", "enum": []string{"XS", "S", "M", "L", "XL", "XXL"}},
		"action":     map[string]any{"type": "string", "enum": []string{"cancel", "resume", "skip", "terminate"}},
		"stepId":     map[string]any{"type": "string", "description": "Selected step for cancel/skip."},
	}, Required: []string{"operation"}}
}
func (t *flowTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var req FlowRequest
	if err := json.Unmarshal([]byte(call.Input), &req); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if err := NormalizeFlowRequest(&req); err != nil {
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
	req.InvocationID = call.ID
	result := FlowResult{Operation: req.Operation, ConversationID: id, Status: "accepted"}
	ctx = context.WithValue(ctx, flowResultKey{}, &result)
	if err := submit(ctx, id, req); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Flow rejected: %v", err)), nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	return NewTextResponse(string(data)), nil
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
	dst = db.InheritWriteGuard(dst, src)
	dst = question.InheritFlowRunID(dst, src)
	if handler := FlowInboxHandler(src); handler != nil {
		dst = WithFlowInboxHandler(dst, handler)
	}
	if v := src.Value(flowTaskLimitKey{}); v != nil {
		dst = context.WithValue(dst, flowTaskLimitKey{}, v)
	}
	return WithFlowTaskTurns(inheritFlowOwnership(dst, src), FlowTaskTurns(src))
}

type flowAgentScopeKey struct{}

func WithFlowAgentScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, flowAgentScopeKey{}, scope)
}
func FlowAgentScope(ctx context.Context) string {
	s, _ := ctx.Value(flowAgentScopeKey{}).(string)
	return s
}

type sessionFlowDeadlineKey struct{}

func WithSessionFlowDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, sessionFlowDeadlineKey{}, deadline)
}
func SessionFlowDeadline(ctx context.Context) time.Time {
	v, _ := ctx.Value(sessionFlowDeadlineKey{}).(time.Time)
	return v
}

type flowAuthorizedPeersKey struct{}

func WithFlowAuthorizedPeers(ctx context.Context, peers []Peer) context.Context {
	return context.WithValue(ctx, flowAuthorizedPeersKey{}, append([]Peer(nil), peers...))
}
func FlowAuthorizedPeers(ctx context.Context) []Peer {
	peers, _ := ctx.Value(flowAuthorizedPeersKey{}).([]Peer)
	return append([]Peer(nil), peers...)
}

// NormalizeFlowRequest keeps API args objects compatible while the model-facing
// tool uses argsJson: Gemini rejects arbitrary empty-object tool declarations.
func NormalizeFlowRequest(req *FlowRequest) error {
	if req.ArgsJSON == "" {
		return nil
	}
	if req.Args != nil {
		return fmt.Errorf("provide args or argsJson, not both")
	}
	if err := json.Unmarshal([]byte(req.ArgsJSON), &req.Args); err != nil {
		return fmt.Errorf("argsJson must be a JSON object: %w", err)
	}
	if req.Args == nil {
		return fmt.Errorf("argsJson must be a JSON object")
	}
	req.ArgsJSON = ""
	return nil
}

type flowInboxHandlerKey struct{}

// The handler is opaque here to avoid the message -> tools dependency cycle;
// the agent layer owns its typed []message.Message callback signature.
func WithFlowInboxHandler(ctx context.Context, handler any) context.Context {
	return context.WithValue(ctx, flowInboxHandlerKey{}, handler)
}
func FlowInboxHandler(ctx context.Context) any { return ctx.Value(flowInboxHandlerKey{}) }
