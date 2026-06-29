// Package task provides the background-task registry and the
// EnqueueTaskCompletion primitive used to inject synthetic
// Assistant(ToolCall)+Tool(ToolResult) pairs into an opencode session.
//
// Three call sites use this package today:
//
//   - The bash tool's run_in_background path spawns a subprocess, returns an
//     immediate ack, and on subprocess exit calls EnqueueTaskCompletion.
//   - The task tool's async path spawns a subagent, returns an immediate ack,
//     and on subagent exit calls EnqueueTaskCompletion.
//   - The monitor tool spawns a long-lived subprocess and calls
//     EnqueueTaskCompletion on every coalesced match window (per-event,
//     non-terminal) plus once at terminal exit.
//   - The cron scheduler also calls EnqueueTaskCompletion when its
//     fire-and-forget task subagent finishes.
//
// All state is in-memory. Opencode restart loses every in-flight task; the
// only durable artifact is the per-task output file at
// <config.Data.Directory>/tasks/<task_id>.out which is swept at boot.
package task

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Kind identifies the lifecycle source of a background task.
type Kind string

const (
	KindBash    Kind = "bash"
	KindTask    Kind = "task"
	KindMonitor Kind = "monitor"
	KindCron    Kind = "cron"
)

// IDPrefix maps a Kind to its task-id prefix. The format makes IDs visually
// debuggable and matches the format requested by the background-tasks spec.
func (k Kind) IDPrefix() string {
	switch k {
	case KindBash:
		return "shell"
	case KindTask:
		return "agent"
	case KindMonitor:
		return "monitor"
	case KindCron:
		return "cron"
	}
	return "task"
}

// State is the registry-tracked terminal-or-running state.
type State int8

const (
	StateRunning State = iota
	StateCompleted
	StateFailed
	StateKilled
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateKilled:
		return "killed"
	}
	return "unknown"
}

// Status is the per-completion status sent to EnqueueTaskCompletion.
// StatusMonitorEvent is the non-terminal "matched some lines in this window"
// status; the other three are terminal.
type Status string

const (
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusKilled       Status = "killed"
	StatusMonitorEvent Status = "monitor-event"
)

// IsTerminal returns whether the status represents the end of a task's
// lifecycle. Non-terminal statuses (currently only StatusMonitorEvent) MUST
// NOT be subject to the notified-CAS dedupe gate and MUST NOT transition the
// task's State out of Running.
func (s Status) IsTerminal() bool {
	return s != StatusMonitorEvent
}

// Task is the in-memory bookkeeping for one background task. The struct is
// intended to be created by a tool's spawn path and handed to the global
// Registry — the registry owns the lifetime.
type Task struct {
	ID                    string
	SessionID             string
	Kind                  Kind
	StartedAt             time.Time
	OutputPath            string
	OriginatingToolCallID string
	OriginatingToolName   string
	// Description is a short human-readable label used by tasklist output.
	Description string
	// Notified is the per-task dedupe flag. EnqueueTaskCompletion CAS-flips
	// this from false → true before writing a TERMINAL synthetic pair; a
	// losing CAS means another path already notified the parent session and
	// the call returns silently.
	Notified atomic.Bool
	// state is read under the registry's RLock and mutated only via
	// MarkFinished/Kill on the registry. We keep it as int32 so it's atomic
	// even outside the registry lock (for race-detector-friendly debugging).
	state atomic.Int32
	// finishedAtNanos holds the UnixNano timestamp of the terminal
	// transition; 0 means "not yet finished". Stored atomically so
	// tasklist consumers can read it without taking the registry lock.
	finishedAtNanos atomic.Int64
	// exitCode is set by MarkFinished when applicable (only for KindBash and
	// KindMonitor — subagents do not surface OS exit codes).
	exitCode atomic.Int32
	hasExit  atomic.Bool

	// Lifecycle hooks. Exactly one of these is non-nil depending on Kind.
	// Cancel is set for KindTask (subagent context cancellation).
	Cancel context.CancelFunc
	// Proc is set for KindBash and KindMonitor (OS process signalling).
	Proc *os.Process

	// done is closed exactly once when the task transitions to a terminal
	// state (StateCompleted / StateFailed / StateKilled). Created in
	// Registry.Register, closed via doneOnce in Registry.MarkFinished and
	// Registry.Kill. Used by Registry.WaitForActiveTasks to signal that
	// a task has reached terminal state without polling. Nil before
	// Register (which initialises it); callers MUST go through the
	// registry to interact with this channel.
	done     chan struct{}
	doneOnce sync.Once
}

// Done returns a channel that is closed when the task reaches a terminal
// state. The channel is created during Registry.Register; querying Done()
// before registration returns nil (and select-on-nil blocks forever, which
// is intentional — pre-registration tasks have no observable terminal
// signal).
func (t *Task) Done() <-chan struct{} { return t.done }

// signalDone closes the done channel exactly once. Called by registry
// methods after the terminal state transition is recorded.
func (t *Task) signalDone() {
	t.doneOnce.Do(func() {
		if t.done != nil {
			close(t.done)
		}
	})
}

// State returns the task's current state in a concurrency-safe way.
func (t *Task) State() State { return State(t.state.Load()) }

// FinishedAt returns the task's terminal transition time; zero if still running.
func (t *Task) FinishedAt() time.Time {
	ns := t.finishedAtNanos.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// ExitCode returns the exit code if one was recorded; ok==false otherwise.
func (t *Task) ExitCode() (int, bool) {
	if !t.hasExit.Load() {
		return 0, false
	}
	return int(t.exitCode.Load()), true
}
