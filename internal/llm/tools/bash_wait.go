package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/task"
)

// leadingWaitRe matches a foreground command that BEGINS with a wall-clock
// wait: `sleep <duration>` either alone or immediately followed by a `;` or
// `&&` separator and a trailer (anything, including pipes and redirects —
// the trailer is run verbatim after the wait). The separator is anchored:
// `sleep 5 & echo bg` does not match, because `&` is neither separator and
// that command backgrounds the sleep rather than waiting on it.
var leadingWaitRe = regexp.MustCompile(`(?s)^sleep\s+([0-9]+(?:\.[0-9]+)?)([smhd]?)\s*(?:$|(?:;|&&)\s*(.*))$`)

// splitLeadingWait reports whether cmd begins with a wall-clock wait and,
// when it does, the requested duration (for the interception note; the
// wait itself ignores it) and the trailer that follows the separator ("" when
// the command is the bare wait).
func splitLeadingWait(cmd string) (requested time.Duration, trailer string, ok bool) {
	m := leadingWaitRe.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", false
	}
	unit := time.Second
	switch m[2] {
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	}
	return time.Duration(n * float64(unit)), strings.TrimSpace(m[3]), true
}

// stripLeadingWaits drops every wall-clock wait that LEADS the trailer, so a
// trailer cannot smuggle the sleep back in: `sleep 0; sleep 300` would
// otherwise sleep 300s under a result that reads "Do NOT sleep".
func stripLeadingWaits(trailer string) string {
	for {
		_, rest, ok := splitLeadingWait(trailer)
		if !ok {
			return strings.TrimSpace(trailer)
		}
		trailer = rest
	}
}

// nonMonitorFilter selects pending tasks eligible for the foreground-sleep
// redirect: bash and task kinds. Monitors are deliberately excluded — they
// are long-lived by design (bounded by max_events / a finite cmd /
// taskstop) and a monitor whose pattern has not matched emits nothing, so
// a stray sleep must not become a block on a monitor's whole lifetime. The
// end-of-turn drain in agent.processGeneration is what bounds monitors.
func nonMonitorFilter(t *task.Task) bool {
	return t.Kind != task.KindMonitor
}

