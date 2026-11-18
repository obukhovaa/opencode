package agent

import (
	"context"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/task"
)

// backgroundMessages reads reports not yet included in this run's model
// history. Only complete synthetic background tool pairs are reports; schema,
// memory and other runtime context messages are not. Track IDs rather than a
// sequence watermark: a report may commit before the real tool-result
// message that follows it.
func (a *agent) backgroundMessages(ctx context.Context, sessionID string, seen map[string]bool) ([]message.Message, error) {
	if task.GlobalRegistry() == nil {
		return nil, nil
	}
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var fresh []message.Message
	for i := 0; i+1 < len(msgs); i++ {
		callMsg, resultMsg := msgs[i], msgs[i+1]
		if !callMsg.Synthetic || !resultMsg.Synthetic || callMsg.Role != message.Assistant || resultMsg.Role != message.Tool {
			continue
		}
		calls, results := callMsg.ToolCalls(), resultMsg.ToolResults()
		if len(calls) != 1 || len(results) != 1 || calls[0].ID != results[0].ToolCallID {
			continue
		}
		switch calls[0].Name {
		case tools.BashToolName, TaskToolName, tools.MonitorToolName:
		default:
			continue
		}
		if !seen[callMsg.ID] && !seen[resultMsg.ID] {
			fresh = append(fresh, callMsg, resultMsg)
		}
		i++
	}
	return fresh, nil
}

func rememberMessages(seen map[string]bool, msgs []message.Message) {
	for _, m := range msgs {
		seen[m.ID] = true
	}
}

func yielded(results *message.Message) bool {
	if results != nil {
		for _, r := range results.ToolResultsByToolName(tools.YieldToolName) {
			if !r.IsError {
				return true
			}
		}
	}
	return false
}

// waitForBackgroundReport waits for the FIRST report, not every task's
// termination. In particular a streaming monitor must remain running while
// the model reacts to an event. Subscribe before reading durable history and
// the pending set, so an event at either boundary cannot be lost.
func (a *agent) waitForBackgroundReport(ctx context.Context, reg task.Registry, sessionID string, seen map[string]bool) ([]message.Message, error) {
	if reg == nil {
		return nil, nil
	}
	// Preserve existing completion/stall handling, including registry Kill
	// (which closes Done before its final report is written).
	waitCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var waitErr error
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		waitErr = drainSessionTasks(waitCtx, reg, sessionID, a.stallPolicy())
	}()
	for {
		report := reg.WatchSession(sessionID)
		input := inbox.Default().Watch(sessionID)
		fresh, err := a.backgroundMessages(ctx, sessionID, seen)
		if err != nil || len(fresh) > 0 || inbox.Default().Len(sessionID) > 0 {
			return fresh, err
		}
		if len(reg.PendingForSession(sessionID, nil)) == 0 {
			// MarkFinished follows the write. Re-read after observing the
			// empty pending set to include a completion racing the first read.
			return a.backgroundMessages(ctx, sessionID, seen)
		}
		select {
		case <-report:
		case <-input:
		case <-done:
			if waitErr != nil {
				return nil, waitErr
			}
			return a.backgroundMessages(ctx, sessionID, seen)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// appendBackgroundReports runs only between model calls, after the real
// tool-call/result batch is complete. Synthetic pairs are appended together,
// never between an outstanding tool call and its result.
func (a *agent) appendBackgroundReports(ctx context.Context, sessionID string, history []message.Message, seen map[string]bool) ([]message.Message, []message.Message) {
	rememberMessages(seen, history)
	fresh, err := a.backgroundMessages(ctx, sessionID, seen)
	if err != nil {
		logging.Warn("Failed to read background reports", "session_id", sessionID, "error", err)
		return history, nil
	}
	rememberMessages(seen, fresh)
	return append(history, fresh...), fresh
}
