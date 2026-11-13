package app

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestFlowStepProgressSummarizesOutputAndTimesEachAttempt(t *testing.T) {
	progress := flowStepProgress{}
	start := time.Unix(1000, 0)
	state := flow.FlowState{SessionID: "step-session", StepID: "review", Iteration: 1, Status: flow.FlowStatusRunning}
	require.Equal(t, "Flow · review · coder · running", progress.note(state, "coder", start))
	// Duplicate running events must not reset the timer.
	progress.note(state, "coder", start.Add(time.Second))
	state.Status, state.Output = flow.FlowStatusCompleted, "Reviewed 8 files.\n\nFixed retry handling; tests pass."
	note := progress.note(state, "coder", start.Add(65*time.Second))
	require.Contains(t, note, "completed · 1m5s")
	require.Contains(t, note, state.Output)
	require.Empty(t, progress)
	state.Status, state.Iteration = flow.FlowStatusRunning, 2
	progress.note(state, "coder", start.Add(time.Hour))
	state.Status, state.Output = flow.FlowStatusFailed, "Tests failed"
	note = progress.note(state, "coder", start.Add(time.Hour+3*time.Second))
	require.Contains(t, note, "failed · iteration 2 · 3s")
	require.Contains(t, note, "Tests failed")
	// Restored terminal states without an observed start do not invent a time.
	require.NotContains(t, progress.note(state, "coder", start), "3s")
}

func TestFlowStepOutputPreviewIsBoundedAndUnicodeSafe(t *testing.T) {
	for _, output := range []string{strings.Repeat("日本語 🧭", 600), strings.Repeat("Progress\n", 100)} {
		preview := flowOutputPreview(output)
		require.True(t, utf8.ValidString(preview))
		require.LessOrEqual(t, len([]rune(preview)), 2030)
		require.LessOrEqual(t, len(strings.Split(preview, "\n")), 16)
		require.Contains(t, preview, "Full output: /flow")
	}
}

func TestFlowStepProgressUsesLatestAssistantOutputWhenResultIsEmpty(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	_, err := a.Messages.Create(ctx, root, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Review done: three fixes, all tests pass."}}})
	require.NoError(t, err)
	state := flow.FlowState{SessionID: root, StepID: "review", Status: flow.FlowStatusCompleted}
	note := a.flowStepProgressNote(ctx, flowStepProgress{}, state, "coder")
	require.Contains(t, note, "Review done: three fixes, all tests pass.")
}

func TestFlowStepProgressRendersStructuredOutputAsJSON(t *testing.T) {
	state := flow.FlowState{StepID: "audit", Status: flow.FlowStatusCompleted, IsStructOutput: true, Output: `{"findings":"Done","issues":[]}`}
	note := (flowStepProgress{}).note(state, "coder", time.Now())
	require.Contains(t, note, "```json\n{\n")
	require.Contains(t, note, "  \"findings\": \"Done\"")
	require.True(t, strings.HasSuffix(note, "\n```"))
}