// interceptForegroundWait implements the non-interactive anti-spin redirect
// (openspec bash-background-mode: "Foreground wall-clock waits are redirected
// to the task wait in non-interactive mode"). When the calling run is
// non-interactive AND the session or its direct child sessions have pending
// non-monitor background tasks AND the command begins with a wall-clock
// wait, the sleep is NOT executed: the call blocks on
// task.Registry.WaitForActiveTasks over the session-and-children scope
// (bounded solely by ctx — the step deadline), then runs the trailer, if
// any, through the ordinary synchronous path and returns a synthetic
// bash-style response: the interception note, the tasks that reached a
// terminal state during the wait, and the trailer's real output, exit code
// and spill-file metadata.
//
// Scope is the caller's session plus sessions whose parent is the caller —
// one level of descent, NOT the flow root, which every parallel step of a
// flow shares. The same scope is applied to the pre-check and to the wait:
// widening only the pre-check would return in microseconds having waited
// for nothing.
//
// Permission: this runs AFTER bashTool.Run's permission gate, which
// evaluated the full command string — trailer included. The trailer is
// deliberately not re-gated; a second evaluation would double-prompt.
//
// Returns (response, true) when the command was intercepted; (zero, false)
// when the command must run normally.
func interceptForegroundWait(ctx context.Context, params BashParams, workdir, sessionID string) (ToolResponse, bool) {
	if !IsNonInteractive(ctx) {
		return ToolResponse{}, false
	}
	requested, trailer, isWait := splitLeadingWait(params.Command)
	if !isWait {
		return ToolResponse{}, false
	}
	reg := task.GlobalRegistry()
	if reg == nil {
		return ToolResponse{}, false
	}
	preWait := reg.PendingForSessionTree(sessionID, nonMonitorFilter)
	if len(preWait) == 0 {
		return ToolResponse{}, false
	}
	trailer = stripLeadingWaits(trailer)

	logging.Info(
		"Non-interactive anti-spin: redirecting foreground sleep to background-task wait",
		"session_id", sessionID,
		"requested_sleep", requested.String(),
		"pending_count", len(preWait),
		"has_trailer", trailer != "",
	)
	startTime := time.Now()
	waitErr := reg.WaitForActiveTasks(ctx, sessionID, task.WaitOptions{IncludeMonitor: false, Scope: task.ScopeSessionAndChildren})

	var b strings.Builder
	fmt.Fprintf(&b,
		"[non-interactive wait] Foreground `sleep` intercepted: this run is non-interactive and %d background task(s) were pending in this session or a subagent it spawned, so the runtime waited on their completion instead of sleeping (requested sleep: %s, actual wait: %s). Do NOT sleep or poll — completions arrive as synthetic tool results automatically.\n",
		len(preWait), requested, time.Since(startTime).Round(time.Millisecond),
	)

	var completedOwn, completedChild, stillPending []*task.Task
	for _, t := range preWait {
		switch {
		case t.State() == task.StateRunning:
			stillPending = append(stillPending, t)
		case t.SessionID == sessionID:
			completedOwn = append(completedOwn, t)
		default:
			completedChild = append(completedChild, t)
		}
	}
	if len(completedOwn) > 0 {
		b.WriteString("\nCompleted during the wait (synthetic completion results follow in the conversation):\n")
		for _, t := range completedOwn {
			writeTaskLine(&b, t, sessionID)
		}
	}
	if len(completedChild) > 0 {
		b.WriteString("\nCompleted during the wait, owned by a subagent session (its completion result was delivered to that session, not here — read output_file for the result):\n")
		for _, t := range completedChild {
			writeTaskLine(&b, t, sessionID)
		}
	}
	if waitErr != nil {
		fmt.Fprintf(&b, "\nThe wait ended early: %v. Still pending:\n", waitErr)
		for _, t := range stillPending {
			writeTaskLine(&b, t, sessionID)
		}
	} else if newlyPending := reg.PendingForSessionTree(sessionID, nonMonitorFilter); len(newlyPending) > 0 {
		// Tasks registered after the wait's snapshot (e.g. a completion
		// handler chained more work). The end-of-turn drain will cover
		// the session's own; surface the fact so the model doesn't re-sleep.
		b.WriteString("\nNewly pending tasks (spawned after the wait began — the runtime will wait for this session's own at end of turn):\n")
		for _, t := range newlyPending {
			writeTaskLine(&b, t, sessionID)
		}
	}

	metadata := BashResponseMetadata{
		StartTime:   startTime.UnixMilli(),
		EndTime:     time.Now().UnixMilli(),
		Description: "non-interactive wait for background tasks (intercepted sleep)",
	}
	switch {
	case trailer == "":
	case waitErr != nil:
		// A dead ctx would only yield "Command was aborted before
		// completion" under the deadline note, which explains nothing.
		fmt.Fprintf(&b, "\nThe trailing command (`%s`) was NOT run: the wait ended on a cancelled context.\n", trailer)
	default:
		output, exitCode, tempPath, err := runForeground(ctx, trailer, workdir, params.Timeout)
		if err != nil {
			fmt.Fprintf(&b, "\nThe trailing command (`%s`) could not be run after the wait: %v\n", trailer, err)
			break
		}
		if output == "" {
			output = "no output"
		}
		fmt.Fprintf(&b, "\n--- `%s` (run after the wait) ---\n%s\n", trailer, output)
		metadata.Description = "non-interactive wait for background tasks (intercepted sleep), then the trailing command"
		metadata.ExitCode = exitCode
		metadata.TempFilePath = tempPath
		metadata.EndTime = time.Now().UnixMilli()
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), metadata), true
}

// writeTaskLine renders one task for the interception note, marking tasks
// owned by a session other than the caller's.
func writeTaskLine(b *strings.Builder, t *task.Task, callerSessionID string) {
	fmt.Fprintf(b, " - task_id=%s kind=%s state=%s output_file=%s", t.ID, t.Kind, t.State(), t.OutputPath)
	if t.SessionID != callerSessionID {
		fmt.Fprintf(b, " owner=%s", t.SessionID)
	}
	if t.Description != "" {
		fmt.Fprintf(b, " desc=%q", t.Description)
	}
	b.WriteString("\n")
}
