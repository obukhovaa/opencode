package tools

import "context"

const MCPComposeToolName = "mcp_compose"

// MCPOutputMetadata describes the rendered result before hooks. It is never
// serialized into a conversation or used as an alternative data channel.
type MCPOutputMetadata struct {
	TextOnly bool
	Complete bool
}

type CompositionChildOutcome struct {
	Response    ToolResponse
	HookContext string
	PostBlocked bool
	Executed    bool
	RuleDenied  bool
	FailureCode string
	ControlErr  error
}

// CompositionOutcome carries control/accounting alongside the public envelope.
// The agent must preserve the envelope when a control error stops the turn.
type CompositionOutcome struct {
	HookContext string
	RuleDenials int
	Progressed  int
}

type CompositionRuntime struct {
	Check    func(context.Context, string) error
	Dispatch func(context.Context, ToolCall) CompositionChildOutcome
}

type compositionRuntimeKey struct{}

func WithCompositionRuntime(ctx context.Context, runtime CompositionRuntime) context.Context {
	return context.WithValue(ctx, compositionRuntimeKey{}, runtime)
}

func CompositionRuntimeFromContext(ctx context.Context) (CompositionRuntime, bool) {
	runtime, ok := ctx.Value(compositionRuntimeKey{}).(CompositionRuntime)
	return runtime, ok && runtime.Check != nil && runtime.Dispatch != nil
}

// PreHookInputValidator protects syntax that an outer hook could normalize away.
type PreHookInputValidator interface {
	ValidateInput(string) error
}

// IsMCPCompositionTool identifies the built-in, including a deferred wrapper.
// An MCP endpoint can legitimately have the same effective tool name.
func IsMCPCompositionTool(tool BaseTool) bool {
	if wrapped, ok := tool.(*DeferredWrapper); ok {
		tool = wrapped.Unwrap()
	}
	_, ok := tool.(*mcpComposeTool)
	return ok
}
