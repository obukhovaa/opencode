// Package hooks implements a Claude-Code-compatible hook runtime: external
// processes are invoked at agent-lifecycle moments (currently PreToolUse and
// PostToolUse), receive event JSON on stdin, and return decisions on stdout.
//
// The package is intentionally subprocess-only — there is no embedded JS
// engine, no plugin manifest format, no language-specific authoring SDK. A
// hook is any POSIX executable that speaks the JSON contract documented in
// openspec/specs/hook-runtime/spec.md and at code.claude.com/docs/en/hooks.
package hooks

// Canonical event names. Strings match Claude Code 1:1 so plugin authors
// can paste their settings.json blocks verbatim.
const (
	EventPreToolUse  = "PreToolUse"
	EventPostToolUse = "PostToolUse"
)

// PreToolUseInput is the JSON document written to a PreToolUse hook's stdin.
// Field names are snake_case per Claude Code's documented schema.
type PreToolUseInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
}

// PostToolUseInput is the JSON document written to a PostToolUse hook's stdin.
type PostToolUseInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	ToolOutput    string         `json:"tool_output"`
}

// HookOutput is the parsed JSON document a hook writes to stdout on exit 0.
// Field names are camelCase per Claude Code's documented schema. All fields
// are optional — a hook that wants no decision returns `{}` (or anything that
// fails to parse and is logged as a non-blocking error).
type HookOutput struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput is the per-event decision payload. Different event types
// honor different fields — PreToolUse uses PermissionDecision + UpdatedInput,
// PostToolUse uses UpdatedToolOutput. AdditionalContext applies to both.
//
// UpdatedToolOutput is a `*string` (not `string`) so callers can distinguish
// "field absent" (nil) from "explicit empty string" (&""). A hook that wants
// to fully suppress a noisy tool output writes `{"updatedToolOutput": ""}`
// — UpdatedInput uses a map for the same reason (nil vs empty map).
type HookSpecificOutput struct {
	HookEventName            string         `json:"hookEventName,omitempty"`
	PermissionDecision       string         `json:"permissionDecision,omitempty"` // "allow" | "deny" | "ask"
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
	UpdatedToolOutput        *string        `json:"updatedToolOutput,omitempty"`
	AdditionalContext        string         `json:"additionalContext,omitempty"`
}

// PreToolDecision is the runtime-side aggregate of PreToolUse hook outputs.
// The agent loop reads it AFTER all matching hooks have run, applying the
// precedence rules documented in design.md::D6: deny-wins, updatedInput
// chains in declaration order, additionalContext accumulates.
type PreToolDecision struct {
	// Block, when true, means the tool MUST NOT execute. BlockReason is the
	// text surfaced to the agent in place of the tool's output. Set when
	// any hook returned permissionDecision="deny" or exited with code 2.
	Block       bool
	BlockReason string

	// ExplicitAllow signals at least one hook returned permissionDecision="allow"
	// without any later deny. When true, the existing permission flow is
	// skipped (the hook is the authoritative decision per D8). When false
	// and Block is also false, the agent falls through to the normal
	// permission gate.
	ExplicitAllow bool

	// UpdatedInput, when non-nil, replaces the tool's input map before
	// execution. The final value is the last hook's updatedInput after
	// the chain-through (each hook receives the prior hook's updatedInput
	// as its own tool_input).
	UpdatedInput map[string]any

	// AdditionalContext is a `\n`-joined concatenation of every hook's
	// additionalContext output. Empty when no hook contributed any.
	AdditionalContext string
}

// PostToolDecision is the runtime-side aggregate of PostToolUse hook outputs.
type PostToolDecision struct {
	// UpdatedOutput, when non-nil, replaces the tool's result content in
	// the agent's conversation history. nil means leave the original
	// output intact.
	UpdatedOutput *string

	// BlockReason is set when a hook exits with code 2. The agent replaces
	// the tool output with this text (per spec, "block" for PostToolUse
	// means surface the stderr as the result content).
	BlockReason string

	// AdditionalContext accumulated across hooks (same rule as PreToolUse).
	AdditionalContext string
}
