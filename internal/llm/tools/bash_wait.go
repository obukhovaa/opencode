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

// pureWaitRe matches a foreground command whose sole effect is a wall-clock
// wait: `sleep <duration>` optionally followed by a single `;`/`&&` and one
// plain `echo`. Deliberately conservative — anything with pipes, redirects,
// command substitution, additional separators, or non-echo trailers runs
// normally. Covers the observed CD-4761 poll forms (`sleep 300; echo done`,
// `sleep 120; echo waited`).
var pureWaitRe = regexp.MustCompile(`^sleep\s+([0-9]+(?:\.[0-9]+)?)([smhd]?)\s*(?:(?:;|&&)\s*echo(?:\s+[^;&|<>$` + "`" + `]*)?)?$`)

// isPureWaitCommand reports whether cmd is a pure wall-clock wait (see
// pureWaitRe) and, when it is, the requested sleep duration (for logging /
// the interception note; the wait itself ignores it).
func isPureWaitCommand(cmd string) (bool, time.Duration) {
	m := pureWaitRe.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return false, 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return false, 0
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
	return true, time.Duration(n * float64(unit))
}

// nonMonitorFilter selects pending tasks eligible for the foreground-sleep
// redirect: bash and task kinds. Monitors are deliberately excluded — they
// are long-lived by design (bounded by max_events / a finite cmd /
// taskstop), so a stray sleep must not become a block on a monitor's whole
// lifetime. The end-of-turn drain in agent.processGeneration is what bounds
// monitors.
func nonMonitorFilter(t *task.Task) bool {
	return t.Kind != task.KindMonitor
}

// interceptForegroundWait implements the non-interactive anti-spin redirect
// (openspec bash-background-mode: "Foreground wall-clock waits are redirected
// to the task wait in non-interactive mode"). When the calling run is
// non-interactive AND the session has pending non-monitor background tasks
// AND the command is a pure wall-clock wait, the sleep is NOT executed;
// instead the call blocks on task.Registry.WaitForActiveTasks (bounded solely
// by ctx — the step deadline) and returns a synthetic bash-style response
// enumerating the tasks that reached terminal state during the wait.
//
// Returns (response, true) when the command was intercepted; (zero, false)
// when the command must run normally.
func interceptForegroundWait(ctx context.Context, command, sessionID string) (ToolResponse, bool) {
	if !IsNonInteractive(ctx) {
		return ToolResponse{}, false
	}
	isWait, requested := isPureWaitCommand(command)
	if !isWait {
		return ToolResponse{}, false
	}
	reg := task.GlobalRegistry()
	if reg == nil {
		return ToolResponse{}, false
	}
	preWait := reg.PendingForSession(sessionID, nonMonitorFilter)
	if len(preWait) == 0 {
		return ToolResponse{}, false
	}

	logging.Info(
		"Non-interactive anti-spin: redirecting foreground sleep to background-task wait",
		"session_id", sessionID,
		"requested_sleep", requested.String(),
		"pending_count", len(preWait),
	)
	startTime := time.Now()
	waitErr := reg.WaitForActiveTasks(ctx, sessionID, task.WaitOptions{IncludeMonitor: false})

	var b strings.Builder
	fmt.Fprintf(&b,
		"[non-interactive wait] Foreground `sleep` intercepted: this run is non-interactive and %d background task(s) were pending, so the runtime waited on their completion instead of sleeping (requested sleep: %s, actual wait: %s). Do NOT sleep or poll — completions arrive as synthetic tool results automatically.\n",
		len(preWait), requested, time.Since(startTime).Round(time.Millisecond),
	)

	var completed, stillPending []*task.Task
	for _, t := range preWait {
		if t.State() == task.StateRunning {
			stillPending = append(stillPending, t)
		} else {
			completed = append(completed, t)
		}
	}
	if len(completed) > 0 {
		b.WriteString("\nCompleted during the wait (synthetic completion results follow in the conversation):\n")
		for _, t := range completed {
			writeTaskLine(&b, t)
		}
	}
	if waitErr != nil {
		fmt.Fprintf(&b, "\nThe wait ended early: %v. Still pending:\n", waitErr)
		for _, t := range stillPending {
			writeTaskLine(&b, t)
		}
	} else if newlyPending := reg.PendingForSession(sessionID, nonMonitorFilter); len(newlyPending) > 0 {
		// Tasks registered after the wait's snapshot (e.g. a completion
		// handler chained more work). The end-of-turn drain will cover
		// them; surface the fact so the model doesn't re-sleep.
		b.WriteString("\nNewly pending tasks (spawned after the wait began — the runtime will wait for them at end of turn):\n")
		for _, t := range newlyPending {
			writeTaskLine(&b, t)
		}
	}

	metadata := BashResponseMetadata{
		StartTime:   startTime.UnixMilli(),
		EndTime:     time.Now().UnixMilli(),
		Description: "non-interactive wait for background tasks (intercepted sleep)",
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), metadata), true
}

func writeTaskLine(b *strings.Builder, t *task.Task) {
	fmt.Fprintf(b, " - task_id=%s kind=%s state=%s output_file=%s", t.ID, t.Kind, t.State(), t.OutputPath)
	if t.Description != "" {
		fmt.Fprintf(b, " desc=%q", t.Description)
	}
	b.WriteString("\n")
}
