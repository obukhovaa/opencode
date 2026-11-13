package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/message"
)

// Track each step attempt independently. A resumed runner starts a new timing
// interval, so downtime is not reported as execution time.
type flowStepProgress map[string]time.Time

func (p flowStepProgress) note(state flow.FlowState, agent string, now time.Time) string {
	key := fmt.Sprintf("%s/%s/%d", state.StepID, state.SessionID, state.Iteration)
	if state.Status == flow.FlowStatusRunning {
		if _, exists := p[key]; !exists {
			p[key] = now
		}
	}
	text := "Flow · " + state.StepID
	if agent != "" {
		text += " · " + agent
	}
	text += " · " + string(state.Status)
	if state.Iteration > 1 {
		text += fmt.Sprintf(" · iteration %d", state.Iteration)
	}
	if !finishedFlowStep(state.Status) {
		return text
	}
	if started, ok := p[key]; ok {
		duration := max(time.Duration(0), now.Sub(started)).Truncate(time.Second)
		elapsed := duration.String()
		if duration == 0 {
			elapsed = "<1s"
		}
		text += " · " + elapsed
		delete(p, key)
	}
	output := strings.TrimSpace(state.Output)
	if output == "" && state.Status == flow.FlowStatusFailed {
		output = "Step failed; inspect its session or resume the flow."
	}
	if output != "" {
		text += "\n\n" + flowOutputPreview(output)
	}
	return text
}

func finishedFlowStep(status flow.FlowStatus) bool {
	return status == flow.FlowStatusCompleted || status == flow.FlowStatusFailed || status == flow.FlowStatusPostponed
}

func flowOutputPreview(output string) string {
	truncated := false
	lines := strings.Split(output, "\n")
	if len(lines) > 14 {
		output = strings.Join(lines[:14], "\n")
		truncated = true
	}
	runes := []rune(output)
	if len(runes) > 2000 {
		output = string(runes[:2000])
		truncated = true
	}
	if truncated {
		output = strings.TrimSpace(output) + "\n\n… Full output: /flow"
	}
	return output
}

func (app *App) flowStepProgressNote(ctx context.Context, progress flowStepProgress, state flow.FlowState, agent string) string {
	// Prose-only steps may not have a structured result. Use their latest
	// assistant text, also available in the flow page's Current output panel.
	if finishedFlowStep(state.Status) && strings.TrimSpace(state.Output) == "" && app.Messages != nil && state.SessionID != "" {
		if messages, err := app.Messages.List(ctx, state.SessionID); err == nil {
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == message.Assistant {
					if text := strings.TrimSpace(messages[i].Content().String()); text != "" {
						state.Output = text
						break
					}
				}
			}
		}
	}
	return progress.note(state, agent, time.Now())
}
