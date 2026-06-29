package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// CompletionInput is the contract for invoking EnqueueTaskCompletion.
//
// OriginatingToolName is what shows up as the ToolCall `Name` on the
// synthetic Assistant message. Renderers resolve the row's display by this
// name, so a background bash completion must use "bash", an async task
// completion must use "task", a monitor event must use "monitor". A
// cron-fired completion uses "task" (cron always invokes the task tool).
//
// The OriginatingToolCallID is informational metadata; the synthetic
// ToolCall.ID is freshly generated so the synthetic pair links to itself,
// not back to the original ack message.
type CompletionInput struct {
	SessionID             string
	OriginatingToolCallID string
	OriginatingToolName   string
	TaskID                string
	Kind                  Kind
	Status                Status
	ExitCode              *int
	// Input is the JSON-encoded tool-input shape the agent would have
	// produced when invoking the originating tool. Renderers format the
	// synthetic ToolCall by this input so a background bash completion
	// renders like a synchronous bash result. Empty input is acceptable
	// (renderer falls back to a generic display).
	Input string
	// Content is the agent-facing result body (what would appear in the
	// Tool ToolResult.Content for a synchronous call).
	Content string
	// SuppressIfNotified governs the notified-CAS gate for terminal
	// statuses. Bash/Task/Monitor callers set this to true; cron sets it
	// to false because cron does not use the dedupe flag.
	SuppressIfNotified bool
}

// EnqueueTaskCompletion writes the synthetic Assistant(ToolCall) +
// Tool(ToolResult) pair to the session and (if the session is idle)
// triggers a fresh agent.Run.
//
// Behavior summary:
//
//   - Status == StatusMonitorEvent: NOT subject to the notified-CAS gate;
//     NOT a state transition; auto-resume still fires if session is idle.
//   - Status in {Completed, Failed, Killed}: if SuppressIfNotified is true
//     AND the task's Notified flag is already true (set by an earlier
//     terminal call) the function returns nil silently.
//   - A terminal status flips Notified true via CAS before writing. The
//     CAS-loser returns nil silently — exactly one terminal completion
//     reaches the message log per task.
//   - The session-busy check is reused from internal/llm/agent's
//     IsSessionBusy. Busy sessions do NOT get a fresh Run kicked off;
//     the in-flight Run will pick up the new messages on its next
//     iteration of message list refresh.
func EnqueueTaskCompletion(ctx context.Context, in CompletionInput) error {
	if in.SessionID == "" {
		return errors.New("task: EnqueueTaskCompletion missing session id")
	}
	if in.OriginatingToolName == "" {
		return errors.New("task: EnqueueTaskCompletion missing originating tool name")
	}
	deps := getDeps()
	if deps == nil {
		return errors.New("task: EnqueueTaskCompletion called before SetDeps")
	}

	// Honor the notified dedupe gate for terminal statuses only.
	terminal := in.Status.IsTerminal()
	if terminal && in.SuppressIfNotified {
		if reg := GlobalRegistry(); reg != nil {
			if t, ok := reg.Get(in.TaskID); ok {
				if !t.Notified.CompareAndSwap(false, true) {
					return nil
				}
			}
		}
	}

	callID := newToolCallID()
	pair := SyntheticPair{
		AssistantToolCallID: callID,
		AssistantToolName:   in.OriginatingToolName,
		AssistantInput:      in.Input,
		ToolToolCallID:      callID,
		ToolName:            in.OriginatingToolName,
		ToolContent:         in.Content,
	}
	if err := deps.WritePair(ctx, in.SessionID, pair); err != nil {
		return fmt.Errorf("task: write synthetic pair: %w", err)
	}

	// Terminal statuses transition the registry state. Monitor-events do
	// not — the task remains running until the subprocess exits or is
	// killed.
	if terminal {
		if reg := GlobalRegistry(); reg != nil {
			state := stateFromStatus(in.Status)
			reg.MarkFinished(in.TaskID, state, in.ExitCode)
		}
	}

	// Auto-resume if the session is idle. Cron retains its own busy lock
	// (acquired externally), so when cron calls this primitive the session
	// is already locked-busy and ResumeSession is a no-op.
	if !deps.IsSessionBusy(in.SessionID) {
		deps.ResumeSession(in.SessionID)
	}
	return nil
}

func stateFromStatus(s Status) State {
	switch s {
	case StatusCompleted:
		return StateCompleted
	case StatusFailed:
		return StateFailed
	case StatusKilled:
		return StateKilled
	}
	return StateRunning
}

func newToolCallID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return "toolu_" + hex.EncodeToString(buf[:])
}
