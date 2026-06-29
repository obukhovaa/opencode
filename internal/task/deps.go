package task

import (
	"context"
)

// SyntheticPair is the message-shape-agnostic payload passed to Deps.WritePair.
// We avoid importing internal/message here because internal/message imports
// internal/llm/tools, and internal/llm/tools needs to import this package
// for the background-task tool code paths — that would be a cycle.
//
// The app-side adapter is responsible for translating these fields into the
// concrete message.CreateMessageParams shape and forwarding to
// message.Service.CreatePair.
type SyntheticPair struct {
	// Assistant content
	AssistantToolCallID string
	AssistantToolName   string
	AssistantInput      string
	// Tool content
	ToolToolCallID string
	ToolName       string
	ToolContent    string
}

// Deps is the small Service interface that EnqueueTaskCompletion uses to
// reach into the rest of opencode without creating an import cycle with
// `internal/llm/agent` or `internal/message`.
//
// Wired at app boot via SetDeps.
type Deps interface {
	// WritePair commits the synthetic Assistant(ToolCall) + Tool(ToolResult)
	// pair atomically against the session's message log. The app-side
	// adapter MUST mark both messages with `synthetic: true` so the bridge
	// filter suppresses tool-update indicator emission.
	WritePair(ctx context.Context, sessionID string, pair SyntheticPair) error
	// IsSessionBusy returns true if an agent.Run is currently in-flight on
	// the session (or another caller holds the cron-style busy lock).
	IsSessionBusy(sessionID string) bool
	// ResumeSession kicks off a fresh agent.Run with empty content against
	// the session. The empty content signals the agent that the next turn's
	// input comes from the just-written synthetic ToolResult. Implementations
	// MUST NOT block — the resume is fire-and-forget.
	ResumeSession(sessionID string)
}

var depsHolder Deps

// SetDeps installs the package-level dependency wiring. Called once at app
// boot. Tests can install a mock and reset between cases via the returned
// restore function.
func SetDeps(d Deps) func() {
	prev := depsHolder
	depsHolder = d
	return func() { depsHolder = prev }
}

// getDeps returns the installed dependency set, or nil if SetDeps was not
// called (e.g. lightweight test environments that exercise the registry
// alone). Callers must nil-check.
func getDeps() Deps { return depsHolder }
